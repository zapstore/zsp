package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// downloadStallTimeout is the duration after which a download is considered stalled
// if no data has been received.
const downloadStallTimeout = 30 * time.Second

// downloadMaxAttempts is how many times DownloadHTTP retries transient failures
// (unexpected EOF, connection reset) before giving up.
const downloadMaxAttempts = 3

// downloadRetryBackoff is the base delay between download attempts.
const downloadRetryBackoff = 1 * time.Second

// MaxRemoteDownloadSize is the maximum size for remote downloads (images, metadata, etc.)
// This prevents memory exhaustion from malicious or unexpectedly large responses.
const MaxRemoteDownloadSize = 20 * 1024 * 1024 // 20MB

// MaxJSONResponseSize bounds API responses decoded into memory.
const MaxJSONResponseSize int64 = 10 * 1024 * 1024

// MaxDownloadSize is the hard cap for APK downloads (and any HTTP downloads
// via DownloadHTTP) to avoid excessive bandwidth or disk usage.
const MaxDownloadSize int64 = 600 * 1024 * 1024 // 600MB

// stallReader closes the response body when no data arrives for the timeout,
// which unblocks a Read stuck on a silent connection. The timer runs only
// while a Read waits on the body, so a slow consumer never counts as a stall.
type stallReader struct {
	body    io.ReadCloser
	timeout time.Duration
	timer   *time.Timer
	stalled atomic.Bool
}

func newStallReader(body io.ReadCloser, timeout time.Duration) *stallReader {
	r := &stallReader{body: body, timeout: timeout}
	r.timer = time.AfterFunc(timeout, func() {
		r.stalled.Store(true)
		_ = body.Close() // unblocks the pending Read; its error is reported as a stall
	})
	r.timer.Stop()
	return r
}

func (r *stallReader) stallErr() error {
	return fmt.Errorf("%w: no data received for %s", errDownloadStalled, r.timeout)
}

func (r *stallReader) Read(p []byte) (int, error) {
	if r.stalled.Load() {
		return 0, r.stallErr()
	}
	r.timer.Reset(r.timeout)
	n, err := r.body.Read(p)
	r.timer.Stop()
	if err != nil && r.stalled.Load() {
		return n, r.stallErr()
	}
	return n, err
}

func (r *stallReader) Close() error {
	r.timer.Stop()
	return r.body.Close()
}

// readBounded reads all of r and fails when it holds more than max bytes,
// instead of silently truncating.
func readBounded(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w: exceeds %d bytes", errTooLarge, max)
	}
	return data, nil
}

// decodeJSONResponse reads and decodes a remote JSON response within maxBytes.
// The cap-plus-one read detects bodies that omit or lie about Content-Length.
func decodeJSONResponse(resp *http.Response, maxBytes int64, target any) error {
	if maxBytes <= 0 {
		return errors.New("JSON response limit must be positive")
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("%w: JSON response exceeds %d bytes", errTooLarge, maxBytes)
	}
	body, err := readBounded(resp.Body, maxBytes)
	if err != nil {
		return fmt.Errorf("read JSON response: %w", err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode JSON response: %w", err)
	}
	return nil
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
		return "", fmt.Errorf("create destination directory: %w", err)
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
		return "", errors.New("invalid destination path: path traversal detected")
	}
	return destPath, nil
}

// doGet issues a GET request with optional headers (e.g. Authorization) and
// validates a 200 response. The caller owns the returned response and must
// close its body.
func doGet(ctx context.Context, client *http.Client, rawURL string, headers map[string]string) (*http.Response, error) {
	req, err := newRequest(ctx, http.MethodGet, rawURL)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := DoWithTorFallback(ctx, client, req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if err := checkHTTPStatus(resp, "download"); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

// normalizeSHA256 returns digest as lowercase hex, or "" when it is not a
// SHA-256 digest. A "sha256:" prefix is accepted.
func normalizeSHA256(digest string) string {
	digest = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(digest)), "sha256:")
	if len(digest) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return ""
	}
	return digest
}

