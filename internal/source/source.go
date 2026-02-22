// Package source handles fetching APKs from various sources.
package source

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/config"
)

// newSecureHTTPClient creates an HTTP client with security best practices:
// - TLS 1.2 minimum version
// - Connection pooling limits to prevent resource exhaustion
// - Reasonable timeouts
//
// Uses a total request timeout — suitable for metadata/API calls with small responses.
// For large file downloads, use newDownloadHTTPClient instead.
func newSecureHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// downloadStallTimeout is the duration after which a download is considered stalled
// if no data has been received.
const downloadStallTimeout = 30 * time.Second

// newDownloadHTTPClient creates an HTTP client for large file downloads.
// Unlike newSecureHTTPClient, it does NOT set a total request timeout.
// Instead, the caller should wrap the response body with a StallTimeoutReader
// to detect stalled downloads.
func newDownloadHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second, // timeout for server to start responding
		},
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
		return 0, fmt.Errorf("download stalled: no data received for %s", r.Timeout)
	}
}

// unsupportedArchRegex matches APK filenames that explicitly indicate unsupported architectures.
// We only want arm64-v8a. Filter out x86, x86_64 (Intel/AMD) and armeabi/armeabi-v7a (32-bit ARM).
var unsupportedArchRegex = regexp.MustCompile(`(?i)[-_\.](x86_64|x86|i686|i386|amd64|armeabi-v7a|armeabi)[-_\.]`)

// MaxRemoteDownloadSize is the maximum size for remote downloads (images, metadata, etc.)
// This prevents memory exhaustion from malicious or unexpectedly large responses.
const MaxRemoteDownloadSize = 20 * 1024 * 1024 // 20MB

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

	// SkipCache bypasses ETag cache for GitHub sources (--overwrite-release).
	SkipCache bool

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
		gh.SkipCache = opts.SkipCache
		gh.IncludePreReleases = opts.IncludePreReleases
		return gh, nil
	case config.SourceGitLab:
		return NewGitLab(cfg)
	case config.SourceGitea:
		gt, err := NewGitea(cfg)
		if err != nil {
			return nil, err
		}
		gt.IncludePreReleases = opts.IncludePreReleases
		return gt, nil
	case config.SourceFDroid:
		return NewFDroid(cfg)
	case config.SourceWeb:
		web, err := NewWeb(cfg)
		if err != nil {
			return nil, err
		}
		web.SkipCache = opts.SkipCache
		return web, nil
	default:
		return nil, fmt.Errorf("unsupported source type: %s", sourceType)
	}
}

// DownloadProgress is called during downloads to report progress.
type DownloadProgress func(downloaded, total int64)

// CacheClearer is an optional interface for sources that support cache clearing.
// Sources that cache release data (like GitHub with ETags) should implement this
// to allow clearing the cache when publishing fails.
type CacheClearer interface {
	// ClearCache removes any cached release data.
	ClearCache() error
}

// CacheCommitter is an optional interface for sources that support deferred cache commits.
// Sources like GitHub store cache data in memory during fetch, then commit to disk
// only after successful publishing via CommitCache().
type CacheCommitter interface {
	// CommitCache persists the pending cache data to disk.
	// Should be called after successful publishing.
	CommitCache() error
}

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

// DownloadHTTP downloads a file from a URL with optional progress reporting.
// This is a shared helper for all HTTP-based sources.
// Uses stall-based timeout: fails only if no data received for 30s, not after a fixed total time.
func DownloadHTTP(ctx context.Context, client *http.Client, url, destPath string, expectedSize int64, progress DownloadProgress) error {
	// Use download client without total timeout
	dlClient := newDownloadHTTPClient()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	// Copy auth headers from the original client's request if any
	// (the passed client is unused for the request itself, but callers
	// may set headers on the request before calling this function)
	_ = client // kept for API compatibility

	resp, err := dlClient.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d: %s", resp.StatusCode, url)
	}

	// Use Content-Length from response if available, otherwise use expected size
	total := resp.ContentLength
	if total <= 0 {
		total = expectedSize
	}

	if total > MaxDownloadSize {
		return fmt.Errorf("download size %d bytes exceeds limit of %d bytes", total, MaxDownloadSize)
	}

	// Create destination file
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	// Wrap body with stall timeout — fails only if no data received for 30s
	var reader io.Reader = &StallTimeoutReader{
		Reader:  resp.Body,
		Timeout: downloadStallTimeout,
	}

	// Wrap with progress tracking if callback provided
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
		os.Remove(destPath)
		return fmt.Errorf("failed to write file: %w", err)
	}

	if written > MaxDownloadSize {
		os.Remove(destPath)
		return fmt.Errorf("download exceeded limit of %d bytes", MaxDownloadSize)
	}

	return nil
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

// DownloadCacheDir returns the directory for caching downloaded APKs.
func DownloadCacheDir() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "zsp", "downloads")
}

// DownloadCacheKey generates a cache key for a download URL.
// The key is a hex-encoded SHA256 hash prefix of the URL.
func DownloadCacheKey(downloadURL string) string {
	h := sha256.Sum256([]byte(downloadURL))
	return hex.EncodeToString(h[:16]) // 32 hex chars
}

// GetCachedDownload checks if a download is already cached.
// Returns the path if cached and valid, empty string otherwise.
func GetCachedDownload(downloadURL, filename string) string {
	cacheDir := DownloadCacheDir()
	cacheKey := DownloadCacheKey(downloadURL)
	cachedPath := filepath.Join(cacheDir, cacheKey+"_"+filepath.Base(filename))

	info, err := os.Stat(cachedPath)
	if err != nil || info.Size() == 0 {
		return "" // Not cached or invalid
	}
	return cachedPath
}

// DeleteCachedDownload removes a cached download file.
// Returns nil if the file doesn't exist or was successfully deleted.
func DeleteCachedDownload(downloadURL, filename string) error {
	cacheDir := DownloadCacheDir()
	cacheKey := DownloadCacheKey(downloadURL)
	cachedPath := filepath.Join(cacheDir, cacheKey+"_"+filepath.Base(filename))

	err := os.Remove(cachedPath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// SaveToDownloadCache saves a downloaded file to the cache.
// Returns the cached path on success.
func SaveToDownloadCache(downloadURL, filename, srcPath string) (string, error) {
	cacheDir := DownloadCacheDir()
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}

	cacheKey := DownloadCacheKey(downloadURL)
	cachedPath := filepath.Join(cacheDir, cacheKey+"_"+filepath.Base(filename))

	// Copy file to cache
	src, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer src.Close()

	dst, err := os.Create(cachedPath)
	if err != nil {
		return "", err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(cachedPath)
		return "", err
	}

	return cachedPath, nil
}
