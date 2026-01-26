package source

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/PaesslerAG/jsonpath"
	"github.com/PuerkitoBio/goquery"
	"github.com/zapstore/zsp/internal/config"
)

// Web implements Source for web scraping with version extraction.
type Web struct {
	cfg       *config.Config
	client    *http.Client
	cacheDir  string
	SkipCache bool // Set to true to bypass version/HTTP cache

	// pendingCache holds the cache from the last fetch, not yet committed to disk.
	// Call CommitCache() after successful publishing to persist it.
	pendingCache *webCache
}

// webCache stores version and HTTP caching information for a web source.
type webCache struct {
	// Version-based caching (when version extractor is configured)
	Version  string `json:"version,omitempty"`
	AssetURL string `json:"asset_url,omitempty"`

	// HTTP caching (for versionless URLs - ETag/Last-Modified/Content-Length)
	ETag          string `json:"etag,omitempty"`
	LastModified  string `json:"last_modified,omitempty"`
	ContentLength int64  `json:"content_length,omitempty"` // Fallback when ETag/Last-Modified unavailable
}

// NewWeb creates a new web scraping source.
func NewWeb(cfg *config.Config) (*Web, error) {
	if cfg.ReleaseSource == nil || !cfg.ReleaseSource.IsWebSource {
		return nil, fmt.Errorf("invalid web source configuration")
	}

	// Set up cache directory
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	cacheDir = filepath.Join(cacheDir, "zsp", "web")

	return &Web{
		cfg:      cfg,
		client:   newSecureHTTPClient(30 * time.Second),
		cacheDir: cacheDir,
	}, nil
}

// resolveRedirects follows redirects and returns the final URL.
// Uses HEAD request to avoid downloading the full content.
func (w *Web) resolveRedirects(ctx context.Context, url string) (string, error) {
	// Create a client that tracks redirects but still follows them
	var finalURL string
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			finalURL = req.URL.String()
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to resolve redirects: %w", err)
	}
	defer resp.Body.Close()

	// If no redirects occurred, finalURL won't be set
	if finalURL == "" {
		finalURL = url
	}

	return finalURL, nil
}

// Type returns the source type.
func (w *Web) Type() config.SourceType {
	return config.SourceWeb
}

// cacheFilePath returns the file path for storing cached URL data.
func (w *Web) cacheFilePath() string {
	// Hash the source URL (or asset_url if no url) for a unique filename
	cacheKey := w.cfg.ReleaseSource.URL
	if cacheKey == "" {
		cacheKey = w.cfg.ReleaseSource.AssetURL
	}
	h := sha256.Sum256([]byte(cacheKey))
	return filepath.Join(w.cacheDir, hex.EncodeToString(h[:8])+".json")
}

// loadCache reads the cached data from disk.
func (w *Web) loadCache() *webCache {
	data, err := os.ReadFile(w.cacheFilePath())
	if err != nil {
		return nil
	}
	var cache webCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	return &cache
}

