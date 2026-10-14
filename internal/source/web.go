package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/zapstore/zsp/internal/sanitize"
)

// webCache stores HTTP validators for a versionless direct URL. Content-Length
// is not a validator: a rebuilt file often keeps its size.
type webCache struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// Web implements Source for web scraping with version extraction.
type Web struct {
	cfg            *config.Config
	client         *http.Client
	downloadClient *http.Client
	cacheDir       string
	SkipHTTPCache  bool
	pendingCache   *webCache
}

// NewWeb creates a new web scraping source.
func NewWeb(cfg *config.Config) (*Web, error) {
	return newWeb(cfg, Options{})
}

func newWeb(cfg *config.Config, opts Options) (*Web, error) {
	if cfg.ReleaseSource == nil || !cfg.ReleaseSource.IsWebSource {
		return nil, errors.New("invalid web source configuration")
	}

	tor := opts.tor()
	return &Web{
		cfg:            cfg,
		client:         newSecureHTTPClient(30*time.Second, tor),
		downloadClient: newDownloadHTTPClient(tor),
		cacheDir:       sourceCacheDir(opts.CacheDir, "web"),
	}, nil
}

// probedURL is the result of a HEAD (or ranged GET) used to name a direct
// download without fetching the body. ContentType and ContentLength come from
// that response; FinalURL is the last redirect hop, or rawURL when there is none.
type probedURL struct {
	FinalURL      string
	ContentType   string
	ContentLength int64
}

// resolveRedirects follows redirects and returns the final URL plus the
// response media type and length. HEAD is used first to avoid downloading the
// asset, with a ranged GET fallback for endpoints that deliberately reject HEAD.
func (w *Web) resolveRedirects(ctx context.Context, rawURL string) (probedURL, error) {
	// Track redirects on a copy of the client, which still follows them.
	var finalURL string
	client := *w.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		finalURL = req.URL.String()
		return validateRedirect(req, via)
	}

	req, err := newRequest(ctx, http.MethodHead, rawURL)
	if err != nil {
		return probedURL{}, err
	}

	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == http.StatusMethodNotAllowed {
		resp.Body.Close()
		req, err = newRequest(ctx, http.MethodGet, rawURL)
		if err == nil {
			req.Header.Set("Range", "bytes=0-0")
			resp, err = client.Do(req)
		}
	}
	if err != nil {
		return probedURL{}, fmt.Errorf("resolve redirects: %w", err)
	}
	defer resp.Body.Close()

	// If no redirects occurred, finalURL won't be set
	if finalURL == "" {
		finalURL = rawURL
	}
	probed := probedURL{FinalURL: finalURL}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		probed.ContentType = resp.Header.Get("Content-Type")
		if resp.ContentLength > 0 {
			probed.ContentLength = resp.ContentLength
		}
	}
	return probed, nil
}

// Type returns the source type.
func (w *Web) Type() config.SourceType {
	return config.SourceWeb
}

