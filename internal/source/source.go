// Package source handles fetching APKs from various sources.
package source

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/config"
	"golang.org/x/net/proxy"
)

// newSecureHTTPClient creates an HTTP client with security best practices:
// - TLS 1.2 minimum version
// - Connection pooling limits to prevent resource exhaustion
// - Reasonable timeouts
// - One Tor SOCKS5 retry on HTTP 403
//
// Uses a total request timeout — suitable for metadata/API calls with small responses.
// For large file downloads, use newDownloadHTTPClient instead.
func newSecureHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: withTorFallback(&http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		}),
		CheckRedirect: validateRedirect,
	}
}

// downloadStallTimeout is the duration after which a download is considered stalled
// if no data has been received.
const downloadStallTimeout = 30 * time.Second

// downloadMaxAttempts is how many times DownloadHTTP retries transient failures
// (unexpected EOF, connection reset) before giving up.
const downloadMaxAttempts = 3

// downloadRetryBackoff is the base delay between download attempts.
const downloadRetryBackoff = 1 * time.Second

var errUnsafeDownloadURL = errors.New("refusing unsafe download URL")

const torSOCKSAddress = "127.0.0.1:9050"

// newDownloadHTTPClient creates an HTTP client for large file downloads.
// Unlike newSecureHTTPClient, it does NOT set a total request timeout.
// Instead, the caller should wrap the response body with a StallTimeoutReader
// to detect stalled downloads. Retries once through Tor on HTTP 403.
//
// Every redirect hop is validated with the same HTTPS-outside-loopback rule
// applied to explicit configuration URLs, so a download cannot be steered to
// an insecure or internal target by following a redirect.
func newDownloadHTTPClient() *http.Client {
	return &http.Client{
		Transport: withTorFallback(&http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second, // timeout for server to start responding
		}),
		CheckRedirect: validateRedirect,
	}
}

func validateRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("too many redirects")
	}
	return validateDownloadURL(req.URL.String())
}

// validateDownloadURL applies the same HTTPS-outside-loopback rule that
// config.ValidateURL enforces on explicit configuration URLs (repository,
// release_source, asset_url) to a download target. It is used both for the
// initial request and for every redirect hop, so a redirect or a dynamically
// extracted URL (e.g. a GitLab interstitial target or a web asset extractor
// result) cannot smuggle an insecure or internal target past validation.
func validateDownloadURL(rawURL string) error {
	if err := config.ValidateURL(rawURL); err != nil {
		return fmt.Errorf("%w: %v", errUnsafeDownloadURL, err)
	}
	return nil
}

// newTorHTTPClient creates an HTTP client routed through a locally running
// Tor SOCKS5 proxy. The transport is not wrapped with Tor fallback.
func newTorHTTPClient() (*http.Client, error) {
	dialer, err := proxy.SOCKS5("tcp", torSOCKSAddress, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("create Tor SOCKS5 dialer: %w", err)
	}

	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("Tor SOCKS5 dialer does not support context cancellation")
	}

	return &http.Client{
		Transport: &http.Transport{
			DialContext: contextDialer.DialContext,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		CheckRedirect: validateRedirect,
	}, nil
}

var torHTTPClient = newTorHTTPClient

// SetTorHTTPClientForTest replaces the Tor HTTP client factory used on 403
// fallback. The returned function restores the previous factory.
func SetTorHTTPClientForTest(fn func() (*http.Client, error)) func() {
	prev := torHTTPClient
	torHTTPClient = fn
	return func() { torHTTPClient = prev }
}

// torFallbackTransport retries a request once through Tor when the direct
// response is HTTP 403. The Tor retry is unauthenticated.
type torFallbackTransport struct {
	base http.RoundTripper
}

func withTorFallback(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &torFallbackTransport{base: base}
}