// saveCache writes the cache data to disk.
func (w *Web) saveCache(cache *webCache) error {
	if err := os.MkdirAll(w.cacheDir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return os.WriteFile(w.cacheFilePath(), data, 0644)
}

// ClearCache removes the cached data.
func (w *Web) ClearCache() error {
	w.pendingCache = nil // Clear pending cache
	cachePath := w.cacheFilePath()
	err := os.Remove(cachePath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// CommitCache saves the pending cache to disk.
// This should be called after successful publishing to persist the cache.
func (w *Web) CommitCache() error {
	if w.pendingCache == nil {
		return nil // Nothing to commit
	}
	err := w.saveCache(w.pendingCache)
	if err == nil {
		w.pendingCache = nil // Clear pending after successful commit
	}
	return err
}

// FetchLatestRelease fetches the latest release from a web source.
//
// The method supports three modes:
//
// 1. Version extraction mode (version_html, version_json, or version_redirect):
//   - Fetches the URL and extracts version using the configured extractor
//   - Substitutes {version} in asset_url to get the download URL
//   - Caches by version - skips if version hasn't changed
//
// 2. Direct URL mode (asset_url only, no version extractor):
//   - Uses asset_url directly as the download URL
//   - Uses HTTP caching (ETag/Last-Modified) to detect changes
//   - Version is extracted from the downloaded APK
//
// 3. Direct URL shorthand (release_source: "https://example.com/app.apk"):
//   - Same as mode 2, but specified as a simple string
func (w *Web) FetchLatestRelease(ctx context.Context) (*Release, error) {
	repo := w.cfg.ReleaseSource

	var version string
	var assetURL string
	var newCache *webCache

	if repo.HasVersionExtractor() {
		// Mode 1: Extract version from page, construct asset URL
		var err error
		version, err = w.extractVersion(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to extract version: %w", err)
		}

		// Substitute {version} in asset_url
		assetURL = strings.ReplaceAll(repo.AssetURL, "{version}", version)

		// Check cache - if version hasn't changed, skip
		if !w.SkipCache {
			cache := w.loadCache()
			if cache != nil && cache.Version == version {
				return nil, ErrNotModified
			}
		}

		newCache = &webCache{
			Version:  version,
			AssetURL: assetURL,
		}
	} else {
		// Mode 2/3: Direct URL (versionless), use HTTP caching
		assetURL = repo.AssetURL

		// Follow redirects to get the final URL (e.g., for GitHub release redirects)
		finalURL, err := w.resolveRedirects(ctx, assetURL)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve URL: %w", err)
		}
		assetURL = finalURL

		// Check for changes using HTTP caching headers (on the final URL)
		cache := w.loadCache()
		if !w.SkipCache && cache != nil {
			modified, etag, lastMod, contentLen, err := w.checkHTTPCacheHeaders(ctx, assetURL, cache.ETag, cache.LastModified, cache.ContentLength)
			if err != nil {
				return nil, fmt.Errorf("failed to check for updates: %w", err)
			}
			if !modified {
				return nil, ErrNotModified
			}
			// Store new headers for later commit
			newCache = &webCache{
				ETag:          etag,
				LastModified:  lastMod,
				ContentLength: contentLen,
			}
		} else {
			// No cache, fetch headers for future use
			_, etag, lastMod, contentLen, err := w.checkHTTPCacheHeaders(ctx, assetURL, "", "", 0)
			if err != nil {
				// Non-fatal - we can still download without caching
				newCache = &webCache{}
			} else {
				newCache = &webCache{
					ETag:          etag,
					LastModified:  lastMod,
					ContentLength: contentLen,
				}
			}
		}

		// Version will be extracted from APK after download
		version = ""
	}

	// Store cache for later commit
	w.pendingCache = newCache

	// Create asset
	// If asset_url doesn't have {version} placeholder, exclude original URL from event
	// (only Blossom URL from x tag should be used). This applies even if there's a
	// version extractor - the URL is static so shouldn't be advertised.
	hasVersionPlaceholder := repo.HasVersionPlaceholder()

	// Extract filename from URL path (without query parameters)
	assetName := filepath.Base(assetURL)
	if parsed, err := url.Parse(assetURL); err == nil {
		assetName = filepath.Base(parsed.Path)
	}

	asset := &Asset{
		Name:       assetName,
		URL:        assetURL,
		ExcludeURL: !hasVersionPlaceholder,
	}

	return &Release{
		Version: version,
		Assets:  []*Asset{asset},
	}, nil
}

// extractVersion extracts the version string using the configured extractor.
func (w *Web) extractVersion(ctx context.Context, repo *config.ReleaseSource) (string, error) {
	if repo.Version == nil {
		return "", fmt.Errorf("no version extractor configured")
	}

	v := repo.Version
	switch v.Mode() {
	case "html":
		return w.extractVersionHTML(ctx, v)
	case "json":
		return w.extractVersionJSON(ctx, v)
	case "header":
		return w.extractVersionHeader(ctx, v)
	default:
		return "", fmt.Errorf("invalid version extractor mode")
	}
}

// extractVersionHTML extracts version from an HTML page using CSS selector.
func (w *Web) extractVersionHTML(ctx context.Context, v *config.VersionExtractor) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", v.URL, nil)
	if err != nil {
		return "", err
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("page fetch failed with status %d", resp.StatusCode)
	}

	// Security: Limit response size to prevent memory exhaustion
	doc, err := goquery.NewDocumentFromReader(io.LimitReader(resp.Body, MaxRemoteDownloadSize))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Find element using CSS selector
	sel := doc.Find(v.Selector)
	if sel.Length() == 0 {
		return "", fmt.Errorf("no element found matching selector %q", v.Selector)
	}

	// Extract value: text content if attribute is empty, otherwise the specified attribute
	var value string
	if v.Attribute == "" {
		value = strings.TrimSpace(sel.First().Text())
	} else {
		var exists bool
		value, exists = sel.First().Attr(v.Attribute)
		if !exists {
			return "", fmt.Errorf("attribute %q not found on element", v.Attribute)
		}
	}

	// Apply match pattern to extract version
	return extractWithPattern(value, v.Match)
}

// extractVersionJSON extracts version from a JSON API using JSONPath.
func (w *Web) extractVersionJSON(ctx context.Context, v *config.VersionExtractor) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", v.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API fetch failed with status %d", resp.StatusCode)
	}

	// Security: Limit response size to prevent memory exhaustion
	limitedReader := io.LimitReader(resp.Body, MaxRemoteDownloadSize)

	// Parse JSON
	var data interface{}
	if err := json.NewDecoder(limitedReader).Decode(&data); err != nil {
		return "", fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Evaluate JSONPath
	result, err := jsonpath.Get(v.Path, data)
	if err != nil {
		return "", fmt.Errorf("JSONPath %q failed: %w", v.Path, err)
	}

	// Convert result to string
	var value string
	switch val := result.(type) {
	case string:
		value = val
	case float64:
		// Handle numeric versions (e.g., 1.0 becomes "1")
		if val == float64(int64(val)) {
			value = fmt.Sprintf("%d", int64(val))
		} else {
			value = fmt.Sprintf("%g", val)
		}
	default:
		value = fmt.Sprintf("%v", val)
	}

	// Apply optional match pattern
	if v.Match != "" {
		return extractWithPattern(value, v.Match)
	}
	return value, nil
}

