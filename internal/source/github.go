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

// pendingCache stores cache data that hasn't been committed yet.
// It's only saved to disk after successful publishing via CommitCache().
type pendingCache struct {
	ETag    string
	Release *githubRelease
}

// GitHub implements Source for GitHub releases.
type GitHub struct {
	cfg       *config.Config
	owner     string
	repo      string
	token     string
	client    *http.Client
	cacheDir  string
	SkipCache          bool // Set to true to bypass ETag cache (--overwrite-release)
	IncludePreReleases bool // Set to true to include pre-releases (--pre-release)

	// pending holds cache data from the last fetch, not yet committed to disk.
	// Call CommitCache() after successful publishing to persist it.
	pending *pendingCache
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
		client:   newSecureHTTPClient(30 * time.Second),
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
	g.pending = nil // Clear pending cache
	cachePath := g.cacheFilePath()
	err := os.Remove(cachePath)
	if os.IsNotExist(err) {
		return nil // No cache to clear
	}
	return err
}

// CommitCache saves the pending cache to disk.
// This should be called after successful publishing to persist the ETag.
func (g *GitHub) CommitCache() error {
	if g.pending == nil {
		return nil // Nothing to commit
	}
	err := g.saveCache(g.pending.ETag, g.pending.Release)
	if err == nil {
		g.pending = nil // Clear pending after successful commit
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
	HTMLURL     string        `json:"html_url"`
	Assets      []githubAsset `json:"assets"`
}

// githubAsset represents a GitHub release asset.
type githubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	ContentType        string `json:"content_type"`
}

// FetchLatestRelease fetches the latest release from GitHub that contains valid APKs.
// First tries /releases/latest (single request, fast path). If that release is a draft,
// a pre-release (when not opted in), or carries no valid APKs, falls back to scanning
// the most recent releases list to find one that qualifies.
// Uses conditional requests (ETag/If-None-Match) on the fast path to reduce rate limit
// usage. Returns ErrNotModified if the latest release hasn't changed since the last check.
// Set SkipCache to true to bypass the ETag check and always fetch fresh data.
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
		return nil, fmt.Errorf("failed to fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, ErrNotModified
	case http.StatusNotFound:
		return nil, fmt.Errorf("no releases found for %s/%s", g.owner, g.repo)
	case http.StatusForbidden:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, fmt.Errorf("GitHub API rate limit exceeded. Set GITHUB_TOKEN environment variable to increase limits")
		}
		return nil, fmt.Errorf("GitHub API access forbidden")
	case http.StatusOK:
		// handled below
	default:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error (status %d): %s", resp.StatusCode, string(body))
	}

	var ghRelease githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&ghRelease); err != nil {
		return nil, fmt.Errorf("failed to parse latest release: %w", err)
	}

	// Use the fast-path result if it qualifies: not a draft, not an unwanted pre-release,
	// and actually contains a valid APK.
	if !ghRelease.Draft && !(ghRelease.Prerelease && !g.IncludePreReleases) {
		release := g.convertRelease(&ghRelease)
		if HasValidAPKs(release.Assets) {
			// Store ETag and release for later commit (after successful publish).
			if etag := resp.Header.Get("ETag"); etag != "" {
				g.pending = &pendingCache{ETag: etag, Release: &ghRelease}
			}
			return release, nil
		}
	}

	// Fast path didn't yield a valid APK — fall back to scanning the release list.
	return g.fetchLatestFromList(ctx)
}

// fetchLatestFromList scans up to maxReleasesToCheck releases and returns the first one
// that is not a draft, passes the pre-release filter, and contains valid APKs.
// Used as a fallback when /releases/latest does not itself contain a valid APK
// (e.g. repos that publish separate desktop and mobile releases).
// ETag is intentionally not cached here: the cached ETag is bound to /releases/latest,
// and mixing endpoints would cause the conditional-request optimisation to stop working.
func (g *GitHub) fetchLatestFromList(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=%d", g.owner, g.repo, maxReleasesToCheck)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no releases found for %s/%s", g.owner, g.repo)
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, fmt.Errorf("GitHub API rate limit exceeded. Set GITHUB_TOKEN environment variable to increase limits")
		}
		return nil, fmt.Errorf("GitHub API access forbidden")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error (status %d): %s", resp.StatusCode, string(body))
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to parse releases: %w", err)
	}

	if len(releases) == 0 {
		return nil, fmt.Errorf("no releases found for %s/%s", g.owner, g.repo)
	}

	for i := range releases {
		ghRelease := &releases[i]
		if ghRelease.Draft || (ghRelease.Prerelease && !g.IncludePreReleases) {
			continue
		}
		release := g.convertRelease(ghRelease)
		if HasValidAPKs(release.Assets) {
			return release, nil
		}
	}

	return nil, fmt.Errorf("no releases with valid APKs found in the last %d releases for %s/%s", maxReleasesToCheck, g.owner, g.repo)
}

// convertRelease converts a GitHub release to our Release type.
func (g *GitHub) convertRelease(ghRelease *githubRelease) *Release {
	assets := make([]*Asset, 0, len(ghRelease.Assets))
	for _, a := range ghRelease.Assets {
		assets = append(assets, &Asset{
			Name:        a.Name,
			URL:         a.BrowserDownloadURL,
			Size:        a.Size,
			ContentType: a.ContentType,
		})
	}

	// Filter out APKs with unsupported architectures (x86, x86_64, etc.)
	assets = FilterUnsupportedArchitectures(assets)

	// Extract version from tag name (strip leading 'v' if present)
	version := ghRelease.TagName
	if strings.HasPrefix(version, "v") {
		version = version[1:]
	}

	// Parse release date from published_at (RFC 3339 format)
	var createdAt time.Time
	if ghRelease.PublishedAt != "" {
		if t, err := time.Parse(time.RFC3339, ghRelease.PublishedAt); err == nil {
			createdAt = t
		}
	}

	return &Release{
		Version:    version,
		TagName:    ghRelease.TagName,
		Changelog:  ghRelease.Body,
		Assets:     assets,
		PreRelease: ghRelease.Prerelease,
		URL:        ghRelease.HTMLURL,
		CreatedAt:  createdAt,
	}
}

// Download downloads an asset from GitHub.
// Uses a download cache to avoid re-downloading the same file.
func (g *GitHub) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
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

	// Use download client (no total timeout — only stall detection)
	dlClient := newDownloadHTTPClient()

	req, err := http.NewRequestWithContext(ctx, "GET", asset.URL, nil)
	if err != nil {
		return "", err
	}

	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}

	resp, err := dlClient.Do(req)
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
	f, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	// Wrap body with stall timeout — fails only if no data received for 30s
	var reader io.Reader = &StallTimeoutReader{
		Reader:  resp.Body,
		Timeout: downloadStallTimeout,
	}

	// Wrap with progress tracking if callback provided
	if progress != nil && total > 0 {
		reader = &ProgressReader{
			Reader:     reader,
			Total:      total,
			OnProgress: progress,
		}
	}

	_, err = io.Copy(f, reader)
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	// Save to download cache (best-effort, ignore errors)
	if cachedPath, err := SaveToDownloadCache(asset.URL, asset.Name, destPath); err == nil {
		// Use cached path instead of temp path
		os.Remove(destPath)
		destPath = cachedPath
	}

	// Update asset with local path
	asset.LocalPath = destPath

	return destPath, nil
}