func (t *torFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusForbidden {
		return resp, nil
	}
	resp.Body.Close()

	torClient, err := torHTTPClient()
	if err != nil {
		return nil, fmt.Errorf("request received 403; retry through Tor unavailable (start Tor with SOCKS5 on %s): %w", torSOCKSAddress, err)
	}
	torReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create unauthenticated Tor request: %w", err)
	}
	resp, err = torClient.Do(torReq)
	if err != nil {
		return nil, fmt.Errorf("request received 403; retry through Tor at %s failed: %w", torSOCKSAddress, err)
	}
	return resp, nil
}

// DoWithTorFallback sends an HTTP request. If the client transport does not
// already provide Tor fallback and the response is 403, it retries once
// through the local Tor SOCKS5 proxy without credentials.
func DoWithTorFallback(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = &http.Client{Transport: withTorFallback(nil)}
	} else if _, ok := client.Transport.(*torFallbackTransport); !ok {
		cloned := *client
		cloned.Transport = withTorFallback(client.Transport)
		client = &cloned
	}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}

// checkHTTPStatus validates an HTTP response status code and returns a descriptive error.
// Returns nil if status is OK. Handles common status codes with actionable error messages.
// serviceName is used in error messages (e.g., "F-Droid", "GitHub API").
func checkHTTPStatus(resp *http.Response, serviceName string) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%s returned 404 Not Found: %s", serviceName, resp.Request.URL)
	case http.StatusForbidden:
		return fmt.Errorf("%s access forbidden (403): you may be rate limited or IP blocked", serviceName)
	case http.StatusTooManyRequests:
		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter != "" {
			return fmt.Errorf("%s rate limited (429): retry after %s seconds", serviceName, retryAfter)
		}
		return fmt.Errorf("%s rate limited (429): too many requests", serviceName)
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return fmt.Errorf("%s temporarily unavailable (status %d): try again later", serviceName, resp.StatusCode)
	default:
		return fmt.Errorf("%s returned status %d", serviceName, resp.StatusCode)
	}
}

// StallTimeoutReader wraps an io.Reader and returns an error if no data is
// received for the specified duration. Unlike http.Client.Timeout, this only
// triggers when the download stalls — not after a fixed total time.
type StallTimeoutReader struct {
	Reader  io.Reader
	Timeout time.Duration
	timer   *time.Timer
}

func (r *StallTimeoutReader) Read(p []byte) (int, error) {
	if r.timer == nil {
		r.timer = time.NewTimer(r.Timeout)
	} else {
		r.timer.Reset(r.Timeout)
	}

	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := r.Reader.Read(p)
		ch <- result{n, err}
	}()

	select {
	case res := <-ch:
		r.timer.Stop()
		return res.n, res.err
	case <-r.timer.C:
		if closer, ok := r.Reader.(io.Closer); ok {
			_ = closer.Close()
		}
		return 0, fmt.Errorf("download stalled: no data received for %s", r.Timeout)
	}
}

// unsupportedArchRegex matches APK filenames that explicitly indicate unsupported architectures.
// We only want arm64-v8a. Filter out x86, x86_64 (Intel/AMD) and armeabi/armeabi-v7a (32-bit ARM).
var unsupportedArchRegex = regexp.MustCompile(`(?i)(^|[-_.])(x86_64|x86|armeabi-v7a|armeabi)([-_.]|$)`)

// MaxRemoteDownloadSize is the maximum size for remote downloads (images, metadata, etc.)
// This prevents memory exhaustion from malicious or unexpectedly large responses.
const MaxRemoteDownloadSize = 20 * 1024 * 1024 // 20MB

// MaxJSONResponseSize bounds API responses decoded into memory.
const MaxJSONResponseSize int64 = 10 * 1024 * 1024

// MaxDownloadSize is the hard cap for APK downloads (and any HTTP downloads
// via DownloadHTTP) to avoid excessive bandwidth or disk usage.
const MaxDownloadSize int64 = 600 * 1024 * 1024 // 600MB

// maxReleasesToCheck is the maximum number of releases to iterate through
// when looking for one with valid APKs (some repos publish desktop and mobile separately).
const maxReleasesToCheck = 10

