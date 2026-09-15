package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	cfg    *config.Config
	client *http.Client
}

// NewWeb creates a new web scraping source.
func NewWeb(cfg *config.Config) (*Web, error) {
	if cfg.ReleaseSource == nil || !cfg.ReleaseSource.IsWebSource {
		return nil, fmt.Errorf("invalid web source configuration")
	}

	return &Web{
		cfg:    cfg,
		client: newSecureHTTPClient(30 * time.Second),
	}, nil
}

// resolveRedirects follows redirects and returns the final URL. HEAD is used
// first to avoid downloading the asset, with a GET fallback for endpoints that
// deliberately reject HEAD.
func (w *Web) resolveRedirects(ctx context.Context, url string) (string, error) {
	// Create a client that tracks redirects but still follows them
	var finalURL string
	client := newSecureHTTPClient(30 * time.Second)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		finalURL = req.URL.String()
		return validateRedirect(req, via)
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == http.StatusMethodNotAllowed {
		resp.Body.Close()
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			req.Header.Set("Range", "bytes=0-0")
			resp, err = client.Do(req)
		}
	}
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

	if repo.HasAssetExtractor() {
		// Mode 2: Extract asset URL from page (version optionally extracted too)
		if repo.HasVersionExtractor() {
			var err error
			version, err = w.extractVersion(ctx, repo)
			if err != nil {
				return nil, fmt.Errorf("failed to extract version: %w", err)
			}
		}

		// Extract the download URL dynamically from the page
		var err error
		assetURL, err = w.extractAssetURL(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to extract asset URL: %w", err)
		}
	} else if repo.HasVersionExtractor() {
		// Mode 1: Extract version from page, construct asset URL
		var err error
		version, err = w.extractVersion(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to extract version: %w", err)
		}

		// Substitute {version} in asset_url
		assetURL = strings.ReplaceAll(repo.AssetURL, "{version}", version)
	} else {
		// Mode 2/3: Direct URL (versionless)
		assetURL = repo.AssetURL

		// Resolve redirects for filename purposes only. Keep downloading from the
		// original URL so tokenized CDN targets (e.g. telegram.org → telesco.pe?token=...)
		// are minted at GET time.
		finalURL, err := w.resolveRedirects(ctx, assetURL)
		if err != nil {
			// HEAD is only a filename optimization. Some otherwise valid APK
			// endpoints reject it, while the authoritative GET succeeds.
			finalURL = assetURL
		}

		// Filename from redirect target (e.g. Telegram.apk); download still uses assetURL
		nameURL = finalURL

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

	asset := &Asset{
		Name:       assetName,
		URL:        assetURL,
		ExcludeURL: excludeURL,
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
		return "", fmt.Errorf("extracted asset value %q is not a valid download URL: %w", assetURL, err)
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
	req, err := http.NewRequestWithContext(ctx, "GET", v.URL, nil)
	if err != nil {
		return "", err
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch page: %w", err)
	}
	defer resp.Body.Close()

	// Validate HTTP status
	if err := checkHTTPStatus(resp, "Web page"); err != nil {
		return "", err
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

	// Validate HTTP status
	if err := checkHTTPStatus(resp, "Web API"); err != nil {
		return "", err
	}

	// Parse a bounded response.
	var data interface{}
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &data); err != nil {
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
	client := newSecureHTTPClient(30 * time.Second)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse // Stop at first redirect
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

// Download downloads an APK from the web.
func (w *Web) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", fmt.Errorf("asset has no download URL")
	}

	destPath, err := prepareDownloadDest(destDir, asset.Name)
	if err != nil {
		return "", err
	}

	if err := DownloadHTTP(ctx, asset.URL, destPath, asset.Size, nil, progress); err != nil {
		return "", err
	}

	asset.LocalPath = destPath
	return destPath, nil
}