// extractVersionHeader extracts version from HTTP redirect headers.
func (w *Web) extractVersionHeader(ctx context.Context, v *config.VersionExtractor) (string, error) {
	// Don't follow redirects - we want to capture the redirect header
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Stop at first redirect
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", v.URL, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch page: %w", err)
	}
	defer resp.Body.Close()

	// Check if we got a redirect
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("expected redirect, got status %d", resp.StatusCode)
	}

	// Get the header value
	headerName := strings.ToLower(v.Header)
	var value string
	for name, values := range resp.Header {
		if strings.ToLower(name) == headerName && len(values) > 0 {
			value = values[0]
			break
		}
	}

	if value == "" {
		return "", fmt.Errorf("header %q not found in response", v.Header)
	}

	// Apply match pattern to extract version
	return extractWithPattern(value, v.Match)
}

// extractWithPattern applies a regex pattern to extract a version string.
// The pattern should have at least one capture group.
// If pattern is empty, returns the trimmed value as-is.
func extractWithPattern(value, pattern string) (string, error) {
	// If no pattern, return the value directly
	if pattern == "" {
		return strings.TrimSpace(value), nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}

	matches := re.FindStringSubmatch(value)
	if len(matches) < 2 {
		return "", fmt.Errorf("pattern %q did not match value %q", pattern, value)
	}

	return matches[1], nil
}

// checkHTTPCacheHeaders checks if a resource has been modified using ETag/Last-Modified/Content-Length.
// Returns (modified, newETag, newLastModified, newContentLength, error).
func (w *Web) checkHTTPCacheHeaders(ctx context.Context, url, etag, lastModified string, contentLength int64) (bool, string, string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return true, "", "", 0, err
	}

	// Add conditional headers if we have cached values
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return true, "", "", 0, fmt.Errorf("HEAD request failed: %w", err)
	}
	defer resp.Body.Close()

	// 304 Not Modified means resource hasn't changed
	if resp.StatusCode == http.StatusNotModified {
		return false, etag, lastModified, contentLength, nil
	}

	// Get new caching headers
	newETag := resp.Header.Get("ETag")
	newLastMod := resp.Header.Get("Last-Modified")
	newContentLen := resp.ContentLength

	// If we had old values and they match new ones, not modified
	if etag != "" && newETag != "" && etag == newETag {
		return false, newETag, newLastMod, newContentLen, nil
	}
	if lastModified != "" && newLastMod != "" && lastModified == newLastMod {
		return false, newETag, newLastMod, newContentLen, nil
	}

	// Fallback: check Content-Length if no ETag/Last-Modified available
	if etag == "" && lastModified == "" && contentLength > 0 && newContentLen > 0 {
		if contentLength == newContentLen {
			return false, newETag, newLastMod, newContentLen, nil
		}
	}

	return true, newETag, newLastMod, newContentLen, nil
}

// Download downloads an APK from the web.
// Uses a download cache to avoid re-downloading the same file.
func (w *Web) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", fmt.Errorf("asset has no download URL")
	}

	// Check download cache first
	if cachedPath := GetCachedDownload(asset.URL, asset.Name); cachedPath != "" {
		asset.LocalPath = cachedPath
		return cachedPath, nil
	}

	// Create destination directory if needed
	if destDir == "" {
		destDir = os.TempDir()
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Security: Sanitize filename to prevent path traversal attacks
	safeName := filepath.Base(asset.Name)
	if safeName == "." || safeName == ".." || safeName == "" {
		return "", fmt.Errorf("invalid asset filename: %s", asset.Name)
	}
	destPath := filepath.Join(destDir, safeName)

	// Security: Validate the final path is within destDir
	cleanDest := filepath.Clean(destPath)
	cleanDir := filepath.Clean(destDir)
	if !strings.HasPrefix(cleanDest, cleanDir+string(filepath.Separator)) && cleanDest != cleanDir {
		return "", fmt.Errorf("invalid destination path: path traversal detected")
	}

	// Download the file
	req, err := http.NewRequestWithContext(ctx, "GET", asset.URL, nil)
	if err != nil {
		return "", err
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d: %s", resp.StatusCode, asset.URL)
	}

	// Use Content-Length from response if available, otherwise use asset size
	total := resp.ContentLength
	if total <= 0 {
		total = asset.Size
	}

	// Create destination file
	file, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Wrap reader with progress tracking if callback provided
	var reader io.Reader = resp.Body
	if progress != nil && total > 0 {
		reader = &ProgressReader{
			Reader:     resp.Body,
			Total:      total,
			OnProgress: progress,
		}
	}

	_, err = io.Copy(file, reader)
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	// Save to download cache (best-effort, ignore errors)
	if cachedPath, err := SaveToDownloadCache(asset.URL, asset.Name, destPath); err == nil {
		os.Remove(destPath)
		destPath = cachedPath
	}

	asset.LocalPath = destPath
	return destPath, nil
}