// Asset represents a downloadable APK asset.
type Asset struct {
	Name        string // Filename
	URL         string // Download URL (empty for local files)
	Size        int64  // Size in bytes (0 if unknown)
	LocalPath   string // Local file path (set after download or for local sources)
	ContentType string // MIME type (if known)
	ExcludeURL  bool   // If true, don't include URL in event (use Blossom URL only)
}

// Release represents a release containing one or more APK assets.
type Release struct {
	Version    string    // Version string (e.g., "1.2.3" or "v1.2.3")
	TagName    string    // Git tag name (if applicable)
	Name       string    // Human-readable release title (if applicable)
	Changelog  string    // Release notes/changelog
	Assets     []*Asset  // Available APK assets
	PreRelease bool      // Whether this is a pre-release
	URL        string    // Release page URL (e.g., https://github.com/user/repo/releases/tag/v1.0)
	CreatedAt  time.Time // Release creation/publish date (zero if unknown)
}

// Source is the interface for APK sources.
type Source interface {
	// Type returns the source type.
	Type() config.SourceType

	// FetchLatestRelease fetches the latest release information.
	FetchLatestRelease(ctx context.Context) (*Release, error)

	// Download downloads an asset and returns the local path.
	// For local sources, this may just return the existing path.
	// The optional progress callback is called during download.
	Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error)
}

// ParsedRelease contains a release with its parsed APK information.
type ParsedRelease struct {
	Release *Release
	APK     *apk.APKInfo
	Asset   *Asset
}

// Options contains options for creating a source.
type Options struct {
	// BaseDir is the base directory for resolving relative paths.
	// Typically the directory containing the config file.
	BaseDir string

	// IncludePreReleases includes pre-releases when fetching the latest release (--pre-release).
	IncludePreReleases bool
}

// New creates a new source based on the config.
func New(cfg *config.Config) (Source, error) {
	return NewWithOptions(cfg, Options{})
}

// NewWithOptions creates a new source with options.
func NewWithOptions(cfg *config.Config, opts Options) (Source, error) {
	sourceType := cfg.GetSourceType()

	switch sourceType {
	case config.SourceLocal:
		localPath := ""
		if cfg.ReleaseSource != nil {
			localPath = cfg.ReleaseSource.LocalPath
		}
		return NewLocalWithBase(localPath, opts.BaseDir)
	case config.SourceGitHub:
		gh, err := NewGitHub(cfg)
		if err != nil {
			return nil, err
		}
		gh.IncludePreReleases = opts.IncludePreReleases
		return gh, nil
	case config.SourceGitLab:
		gl, err := NewGitLab(cfg)
		if err != nil {
			return nil, err
		}
		gl.IncludePreReleases = opts.IncludePreReleases
		return gl, nil
	case config.SourceGitea:
		gt, err := NewGitea(cfg)
		if err != nil {
			return nil, err
		}
		gt.IncludePreReleases = opts.IncludePreReleases
		return gt, nil
	case config.SourceFDroid:
		fd, err := NewFDroid(cfg)
		if err != nil {
			return nil, err
		}
		return fd, nil
	case config.SourceWeb:
		web, err := NewWeb(cfg)
		if err != nil {
			return nil, err
		}
		return web, nil
	default:
		return nil, fmt.Errorf("unsupported source type: %s", sourceType)
	}
}

// DownloadProgress is called during downloads to report progress.
type DownloadProgress func(downloaded, total int64)

// Downloader wraps an io.Reader to track download progress.
type ProgressReader struct {
	Reader     io.Reader
	Total      int64
	Downloaded int64
	OnProgress DownloadProgress
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	pr.Downloaded += int64(n)
	if pr.OnProgress != nil {
		pr.OnProgress(pr.Downloaded, pr.Total)
	}
	return n, err
}

