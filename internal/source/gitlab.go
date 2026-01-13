package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/config"
)

// GitLab implements Source for GitLab releases.
// Supports both gitlab.com and self-hosted GitLab instances.
type GitLab struct {
	cfg       *config.Config
	baseURL   string // e.g., "https://gitlab.com" or self-hosted URL
	projectID string // URL-encoded project path (e.g., "user%2Frepo")
	client    *http.Client
}

// NewGitLab creates a new GitLab source.
func NewGitLab(cfg *config.Config) (*GitLab, error) {
	repoURL := cfg.GetAPKSourceURL()

	// Use the new helper that extracts both base URL and repo path
	baseURL, repoPath := config.GetGitLabRepoWithBase(repoURL)
	if repoPath == "" {
		// Fallback to old method for gitlab.com URLs
		repoPath = config.GetGitLabRepo(repoURL)
		if repoPath == "" {
			return nil, fmt.Errorf("invalid GitLab URL: %s", repoURL)
		}
		baseURL = "https://gitlab.com"
	}

	// URL-encode the project path for API calls
	projectID := url.PathEscape(repoPath)

	return &GitLab{
		cfg:       cfg,
		baseURL:   baseURL,
		projectID: projectID,
		client:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Type returns the source type.
func (g *GitLab) Type() config.SourceType {
	return config.SourceGitLab
}

// gitlabRelease represents a GitLab release API response.
type gitlabRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleasedAt  string `json:"released_at"`
	Assets      struct {
		Links []gitlabAssetLink `json:"links"`
	} `json:"assets"`
}

// gitlabAssetLink represents a GitLab release asset link.
type gitlabAssetLink struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	LinkType string `json:"link_type"` // "other", "runbook", "image", "package"
}

// FetchLatestRelease fetches the latest release from GitLab.
func (g *GitLab) FetchLatestRelease(ctx context.Context) (*Release, error) {
	// GitLab API: GET /projects/:id/releases
	// Returns releases sorted by released_at descending
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/releases", g.baseURL, g.projectID)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no releases found or project not accessible")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitLab API error (status %d): %s", resp.StatusCode, string(body))
	}

	var releases []gitlabRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to parse releases: %w", err)
	}

	if len(releases) == 0 {
		return nil, fmt.Errorf("no releases found")
	}

	// Take the first (latest) release
	glRelease := releases[0]

	// Convert asset links to our Asset type
	assets := make([]*Asset, 0, len(glRelease.Assets.Links))
	for _, link := range glRelease.Assets.Links {
		assets = append(assets, &Asset{
			Name: link.Name,
			URL:  link.URL,
		})
	}

	// Extract version from tag name
	version := glRelease.TagName
	if strings.HasPrefix(version, "v") {
		version = version[1:]
	}

	return &Release{
		Version:   version,
		TagName:   glRelease.TagName,
		Changelog: glRelease.Description,
		Assets:    assets,
	}, nil
}

// Download downloads an asset from GitLab.
func (g *GitLab) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
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

	destPath := filepath.Join(destDir, asset.Name)

	// Download the file
	req, err := http.NewRequestWithContext(ctx, "GET", asset.URL, nil)
	if err != nil {
		return "", err
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

	asset.LocalPath = destPath
	return destPath, nil
}