// FetchLatestRelease fetches the latest release from a web source.
//
// The method supports four modes:
//
// 1. Version extraction mode (version + asset_url with {version} template):
//   - Fetches the URL and extracts version using the configured extractor
//   - Substitutes {version} in asset_url to get the download URL
//
// 2. Asset extraction mode (version + asset extractor):
//   - Extracts version from page (for the release event) if configured
//   - Extracts download URL dynamically from page using asset extractor
//   - Used for sites with dynamic/expiring download URLs (e.g., CDN tokens)
//
// 3. Direct URL mode (asset_url only, no version extractor):
//   - Uses asset_url directly as the download URL
//   - Version is extracted from the downloaded APK
//
// 4. Direct URL shorthand (release_source: "https://example.com/app.apk"):
//   - Same as mode 3, but specified as a simple string
func (w *Web) FetchLatestRelease(ctx context.Context) (*Release, error) {
	repo := w.cfg.ReleaseSource

	var version string
	var assetURL string
	var nameURL string // optional override for filename (e.g. resolved redirect target)
	var probedType string
	var probedLength int64

	if repo.HasAssetExtractor() {
		// Mode 2: Extract asset URL from page (version optionally extracted too)
		if repo.HasVersionExtractor() {
			var err error
			version, err = w.extractVersion(ctx, repo)
			if err != nil {
				return nil, fmt.Errorf("extract version: %w", err)
			}
		}

		// Extract the download URL dynamically from the page
		var err error
		assetURL, err = w.extractAssetURL(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("extract asset URL: %w", err)
		}
	} else if repo.HasVersionExtractor() {
		// Mode 1: Extract version from page, construct asset URL
		var err error
		version, err = w.extractVersion(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("extract version: %w", err)
		}

		// Substitute {version} in asset_url. The version comes from a remote
		// page, so the result is validated like any other runtime-derived URL.
		assetURL = strings.ReplaceAll(repo.AssetURL, "{version}", version)
		if err := validateDownloadURL(assetURL); err != nil {
			return nil, fmt.Errorf("asset URL for version %q: %w", sanitize.Text(version), err)
		}
	} else {
		// Mode 2/3: Direct URL (versionless)
		assetURL = repo.AssetURL

		// Resolve redirects for filename purposes only. Keep downloading from the
		// original URL so tokenized CDN targets (e.g. telegram.org → telesco.pe?token=...)
		// are minted at GET time.
		probed, err := w.resolveRedirects(ctx, assetURL)
		if err != nil {
			// HEAD is only a filename optimization. Some otherwise valid APK
			// endpoints reject it, while the authoritative GET succeeds.
			probed.FinalURL = assetURL
		}

		// Filename from redirect target (e.g. Telegram.apk); download still uses assetURL.
		// A CDN object named .bin is still an APK when the probe says so.
		nameURL = probed.FinalURL
		probedType = probed.ContentType
		probedLength = probed.ContentLength

		if !w.SkipHTTPCache {
			changed, validators, cacheErr := w.checkHTTPCacheHeaders(ctx, probed.FinalURL, w.loadCache())
			switch {
			case cacheErr != nil && (errors.Is(cacheErr, context.Canceled) || errors.Is(cacheErr, context.DeadlineExceeded)):
				return nil, cacheErr
			case cacheErr != nil:
				// Validators only save a download. Without them, treat the
				// source as changed and store nothing.
			case !changed:
				return nil, ErrNotModified
			default:
				w.pendingCache = validators
			}
		}

		// Version will be extracted from APK after download
		version = ""
	}

	// Create asset
	// An extracted URL is runtime-derived and may be short-lived. A static
	// source URL remains publishable even when it has no version placeholder.
	excludeURL := repo.HasAssetExtractor()

	// Extract filename from URL path (without query parameters).
	// nameURL may be the redirect target when the download URL itself is generic.
	if nameURL == "" {
		nameURL = assetURL
	}
	assetName := filepath.Base(nameURL)
	if parsed, err := url.Parse(nameURL); err == nil {
		assetName = filepath.Base(parsed.Path)
	}
	if IsAPKContentType(probedType) {
		assetName = apkFilename(assetName)
	}

	asset := &Asset{
		Name:        assetName,
		URL:         assetURL,
		Size:        probedLength,
		ContentType: probedType,
		ExcludeURL:  excludeURL,
	}

	return &Release{
		Version: version,
		Assets:  []*Asset{asset},
	}, nil
}

// apkFilename returns name with an .apk suffix. A CDN object such as
// "hash.bin" stays recognizable; only the suffix changes.
func apkFilename(name string) string {
	if strings.HasSuffix(strings.ToLower(name), ".apk") {
		return name
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if base == "" || base == "." || base == ".." {
		base = "app"
	}
	return base + ".apk"
}

// extractVersion extracts the version string using the configured extractor.
func (w *Web) extractVersion(ctx context.Context, repo *config.ReleaseSource) (string, error) {
	if repo.Version == nil {
		return "", fmt.Errorf("no version extractor configured")
	}

	return w.extractValue(ctx, repo.Version)
}

// extractAssetURL extracts the download URL using the configured asset extractor.
func (w *Web) extractAssetURL(ctx context.Context, repo *config.ReleaseSource) (string, error) {
	if repo.Asset == nil {
		return "", fmt.Errorf("no asset extractor configured")
	}

	assetURL, err := w.extractValue(ctx, repo.Asset)
	if err != nil {
		return "", err
	}

	// An extracted asset URL is not an explicit configuration value, but it
	// must satisfy the same HTTPS-outside-loopback rule so a compromised or
	// misconfigured page cannot redirect the download to an insecure target.
	if err := validateDownloadURL(assetURL); err != nil {
		return "", fmt.Errorf("extracted asset URL %s is not a valid download URL: %s", sanitize.URL(assetURL), sanitize.Text(err.Error()))
	}

	return assetURL, nil
}

// extractValue extracts a string value using a VersionExtractor configuration.
// Used by both version and asset extraction.
func (w *Web) extractValue(ctx context.Context, v *config.VersionExtractor) (string, error) {
	switch v.Mode() {
	case "html":
		return w.extractVersionHTML(ctx, v)
	case "json":
		return w.extractVersionJSON(ctx, v)
	case "header":
		return w.extractVersionHeader(ctx, v)
	default:
		return "", fmt.Errorf("invalid extractor mode")
	}
}

// extractVersionHTML extracts version from an HTML page using CSS selector.
func (w *Web) extractVersionHTML(ctx context.Context, v *config.VersionExtractor) (string, error) {
	req, err := newRequest(ctx, http.MethodGet, v.URL)
	if err != nil {
		return "", err
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch page: %w", err)
	}
	defer resp.Body.Close()

	// Validate HTTP status
	if err := checkHTTPStatus(resp, "Web page"); err != nil {
		return "", err
	}

	// Security: bound the response to prevent memory exhaustion
	page, err := readBounded(resp.Body, MaxRemoteDownloadSize)
	if err != nil {
		return "", fmt.Errorf("read page: %w", err)
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return "", fmt.Errorf("parse HTML: %w", err)
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
	req, err := newRequest(ctx, http.MethodGet, v.URL)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch API: %w", err)
	}
	defer resp.Body.Close()

	// Validate HTTP status
	if err := checkHTTPStatus(resp, "Web API"); err != nil {
		return "", err
	}

	// Parse a bounded response.
	var data interface{}
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &data); err != nil {
		return "", fmt.Errorf("parse JSON: %w", err)
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
	client := *w.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse // Stop at first redirect
	}

	req, err := newRequest(ctx, http.MethodGet, v.URL)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch page: %w", err)
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
		return "", fmt.Errorf("pattern %q did not match", pattern)
	}

	return matches[1], nil
}