// writeDownloadResponse streams resp.Body to destPath, enforcing
// MaxDownloadSize and the stall timeout, and reporting progress. When
// wantSHA256 is set the content must hash to it. The caller retains ownership
// of resp and must close its body.
func writeDownloadResponse(resp *http.Response, destPath string, expectedSize int64, wantSHA256 string, progress DownloadProgress) error {
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
		return fmt.Errorf("create file: %w", err)
	}
	stall := newStallReader(resp.Body, downloadStallTimeout)
	defer stall.Close()
	var reader io.Reader = stall
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

	var dst io.Writer = f
	hash := sha256.New()
	if wantSHA256 != "" {
		dst = io.MultiWriter(f, hash)
	}

	written, err := io.Copy(dst, limitedReader)
	if err != nil {
		closeErr := f.Close()
		os.Remove(destPath)
		return fmt.Errorf("write file: %w", errors.Join(err, closeErr))
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
		return fmt.Errorf("write file: %w", io.ErrUnexpectedEOF)
	}
	if wantSHA256 != "" {
		if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, wantSHA256) {
			os.Remove(destPath)
			return fmt.Errorf("%w: downloaded SHA-256 %s, published %s", ErrChecksumMismatch, got, wantSHA256)
		}
	}
	return nil
}

// downloadOnce performs a single fetch-and-write attempt. fetch performs the
// (possibly source-specific) HTTP exchange for this attempt and must return a
// fresh response — retries must not reuse an already-consumed body.
func downloadOnce(ctx context.Context, destPath string, expectedSize int64, wantSHA256 string, progress DownloadProgress, fetch func(context.Context) (*http.Response, error)) error {
	resp, err := fetch(ctx)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return writeDownloadResponse(resp, destPath, expectedSize, wantSHA256, progress)
}

// downloadWithRetries retries downloadOnce for transient failures (see
// IsTransient) up to downloadMaxAttempts, backing off between attempts and
// honoring ctx cancellation. This is the shared retry harness behind
// DownloadHTTP; source-specific download logic (e.g. GitLab's interstitial
// resolution) can supply its own fetch function to gain the same size limit,
// stall detection, checksum verification, and bounded retries.
func downloadWithRetries(ctx context.Context, destPath string, expectedSize int64, wantSHA256 string, progress DownloadProgress, fetch func(context.Context) (*http.Response, error)) error {
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

		err := downloadOnce(ctx, destPath, expectedSize, wantSHA256, progress, fetch)
		if err == nil {
			return nil
		}
		lastErr = err
		if !IsTransient(err) || attempt == downloadMaxAttempts {
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
// not after a fixed total time), and retries transient failures up to
// downloadMaxAttempts. headers are attached to the first request of every
// attempt, e.g. a source's token; pass nil when none are needed. Redirects to
// another host drop credential headers.
func DownloadHTTP(ctx context.Context, rawURL, destPath string, expectedSize int64, headers map[string]string, progress DownloadProgress) error {
	return downloadHTTP(ctx, nil, rawURL, destPath, expectedSize, "", headers, progress)
}

// downloadHTTP is DownloadHTTP with a caller-supplied client, so a source
// reuses its connections and its single Tor fallback, and an optional SHA-256
// the content must match. A nil client builds a default one.
func downloadHTTP(ctx context.Context, client *http.Client, rawURL, destPath string, expectedSize int64, wantSHA256 string, headers map[string]string, progress DownloadProgress) error {
	if err := validateDownloadURL(rawURL); err != nil {
		return err
	}
	if client == nil {
		client = newDownloadHTTPClient(nil)
	}
	fetch := func(ctx context.Context) (*http.Response, error) {
		return doGet(ctx, client, rawURL, headers)
	}
	return downloadWithRetries(ctx, destPath, expectedSize, wantSHA256, progress, fetch)
}

// DownloadProgress is called during downloads to report progress.
type DownloadProgress func(downloaded, total int64)

// ProgressReader wraps an io.Reader to track download progress.
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