// prepareDownloadDest resolves the destination file path for a downloaded
// asset, creating destDir if needed and sanitizing name against path
// traversal. This is a shared helper for all sources that download to a
// caller-owned temporary directory.
func prepareDownloadDest(destDir, name string) (string, error) {
	if destDir == "" {
		destDir = os.TempDir()
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Security: sanitize the filename to prevent path traversal attacks.
	safeName := filepath.Base(name)
	if safeName == "." || safeName == ".." || safeName == "" {
		return "", fmt.Errorf("invalid asset filename: %s", name)
	}
	destPath := filepath.Join(destDir, safeName)

	// Security: validate the final path is within destDir.
	cleanDest := filepath.Clean(destPath)
	cleanDir := filepath.Clean(destDir)
	if !strings.HasPrefix(cleanDest, cleanDir+string(filepath.Separator)) && cleanDest != cleanDir {
		return "", fmt.Errorf("invalid destination path: path traversal detected")
	}
	return destPath, nil
}

// doGet issues a GET request with optional headers (e.g. Authorization) and
// validates a 200 response. The caller owns the returned response and must
// close its body.
func doGet(ctx context.Context, client *http.Client, rawURL string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := DoWithTorFallback(ctx, client, req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download failed with status %d: %s", resp.StatusCode, rawURL)
	}
	return resp, nil
}

// writeDownloadResponse streams resp.Body to destPath, enforcing
// MaxDownloadSize and the stall timeout, and reporting progress. The caller
// retains ownership of resp and must close its body.
func writeDownloadResponse(resp *http.Response, destPath string, expectedSize int64, progress DownloadProgress) error {
	// Use Content-Length from response if available, otherwise use expected size.
	total := resp.ContentLength
	if total <= 0 {
		total = expectedSize
	}
	if total > MaxDownloadSize {
		return fmt.Errorf("download size %d bytes exceeds limit of %d bytes", total, MaxDownloadSize)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	var reader io.Reader = &StallTimeoutReader{
		Reader:  resp.Body,
		Timeout: downloadStallTimeout,
	}
	if progress != nil {
		reader = &ProgressReader{
			Reader:     reader,
			Total:      total, // May be 0 if unknown; callback will receive 0 as total
			OnProgress: progress,
		}
	}

	// Enforce maximum download size even when Content-Length is missing.
	limitedReader := &io.LimitedReader{
		R: reader,
		N: MaxDownloadSize + 1, // allow detecting overflow
	}

	written, err := io.Copy(f, limitedReader)
	if err != nil {
		closeErr := f.Close()
		os.Remove(destPath)
		return fmt.Errorf("failed to write file: %w", errors.Join(err, closeErr))
	}
	if err := f.Close(); err != nil {
		os.Remove(destPath)
		return fmt.Errorf("close download: %w", err)
	}
	if written > MaxDownloadSize {
		os.Remove(destPath)
		return fmt.Errorf("download exceeded limit of %d bytes", MaxDownloadSize)
	}

	// Detect truncated responses when the server advertised a Content-Length.
	if resp.ContentLength > 0 && written != resp.ContentLength {
		os.Remove(destPath)
		return fmt.Errorf("failed to write file: %w", io.ErrUnexpectedEOF)
	}
	return nil
}

// decodeJSONResponse reads and decodes a remote JSON response within maxBytes.
// The cap-plus-one read detects bodies that omit or lie about Content-Length.
func decodeJSONResponse(resp *http.Response, maxBytes int64, target interface{}) error {
	if maxBytes <= 0 {
		return fmt.Errorf("JSON response limit must be positive")
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("JSON response exceeds maximum size of %d bytes", maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return fmt.Errorf("read JSON response: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return fmt.Errorf("JSON response exceeds maximum size of %d bytes", maxBytes)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode JSON response: %w", err)
	}
	return nil
}

// downloadOnce performs a single fetch-and-write attempt. fetch performs the
// (possibly source-specific) HTTP exchange for this attempt and must return a
// fresh response — retries must not reuse an already-consumed body.
func downloadOnce(ctx context.Context, destPath string, expectedSize int64, progress DownloadProgress, fetch func(context.Context) (*http.Response, error)) error {
	resp, err := fetch(ctx)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return writeDownloadResponse(resp, destPath, expectedSize, progress)
}

// downloadWithRetries retries downloadOnce for transient failures (unexpected
// EOF, connection reset, stalls) up to downloadMaxAttempts, backing off
// between attempts and honoring ctx cancellation. This is the shared retry
// harness behind DownloadHTTP; source-specific download logic (e.g. GitLab's
// interstitial resolution) can supply its own fetch function to gain the same
// size limit, stall detection, and bounded retries.
func downloadWithRetries(ctx context.Context, destPath string, expectedSize int64, progress DownloadProgress, fetch func(context.Context) (*http.Response, error)) error {
	var lastErr error
	for attempt := 1; attempt <= downloadMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 1 {
			backoff := downloadRetryBackoff * time.Duration(attempt-1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		err := downloadOnce(ctx, destPath, expectedSize, progress, fetch)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientDownloadError(err) || attempt == downloadMaxAttempts {
			return err
		}
		os.Remove(destPath)
	}
	return lastErr
}

// DownloadHTTP downloads a file from a URL to destPath with optional progress
// reporting. This is the shared pipeline for all HTTP-based sources: it
// enforces the size limit, validates the URL and every redirect hop, applies
// stall-based timeout detection (fails only if no data is received for 30s,
// not after a fixed total time), and retries transient failures (unexpected
// EOF, connection reset) up to downloadMaxAttempts. headers are attached to
// every request attempt, e.g. a source's Authorization token; pass nil when
// none are needed.
func DownloadHTTP(ctx context.Context, rawURL, destPath string, expectedSize int64, headers map[string]string, progress DownloadProgress) error {
	if err := validateDownloadURL(rawURL); err != nil {
		return err
	}
	fetch := func(ctx context.Context) (*http.Response, error) {
		return doGet(ctx, newDownloadHTTPClient(), rawURL, headers)
	}
	return downloadWithRetries(ctx, destPath, expectedSize, progress, fetch)
}

// isTransientDownloadError reports whether err is worth retrying.
func isTransientDownloadError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errUnsafeDownloadURL) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := err.Error()
	for _, substr := range []string{
		"unexpected EOF",
		"connection reset",
		"broken pipe",
		"connection refused",
		"i/o timeout",
		"TLS handshake timeout",
		"server closed idle connection",
		"download stalled",
	} {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

// FilterUnsupportedArchitectures removes APK assets that explicitly indicate
// unsupported architectures (x86, x86_64, etc.) in their filename.
// Assets without architecture indicators or with supported architectures (arm64-v8a, armeabi-v7a) are kept.
func FilterUnsupportedArchitectures(assets []*Asset) []*Asset {
	filtered := make([]*Asset, 0, len(assets))
	for _, asset := range assets {
		if !HasUnsupportedArchitecture(asset.Name) {
			filtered = append(filtered, asset)
		}
	}
	return filtered
}

// HasUnsupportedArchitecture returns true if the filename explicitly indicates
// an unsupported architecture (x86, x86_64, etc.).
func HasUnsupportedArchitecture(filename string) bool {
	// Only check APK files
	if !strings.HasSuffix(strings.ToLower(filename), ".apk") {
		return false
	}
	return unsupportedArchRegex.MatchString(filename)
}

// IsAPKURL checks if a URL points to an APK file.
// Properly handles URLs with query parameters.
func IsAPKURL(rawURL string) bool {
	u := strings.ToLower(rawURL)
	// Strip query parameters
	if idx := strings.Index(u, "?"); idx >= 0 {
		u = u[:idx]
	}
	return strings.HasSuffix(u, ".apk")
}

// IsAPKAsset checks if an asset (by name or URL) is an APK file.
func IsAPKAsset(name, url string) bool {
	if strings.HasSuffix(strings.ToLower(name), ".apk") {
		return true
	}
	return IsAPKURL(url)
}

// HasValidAPKs returns true if the assets contain at least one APK file.
// Used to determine if a release is a valid mobile release (vs desktop-only).
func HasValidAPKs(assets []*Asset) bool {
	for _, asset := range assets {
		if IsAPKAsset(asset.Name, asset.URL) {
			return true
		}
	}
	return false
}
