package source

import (
	"context"
	"crypto/sha256"
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

	"github.com/zapstore/zsp/internal/config"
)

// Web implements Source for web scraping.
type Web struct {
	cfg       *config.Config
	client    *http.Client
	cacheDir  string
	SkipCache bool // Set to true to bypass URL cache

	// pendingURLs holds URLs from the last fetch, not yet committed to disk.
	// Call CommitCache() after successful publishing to persist it.
	pendingURLs []string
}

// webCache stores the last downloaded asset URLs for a web source.
type webCache struct {
	AssetURLs []string `json:"asset_urls"`
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
		client:   &http.Client{Timeout: 30 * time.Second},
		cacheDir: cacheDir,
	}, nil
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

// loadCache reads the cached URL data from disk.
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

// saveCache writes the asset URLs to disk.
func (w *Web) saveCache(assetURLs []string) error {
	if err := os.MkdirAll(w.cacheDir, 0755); err != nil {
		return err
	}
	cache := webCache{AssetURLs: assetURLs}
	data, err := json.Marshal(&cache)
	if err != nil {
		return err
	}
	return os.WriteFile(w.cacheFilePath(), data, 0644)
}

// ClearCache removes the cached URL data.
func (w *Web) ClearCache() error {
	w.pendingURLs = nil // Clear pending cache
	cachePath := w.cacheFilePath()
	err := os.Remove(cachePath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// CommitCache saves the pending cache to disk.
// This should be called after successful publishing to persist the URLs.
func (w *Web) CommitCache() error {
	if len(w.pendingURLs) == 0 {
		return nil // Nothing to commit
	}
	err := w.saveCache(w.pendingURLs)
	if err == nil {
		w.pendingURLs = nil // Clear pending after successful commit
	}
	return err
}

// FetchLatestRelease fetches the latest release via web scraping.
// It finds URLs matching the asset_url pattern and returns them as assets.
// Version is left empty - it will be extracted from the APK after download.
//
// If URL is empty, asset_url is used directly as the download URL (no page scraping).
// If URL is set, the page is fetched and asset_url is used as a regex pattern to find APK URLs.
func (w *Web) FetchLatestRelease(ctx context.Context) (*Release, error) {
	repo := w.cfg.ReleaseSource

	var matchedURLs []string

	if repo.URL == "" {
		// Direct mode: use asset_url as the download URL directly (no scraping)
		matchedURLs = []string{repo.AssetURL}
	} else {
		// Scraping mode: fetch the page and find URLs matching the pattern
		pattern, err := regexp.Compile(repo.AssetURL)
		if err != nil {
			return nil, fmt.Errorf("invalid asset_url pattern: %w", err)
		}

		matchedURLs, err = w.findMatchingURLs(ctx, repo.URL, pattern)
		if err != nil {
			return nil, err
		}

		if len(matchedURLs) == 0 {
			return nil, fmt.Errorf("no URLs matching pattern %q found", repo.AssetURL)
		}
	}

	// Check cache - if all matched URLs were already processed, skip
	if !w.SkipCache {
		cache := w.loadCache()
		if cache != nil && urlsEqual(matchedURLs, cache.AssetURLs) {
			return nil, ErrNotModified
		}
	}

	// Store URLs for later commit (after successful publish).
	// Don't save to disk yet - call CommitCache() after successful publishing.
	w.pendingURLs = matchedURLs

	// Convert to assets
	assets := make([]*Asset, 0, len(matchedURLs))
	for _, u := range matchedURLs {
		assets = append(assets, &Asset{
			Name: filepath.Base(u),
			URL:  u,
		})
	}

	// Filter out APKs with unsupported architectures (x86, x86_64, etc.)
	assets = FilterUnsupportedArchitectures(assets)

	return &Release{
		Version: "", // Will be filled from APK after download
		Assets:  assets,
	}, nil
}

// findMatchingURLs searches for URLs matching the pattern in headers and body.
func (w *Web) findMatchingURLs(ctx context.Context, pageURL string, pattern *regexp.Regexp) ([]string, error) {
	// Track redirects
	var redirectURLs []string
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			redirectURLs = append(redirectURLs, req.URL.String())
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("page fetch failed with status %d", resp.StatusCode)
	}

	var matches []string
	seen := make(map[string]bool)

	addMatch := func(u string) {
		resolved := resolveAssetURL(pageURL, u)
		if !seen[resolved] {
			seen[resolved] = true
			matches = append(matches, resolved)
		}
	}

	// 1. Check redirect chain
	for _, u := range redirectURLs {
		if pattern.MatchString(u) {
			addMatch(u)
		}
	}

	// 2. Check final URL
	if pattern.MatchString(resp.Request.URL.String()) {
		addMatch(resp.Request.URL.String())
	}

	// 3. Check Location header (for non-followed redirects)
	if loc := resp.Header.Get("Location"); loc != "" && pattern.MatchString(loc) {
		addMatch(loc)
	}

	// 4. Search body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read page: %w", err)
	}

	bodyMatches := pattern.FindAllString(string(body), -1)
	for _, m := range bodyMatches {
		addMatch(m)
	}

	return matches, nil
}

// resolveAssetURL resolves a potentially relative URL against a base URL.
func resolveAssetURL(baseURL, matchedURL string) string {
	// If already absolute, use as-is
	if strings.HasPrefix(matchedURL, "http://") || strings.HasPrefix(matchedURL, "https://") {
		return matchedURL
	}

	// Resolve relative to base URL
	base, err := url.Parse(baseURL)
	if err != nil {
		return matchedURL
	}
	ref, err := url.Parse(matchedURL)
	if err != nil {
		return matchedURL
	}
	return base.ResolveReference(ref).String()
}

// urlsEqual checks if two URL slices contain the same URLs (order-independent).
func urlsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aSet := make(map[string]bool)
	for _, u := range a {
		aSet[u] = true
	}
	for _, u := range b {
		if !aSet[u] {
			return false
		}
	}
	return true
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

	// Save to download cache (best-effort, ignore errors)
	if cachedPath, err := SaveToDownloadCache(asset.URL, asset.Name, destPath); err == nil {
		os.Remove(destPath)
		destPath = cachedPath
	}

	asset.LocalPath = destPath
	return destPath, nil
}