func (w *Web) cacheFilePath() string {
	cacheKey := w.cfg.ReleaseSource.URL
	if cacheKey == "" {
		cacheKey = w.cfg.ReleaseSource.AssetURL
	}
	if cacheKey == "" && w.cfg.ReleaseSource.Asset != nil {
		cacheKey = w.cfg.ReleaseSource.Asset.URL
	}
	sum := sha256.Sum256([]byte(cacheKey))
	return filepath.Join(w.cacheDir, hex.EncodeToString(sum[:8])+".json")
}

func (w *Web) loadCache() *webCache {
	data, err := os.ReadFile(w.cacheFilePath())
	if err != nil {
		return nil
	}
	var cache webCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	if cache.ETag == "" && cache.LastModified == "" {
		return nil
	}
	return &cache
}

func (w *Web) saveCache(cache *webCache) error {
	if err := os.MkdirAll(w.cacheDir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return os.WriteFile(w.cacheFilePath(), data, 0o644)
}

// CommitCache persists HTTP cache headers from the last successful fetch.
func (w *Web) CommitCache() error {
	if w.pendingCache == nil {
		return nil
	}
	if err := w.saveCache(w.pendingCache); err != nil {
		return err
	}
	w.pendingCache = nil
	return nil
}

// ClearHTTPCache deletes persisted HTTP cache headers so the next fetch is unconditional.
func (w *Web) ClearHTTPCache() error {
	w.pendingCache = nil
	err := os.Remove(w.cacheFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// checkHTTPCacheHeaders reports whether rawURL changed since cache, using a
// conditional HEAD request, and returns the validators to store for the next
// run (nil when the server offers none). Only 200 and 304 are answers; any
// other status is an error. A source with no cached validator is changed, and
// matching requires ETag or Last-Modified, never Content-Length.
func (w *Web) checkHTTPCacheHeaders(ctx context.Context, rawURL string, cache *webCache) (bool, *webCache, error) {
	req, err := newRequest(ctx, http.MethodHead, rawURL)
	if err != nil {
		return true, nil, err
	}
	if cache != nil && cache.ETag != "" {
		req.Header.Set("If-None-Match", cache.ETag)
	}
	if cache != nil && cache.LastModified != "" {
		req.Header.Set("If-Modified-Since", cache.LastModified)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return true, nil, fmt.Errorf("HEAD request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && cache != nil {
		return false, cache, nil
	}
	if err := checkHTTPStatus(resp, "Web source"); err != nil {
		return true, nil, err
	}

	etag, lastModified := resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	var validators *webCache
	if etag != "" || lastModified != "" {
		validators = &webCache{ETag: etag, LastModified: lastModified}
	}
	if cache == nil {
		return true, validators, nil
	}
	switch {
	case cache.ETag != "" && etag != "":
		return cache.ETag != etag, validators, nil
	case cache.LastModified != "" && lastModified != "":
		return cache.LastModified != lastModified, validators, nil
	}
	return true, validators, nil
}

// Download downloads an APK from the web.
func (w *Web) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", fmt.Errorf("asset has no download URL")
	}

	destPath, err := prepareDownloadDest(destDir, asset.Name)
	if err != nil {
		return "", err
	}

	if err := downloadHTTP(ctx, w.downloadClient, asset.URL, destPath, asset.Size, asset.SHA256, nil, progress); err != nil {
		return "", err
	}

	asset.LocalPath = destPath
	return destPath, nil
}
