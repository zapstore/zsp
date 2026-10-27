package source

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zapstore/zsp/v2/internal/config"
)

// Gitea implements Source for Gitea/Forgejo/Codeberg releases.
// This covers any Gitea-compatible forge (Gitea, Forgejo, Codeberg, etc.)
type Gitea struct {
	baseURL            string // e.g., "https://codeberg.org"
	owner              string
	repo               string
	client             *http.Client
	downloadClient     *http.Client
	filter             *releaseFilter
	IncludePreReleases bool // Set to true to include pre-releases (--pre-release)
	maxReleases        int
}

// NewGitea creates a new Gitea source.
func NewGitea(cfg *config.Config) (*Gitea, error) {
	return newGitea(cfg, Options{})
}

func newGitea(cfg *config.Config, opts Options) (*Gitea, error) {
	repoURL := cfg.GetAPKSourceURL()
	baseURL, repoPath := config.GetGiteaRepo(repoURL)
	if repoPath == "" {
		return nil, fmt.Errorf("invalid Gitea/Forgejo URL: %s", repoURL)
	}

	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid Gitea repo path: %s", repoPath)
	}

	if err := validateDownloadURL(baseURL); err != nil {
		return nil, fmt.Errorf("gitea base URL: %w", err)
	}
	filter, err := newReleaseFilter(cfg.ReleaseFilter)
	if err != nil {
		return nil, err
	}

	tor := opts.tor()
	return &Gitea{
		baseURL:        baseURL,
		owner:          parts[0],
		repo:           parts[1],
		client:         newSecureHTTPClient(30*time.Second, tor),
		downloadClient: newDownloadHTTPClient(tor),
		filter:         filter,
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
// Iterates through the newest releases to find one with APK assets (for repos that
// publish desktop and mobile releases separately). The scan size defaults to 10.
func (g *Gitea) FetchLatestRelease(ctx context.Context) (*Release, error) {
	// Fetch multiple releases to find one with valid APKs
	return g.fetchLatestFromList(ctx)
}

// fetchLatestFromList fetches releases and returns the first one with valid APKs.
func (g *Gitea) fetchLatestFromList(ctx context.Context) (*Release, error) {
	limit := releaseScanLimit(g.maxReleases)
	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/releases?limit=%d", g.baseURL, g.owner, g.repo, limit)

	req, err := newForgeRequest(ctx, config.SourceGitea, apiURL)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if err := checkHTTPStatus(resp, "Gitea API"); err != nil {
		return nil, describeNotFound(err, fmt.Sprintf("no releases found for %s/%s", g.owner, g.repo))
	}

	var releases []giteaRelease
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &releases); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}

	if len(releases) == 0 {
		return nil, fmt.Errorf("no releases found for %s/%s", g.owner, g.repo)
	}

	// Find the first non-draft release with valid APKs
	for _, r := range releases {
		// Skip drafts; skip prereleases unless explicitly included
		if r.Draft || (r.Prerelease && !g.IncludePreReleases) {
			continue
		}
		if !g.filter.matches(r.TagName, r.Name) {
			continue
		}
		release := g.convertRelease(&r)
		if HasValidAPKs(release.Assets) {
			return release, nil
		}
	}

	return nil, fmt.Errorf("no releases with valid APKs found in the last %d releases for %s/%s", limit, g.owner, g.repo)
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

	return &Release{
		Version:    versionFromTag(gtRelease.TagName),
		TagName:    gtRelease.TagName,
		Name:       gtRelease.Name,
		Changelog:  gtRelease.Body,
		Assets:     assets,
		PreRelease: gtRelease.Prerelease,
		URL:        gtRelease.HTMLURL,
		CreatedAt:  parseReleaseTime(gtRelease.PublishedAt, gtRelease.CreatedAt),
	}
}

// Download downloads an asset from a Gitea-compatible forge, using the
// shared DownloadHTTP pipeline (size limit, redirect validation, stall
// detection, bounded retries) with Gitea's token attached when the asset is hosted on GITEA_HOST.
func (g *Gitea) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", fmt.Errorf("asset has no download URL")
	}

	destPath, err := prepareDownloadDest(destDir, asset.Name)
	if err != nil {
		return "", err
	}

	headers := forgeAuthHeaders(config.SourceGitea, asset.URL)
	if err := downloadHTTP(ctx, g.downloadClient, asset.URL, destPath, asset.Size, asset.SHA256, headers, progress); err != nil {
		return "", err
	}

	asset.LocalPath = destPath
	return destPath, nil
}
