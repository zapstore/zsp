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

// Gitea implements Source for Gitea/Forgejo/Codeberg releases.
// This covers any Gitea-compatible forge (Gitea, Forgejo, Codeberg, etc.)
type Gitea struct {
	cfg      *config.Config
	baseURL  string // e.g., "https://codeberg.org"
	owner    string
	repo     string
	token    string
	client   *http.Client
}

// NewGitea creates a new Gitea source.
func NewGitea(cfg *config.Config) (*Gitea, error) {
	repoURL := cfg.GetAPKSourceURL()
	baseURL, repoPath := config.GetGiteaRepo(repoURL)
	if repoPath == "" {
		return nil, fmt.Errorf("invalid Gitea/Forgejo URL: %s", repoURL)
	}

	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid Gitea repo path: %s", repoPath)
	}

	return &Gitea{
		cfg:     cfg,
		baseURL: baseURL,
		owner:   parts[0],
		repo:    parts[1],
		token:   os.Getenv("GITEA_TOKEN"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Type returns the source type.
func (g *Gitea) Type() config.SourceType {
	return config.SourceGitea
}

// giteaRelease represents a Gitea/Forgejo release API response.
// The API is compatible across Gitea, Forgejo, and Codeberg.
type giteaRelease struct {
	ID          int64        `json:"id"`
	TagName     string       `json:"tag_name"`
	Name        string       `json:"name"`
	Body        string       `json:"body"`
	Draft       bool         `json:"draft"`
	Prerelease  bool         `json:"prerelease"`
	CreatedAt   string       `json:"created_at"`
	PublishedAt string       `json:"published_at"`
	HTMLURL     string       `json:"html_url"`
	Assets      []giteaAsset `json:"assets"`
}

// giteaAsset represents a Gitea/Forgejo release asset.
type giteaAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	DownloadCount      int64  `json:"download_count"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// FetchLatestRelease fetches the latest release from a Gitea-compatible forge that contains valid APKs.
// Iterates through up to 10 releases to find one with APK assets (for repos that
// publish desktop and mobile releases separately).
func (g *Gitea) FetchLatestRelease(ctx context.Context) (*Release, error) {
	// Fetch multiple releases to find one with valid APKs
	return g.fetchLatestFromList(ctx)
}

// fetchLatestFromList fetches releases and returns the first one with valid APKs.
func (g *Gitea) fetchLatestFromList(ctx context.Context) (*Release, error) {
	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/releases?limit=%d", g.baseURL, g.owner, g.repo, maxReleasesToCheck)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	if g.token != "" {
		req.Header.Set("Authorization", "token "+g.token)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no releases found for %s/%s", g.owner, g.repo)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Gitea API error (status %d): %s", resp.StatusCode, string(body))
	}

	var releases []giteaRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to parse releases: %w", err)
	}

	if len(releases) == 0 {
		return nil, fmt.Errorf("no releases found for %s/%s", g.owner, g.repo)
	}

	// Find the first non-draft release with valid APKs
	for _, r := range releases {
		if r.Draft {
			continue
		}
		release := g.convertRelease(&r)
		if hasValidAPKs(release.Assets) {
			return release, nil
		}
	}

	return nil, fmt.Errorf("no releases with valid APKs found in the last %d releases for %s/%s", maxReleasesToCheck, g.owner, g.repo)
}

// convertRelease converts a Gitea release to our Release type.
func (g *Gitea) convertRelease(gtRelease *giteaRelease) *Release {
	assets := make([]*Asset, 0, len(gtRelease.Assets))
	for _, a := range gtRelease.Assets {
		assets = append(assets, &Asset{
			Name: a.Name,
			URL:  a.BrowserDownloadURL,
			Size: a.Size,
		})
	}

	// Filter out APKs with unsupported architectures (x86, x86_64, etc.)
	assets = FilterUnsupportedArchitectures(assets)

	// Extract version from tag name (strip leading 'v' if present)
	version := gtRelease.TagName
	if strings.HasPrefix(version, "v") {
		version = version[1:]
	}

	return &Release{
		Version:    version,
		TagName:    gtRelease.TagName,
		Changelog:  gtRelease.Body,
		Assets:     assets,
		PreRelease: gtRelease.Prerelease,
		URL:        gtRelease.HTMLURL,
	}
}

// Download downloads an asset from a Gitea-compatible forge.
// Uses a download cache to avoid re-downloading the same file.
func (g *Gitea) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
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

	if g.token != "" {
		req.Header.Set("Authorization", "token "+g.token)
	}

	resp, err := g.client.Do(req)
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

	// Save to download cache (best-effort, ignore errors)
	if cachedPath, err := SaveToDownloadCache(asset.URL, asset.Name, destPath); err == nil {
		os.Remove(destPath)
		destPath = cachedPath
	}

	// Update asset with local path
	asset.LocalPath = destPath

	return destPath, nil
}

