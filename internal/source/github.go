package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/config"
)

// ErrNotModified is returned when the release hasn't changed since the last check.
var ErrNotModified = fmt.Errorf("release not modified")

// releaseCache stores ETag and release data for conditional requests.
type releaseCache struct {
	ETag    string         `json:"etag"`
	Release *githubRelease `json:"release"`
}

// GitHub implements Source for GitHub releases.
type GitHub struct {
	cfg       *config.Config
	owner     string
	repo      string
	token     string
	client    *http.Client
	cacheDir  string
	SkipCache bool // Set to true to bypass ETag cache (--overwrite-release)
}

// NewGitHub creates a new GitHub source.
func NewGitHub(cfg *config.Config) (*GitHub, error) {
	url := cfg.GetAPKSourceURL()
	repoPath := config.GetGitHubRepo(url)
	if repoPath == "" {
		return nil, fmt.Errorf("invalid GitHub URL: %s", url)
	}

	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid GitHub repo path: %s", repoPath)
	}

	// Set up cache directory for ETags
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	cacheDir = filepath.Join(cacheDir, "zsp", "github")

	return &GitHub{
		cfg:      cfg,
		owner:    parts[0],
		repo:     parts[1],
		token:    os.Getenv("GITHUB_TOKEN"),
		client:   &http.Client{Timeout: 30 * time.Second},
		cacheDir: cacheDir,
	}, nil
}

// Type returns the source type.
func (g *GitHub) Type() config.SourceType {
	return config.SourceGitHub
}

// cacheFilePath returns the file path for storing cached release data.
func (g *GitHub) cacheFilePath() string {
	// Use owner_repo as filename to avoid path issues
	return filepath.Join(g.cacheDir, fmt.Sprintf("%s_%s.json", g.owner, g.repo))
}

// loadCache reads the cached release data from disk.
func (g *GitHub) loadCache() *releaseCache {
	data, err := os.ReadFile(g.cacheFilePath())
	if err != nil {
		return nil
	}
	var cache releaseCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	return &cache
}

// saveCache writes the release data and ETag to disk.
func (g *GitHub) saveCache(etag string, release *githubRelease) error {
	if err := os.MkdirAll(g.cacheDir, 0755); err != nil {
		return err
	}
	cache := releaseCache{
		ETag:    etag,
		Release: release,
	}
	data, err := json.Marshal(&cache)
	if err != nil {
		return err
	}
	return os.WriteFile(g.cacheFilePath(), data, 0644)
}

// GetCachedRelease returns the cached release if available.
func (g *GitHub) GetCachedRelease() *Release {
	cache := g.loadCache()
	if cache == nil || cache.Release == nil {
		return nil
	}
	return g.convertRelease(cache.Release)
}

// ClearCache removes the cached release data.
// This should be called when publishing fails so the next run can retry.
func (g *GitHub) ClearCache() error {
	cachePath := g.cacheFilePath()
	err := os.Remove(cachePath)
	if os.IsNotExist(err) {
		return nil // No cache to clear
	}
	return err
}

// githubRelease represents a GitHub release API response.
type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	Body        string        `json:"body"`
	Prerelease  bool          `json:"prerelease"`
	Draft       bool          `json:"draft"`
	PublishedAt string        `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

// githubAsset represents a GitHub release asset.
type githubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	ContentType        string `json:"content_type"`
}

// FetchLatestRelease fetches the latest release from GitHub.
// Uses conditional requests (ETag/If-None-Match) to reduce rate limit usage.
// Returns ErrNotModified if the release hasn't changed since the last check.
// Set SkipCache field to true to bypass the ETag check and always fetch fresh data.
func (g *GitHub) FetchLatestRelease(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", g.owner, g.repo)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}

	// Add If-None-Match header if we have a cached ETag (unless skipping cache)
	if !g.SkipCache {
		if cache := g.loadCache(); cache != nil && cache.ETag != "" {
			req.Header.Set("If-None-Match", cache.ETag)
		}
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch release: %w", err)
	}
	defer resp.Body.Close()

	// Handle 304 Not Modified - no new release since last check
	if resp.StatusCode == http.StatusNotModified {
		return nil, ErrNotModified
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no releases found for %s/%s", g.owner, g.repo)
	}

	if resp.StatusCode == http.StatusForbidden {
		// Check for rate limiting
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, fmt.Errorf("GitHub API rate limit exceeded. Set GITHUB_TOKEN environment variable to increase limits")
		}
		return nil, fmt.Errorf("GitHub API access forbidden")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error (status %d): %s", resp.StatusCode, string(body))
	}

	var ghRelease githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&ghRelease); err != nil {
		return nil, fmt.Errorf("failed to parse release: %w", err)
	}

	// Save ETag and release data for future conditional requests
	if etag := resp.Header.Get("ETag"); etag != "" {
		_ = g.saveCache(etag, &ghRelease) // Ignore error, caching is best-effort
	}

	return g.convertRelease(&ghRelease), nil
}

// convertRelease converts a GitHub release to our Release type.
func (g *GitHub) convertRelease(ghRelease *githubRelease) *Release {
	assets := make([]*Asset, len(ghRelease.Assets))
	for i, a := range ghRelease.Assets {
		assets[i] = &Asset{
			Name:        a.Name,
			URL:         a.BrowserDownloadURL,
			Size:        a.Size,
			ContentType: a.ContentType,
		}
	}

	// Extract version from tag name (strip leading 'v' if present)
	version := ghRelease.TagName
	if strings.HasPrefix(version, "v") {
		version = version[1:]
	}

	return &Release{
		Version:    version,
		TagName:    ghRelease.TagName,
		Changelog:  ghRelease.Body,
		Assets:     assets,
		PreRelease: ghRelease.Prerelease,
	}
}

// Download downloads an asset from GitHub.
func (g *GitHub) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
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

	// Download the file with progress tracking
	// Note: GitHub requires auth header, so we can't use the shared helper directly
	req, err := http.NewRequestWithContext(ctx, "GET", asset.URL, nil)
	if err != nil {
		return "", err
	}

	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}

	resp, err := g.client.Do(req)
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
	f, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	// Wrap reader with progress tracking if callback provided
	var reader io.Reader = resp.Body
	if progress != nil && total > 0 {
		reader = &ProgressReader{
			Reader:     resp.Body,
			Total:      total,
			OnProgress: progress,
		}
	}

	_, err = io.Copy(f, reader)
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	// Update asset with local path
	asset.LocalPath = destPath

	return destPath, nil
}
