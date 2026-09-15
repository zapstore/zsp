package source

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/config"
)

// GitHub implements Source for GitHub releases.
type GitHub struct {
	cfg                *config.Config
	owner              string
	repo               string
	token              string
	client             *http.Client
	IncludePreReleases bool // Set to true to include pre-releases (--pre-release)
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

	return &GitHub{
		cfg:    cfg,
		owner:  parts[0],
		repo:   parts[1],
		token:  config.GetEnv("GITHUB_TOKEN"),
		client: newSecureHTTPClient(30 * time.Second),
	}, nil
}

// Type returns the source type.
func (g *GitHub) Type() config.SourceType {
	return config.SourceGitHub
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
//
// Note: /releases/latest always returns the latest stable release — GitHub excludes
// prereleases from that endpoint by design. When IncludePreReleases is set we skip
// straight to the list endpoint so that a prerelease newer than the latest stable
// release is not missed.
func (g *GitHub) FetchLatestRelease(ctx context.Context) (*Release, error) {
	if g.IncludePreReleases {
		return g.fetchLatestFromList(ctx)
	}

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

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
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
		return nil, fmt.Errorf("GitHub API error (status %d)", resp.StatusCode)
	}

	var ghRelease githubRelease
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &ghRelease); err != nil {
		return nil, fmt.Errorf("failed to parse latest release: %w", err)
	}

	// Use the fast-path result if it qualifies: not a draft, not an unwanted pre-release,
	// matches release filter, and actually contains a valid APK.
	if !ghRelease.Draft && !(ghRelease.Prerelease && !g.IncludePreReleases) && g.matchesReleaseFilter(ghRelease.TagName, ghRelease.Name) {
		release := g.convertRelease(&ghRelease)
		if HasValidAPKs(release.Assets) {
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
		return nil, fmt.Errorf("GitHub API error (status %d)", resp.StatusCode)
	}

	var releases []githubRelease
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &releases); err != nil {
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
		if !g.matchesReleaseFilter(ghRelease.TagName, ghRelease.Name) {
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
		Name:       ghRelease.Name,
		Changelog:  ghRelease.Body,
		Assets:     assets,
		PreRelease: ghRelease.Prerelease,
		URL:        ghRelease.HTMLURL,
		CreatedAt:  createdAt,
	}
}

// Download downloads an asset from GitHub, using the shared DownloadHTTP
// pipeline (size limit, redirect validation, stall detection, bounded
// retries) with GitHub's bearer token attached when configured.
func (g *GitHub) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", fmt.Errorf("asset has no download URL")
	}

	destPath, err := prepareDownloadDest(destDir, asset.Name)
	if err != nil {
		return "", err
	}

	var headers map[string]string
	if g.token != "" {
		headers = map[string]string{"Authorization": "Bearer " + g.token}
	}

	if err := DownloadHTTP(ctx, asset.URL, destPath, asset.Size, headers, progress); err != nil {
		return "", err
	}

	asset.LocalPath = destPath
	return destPath, nil
}

// matchesReleaseFilter checks whether a release tag or name matches the filter.
func (g *GitHub) matchesReleaseFilter(tagName string, names ...string) bool {
	if g.cfg.ReleaseFilter == "" {
		return true
	}
	re, err := regexp.Compile(g.cfg.ReleaseFilter)
	if err != nil {
		return false
	}
	if re.MatchString(tagName) {
		return true
	}
	for _, name := range names {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}
