package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/config"
)

// Web implements Source for web scraping.
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
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Type returns the source type.
func (w *Web) Type() config.SourceType {
	return config.SourceWeb
}

// FetchLatestRelease fetches the latest release via web scraping.
func (w *Web) FetchLatestRelease(ctx context.Context) (*Release, error) {
	repo := w.cfg.ReleaseSource

	// Extract version using the configured method
	version, err := w.extractVersion(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to extract version: %w", err)
	}

	// Build asset URL by replacing $version placeholder
	assetURL := strings.ReplaceAll(repo.AssetURL, "$version", version)
	assetName := filepath.Base(assetURL)

	assets := []*Asset{
		{
			Name: assetName,
			URL:  assetURL,
		},
	}

	return &Release{
		Version: version,
		Assets:  assets,
	}, nil
}

// extractVersion extracts the version using the configured method.
func (w *Web) extractVersion(ctx context.Context, repo *config.ReleaseSource) (string, error) {
	if repo.HTML != nil {
		return w.extractVersionHTML(ctx, repo.URL, repo.HTML)
	}
	if repo.JSON != nil {
		return w.extractVersionJSON(ctx, repo.URL, repo.JSON)
	}
	if repo.Redirect != nil {
		return w.extractVersionRedirect(ctx, repo.URL, repo.Redirect)
	}

	return "", fmt.Errorf("no extraction method configured")
}

// extractVersionHTML extracts version from HTML using CSS selector.
func (w *Web) extractVersionHTML(ctx context.Context, url string, extractor *config.HTMLExtractor) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read page: %w", err)
	}

	// Simple HTML extraction without a full parser
	// This is a basic implementation - for complex selectors, consider using goquery
	content := string(body)

	// Try to find content matching the selector pattern
	// This is a simplified approach - real CSS selector parsing would need goquery
	var value string

	// Handle simple class selectors like ".version-badge"
	if strings.HasPrefix(extractor.Selector, ".") {
		className := extractor.Selector[1:]
		// Look for class="className" or class='className'
		pattern := fmt.Sprintf(`class=["']?[^"']*%s[^"']*["']?[^>]*>([^<]+)<`, regexp.QuoteMeta(className))
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(content)
		if len(matches) > 1 {
			value = strings.TrimSpace(matches[1])
		}
	}

	// Handle attribute extraction if specified
	if extractor.Attribute != "" && extractor.Attribute != "text" {
		// Look for the attribute value
		pattern := fmt.Sprintf(`%s=["']([^"']+)["']`, regexp.QuoteMeta(extractor.Attribute))
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(content)
		if len(matches) > 1 {
			value = strings.TrimSpace(matches[1])
		}
	}

	if value == "" {
		return "", fmt.Errorf("could not extract value using selector %q", extractor.Selector)
	}

	// Apply pattern if specified
	if extractor.Pattern != "" {
		re, err := regexp.Compile(extractor.Pattern)
		if err != nil {
			return "", fmt.Errorf("invalid pattern: %w", err)
		}
		matches := re.FindStringSubmatch(value)
		if len(matches) > 1 {
			value = matches[1]
		} else if len(matches) == 1 {
			value = matches[0]
		}
	}

	return value, nil
}

// extractVersionJSON extracts version from JSON using JSONPath.
func (w *Web) extractVersionJSON(ctx context.Context, url string, extractor *config.JSONExtractor) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Accept", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch JSON: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("JSON fetch failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read JSON: %w", err)
	}

	// Parse JSON
	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Simple JSONPath implementation
	// Supports basic paths like "$.latest.version" or "$.tag_name"
	value, err := extractJSONPath(data, extractor.Path)
	if err != nil {
		return "", err
	}

	// Apply pattern if specified
	if extractor.Pattern != "" {
		re, err := regexp.Compile(extractor.Pattern)
		if err != nil {
			return "", fmt.Errorf("invalid pattern: %w", err)
		}
		matches := re.FindStringSubmatch(value)
		if len(matches) > 1 {
			value = matches[1]
		}
	}

	return value, nil
}

// extractVersionRedirect extracts version from HTTP redirect header.
func (w *Web) extractVersionRedirect(ctx context.Context, url string, extractor *config.RedirectExtractor) (string, error) {
	// Create client that doesn't follow redirects
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch redirect: %w", err)
	}
	defer resp.Body.Close()

	// Get the header value
	headerValue := resp.Header.Get(extractor.Header)
	if headerValue == "" {
		return "", fmt.Errorf("header %q not found in response", extractor.Header)
	}

	// Apply pattern
	if extractor.Pattern == "" {
		return "", fmt.Errorf("pattern is required for redirect extraction")
	}

	re, err := regexp.Compile(extractor.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}

	matches := re.FindStringSubmatch(headerValue)
	if len(matches) < 2 {
		return "", fmt.Errorf("pattern did not match header value")
	}

	return matches[1], nil
}

// extractJSONPath extracts a value from JSON using a simple JSONPath expression.
func extractJSONPath(data interface{}, path string) (string, error) {
	// Remove leading "$." if present
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")

	parts := strings.Split(path, ".")
	current := data

	for _, part := range parts {
		if part == "" {
			continue
		}

		switch v := current.(type) {
		case map[string]interface{}:
			var ok bool
			current, ok = v[part]
			if !ok {
				return "", fmt.Errorf("key %q not found", part)
			}
		case []interface{}:
			// Handle array index (e.g., "[0]")
			if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
				// Parse index
				return "", fmt.Errorf("array indexing not yet supported")
			}
			// If it's an array and we're looking for a key, use first element
			if len(v) > 0 {
				if m, ok := v[0].(map[string]interface{}); ok {
					current, ok = m[part]
					if !ok {
						return "", fmt.Errorf("key %q not found in array element", part)
					}
				} else {
					return "", fmt.Errorf("expected object in array")
				}
			} else {
				return "", fmt.Errorf("empty array")
			}
		default:
			return "", fmt.Errorf("cannot navigate into %T", current)
		}
	}

	// Convert to string
	switch v := current.(type) {
	case string:
		return v, nil
	case float64:
		return fmt.Sprintf("%v", v), nil
	case int:
		return fmt.Sprintf("%d", v), nil
	case bool:
		return fmt.Sprintf("%v", v), nil
	default:
		return "", fmt.Errorf("cannot convert %T to string", current)
	}
}

// Download downloads an APK from the web.
func (w *Web) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", fmt.Errorf("asset has no download URL")
	}

	// Create destination directory if needed
	if destDir == "" {
		destDir = os.TempDir()
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Sanitize filename to prevent path traversal attacks
	safeName := filepath.Base(asset.Name)
	destPath := filepath.Join(destDir, safeName)

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
		return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
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

	asset.LocalPath = destPath
	return destPath, nil
}
