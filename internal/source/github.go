package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/config"
)

// githubCache stores validators for /releases/latest: the ETag for conditional
// requests and a fingerprint of the release fields zsp consumes. Volatile
// fields (asset download counts, reactions) move the ETag on their own.
type githubCache struct {
	ETag        string `json:"etag,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// GitHub implements Source for GitHub releases.
type GitHub struct {
	cfg                *config.Config
	owner              string
	repo               string
	token              string
	client             *http.Client
	cacheDir           string
	apiBase            string
	SkipHTTPCache      bool // Set to true to bypass ETag checks
	IncludePreReleases bool // Set to true to include pre-releases (--pre-release)
	maxReleases        int
	pendingCache       *githubCache
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
		cfg:      cfg,
		owner:    parts[0],
		repo:     parts[1],
		token:    config.GetEnv("GITHUB_TOKEN"),
		client:   newSecureHTTPClient(30 * time.Second),
		cacheDir: sourceCacheDir("github"),
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

func (g *GitHub) cacheFilePath() string {
	return filepath.Join(g.cacheDir, filepath.Base(g.owner)+"_"+filepath.Base(g.repo)+".json")
}

func (g *GitHub) loadCache() *githubCache {
	data, err := os.ReadFile(g.cacheFilePath())
	if err != nil {
		return nil
	}
	var cache githubCache
	if err := json.Unmarshal(data, &cache); err != nil || (cache.ETag == "" && cache.Fingerprint == "") {
		return nil
	}
	return &cache
}

func (g *GitHub) saveCache(cache *githubCache) error {
	if err := os.MkdirAll(g.cacheDir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return os.WriteFile(g.cacheFilePath(), data, 0o644)
}

// CommitCache persists the validators from the last successful fetch.
func (g *GitHub) CommitCache() error {
	if g.pendingCache == nil {
		return nil
	}
	if err := g.saveCache(g.pendingCache); err != nil {
		return err
	}
	g.pendingCache = nil
	return nil
}

// ClearHTTPCache deletes the persisted ETag so the next fetch is unconditional.
func (g *GitHub) ClearHTTPCache() error {
	g.pendingCache = nil
	err := os.Remove(g.cacheFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (g *GitHub) apiURL(format string, args ...any) string {
	base := g.apiBase
	if base == "" {
		base = "https://api.github.com"
	}
	return base + fmt.Sprintf(format, args...)
}

func (g *GitHub) setGitHubHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
}

// FetchLatestRelease fetches the latest release from GitHub that contains valid APKs.
// First tries /releases/latest (single request, fast path). If that release is a draft,
// a pre-release (when not opted in), or carries no valid APKs, falls back to scanning
// the most recent releases list to find one that qualifies.
// Uses conditional requests (ETag/If-None-Match) on the fast path. Returns
// ErrNotModified if the latest release has not changed since the last fetch;
// an ETag that moved only because of volatile fields (asset download counts,
// reactions) still counts as unchanged. Set SkipHTTPCache to true to bypass
// the check and always fetch fresh data.
//
// Note: /releases/latest always returns the latest stable release — GitHub excludes
// prereleases from that endpoint by design. When IncludePreReleases is set we skip
// straight to the list endpoint so that a prerelease newer than the latest stable
// release is not missed.
func (g *GitHub) FetchLatestRelease(ctx context.Context) (*Release, error) {
	if g.IncludePreReleases {
		return g.fetchLatestFromList(ctx)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", g.apiURL("/repos/%s/%s/releases/latest", g.owner, g.repo), nil)
	if err != nil {
		return nil, err
	}
	g.setGitHubHeaders(req)
	var cache *githubCache
	if !g.SkipHTTPCache {
		cache = g.loadCache()
		if cache != nil && cache.ETag != "" {
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
			fingerprint := releaseFingerprint(&ghRelease)
			if etag := resp.Header.Get("ETag"); etag != "" {
				g.pendingCache = &githubCache{ETag: etag, Fingerprint: fingerprint}
			}
			if cache != nil && cache.Fingerprint != "" && cache.Fingerprint == fingerprint {
				// The ETag moved but nothing zsp consumes did. Treat as not
				// modified; the refreshed validators are committed by Fetch.
				return nil, ErrNotModified
			}
			return release, nil
		}
	}

	// Fast path didn't yield a valid APK — fall back to scanning the release list.
	// ETag is intentionally not cached here: the cached ETag is bound to /releases/latest.
	return g.fetchLatestFromList(ctx)
}

// fetchLatestFromList scans the newest releases and returns the first one
// that is not a draft, passes the pre-release filter, and contains valid APKs.
// Used as a fallback when /releases/latest does not itself contain a valid APK
// (e.g. repos that publish separate desktop and mobile releases).
func (g *GitHub) fetchLatestFromList(ctx context.Context) (*Release, error) {
	limit := releaseScanLimit(g.maxReleases)
	req, err := http.NewRequestWithContext(ctx, "GET", g.apiURL("/repos/%s/%s/releases?per_page=%d", g.owner, g.repo, limit), nil)
	if err != nil {
		return nil, err
	}
	g.setGitHubHeaders(req)

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

	return nil, fmt.Errorf("no releases with valid APKs found in the last %d releases for %s/%s", limit, g.owner, g.repo)
}

// releaseFingerprint hashes the release fields zsp consumes, so an ETag that
// moved only because of volatile fields still detects as unchanged. An asset
// replaced in place with identical metadata is not detectable; SkipHTTPCache
// covers that case.
func releaseFingerprint(release *githubRelease) string {
	assets := slices.Clone(release.Assets)
	slices.SortFunc(assets, func(a, b githubAsset) int {
		if byName := strings.Compare(a.Name, b.Name); byName != 0 {
			return byName
		}
		return strings.Compare(a.BrowserDownloadURL, b.BrowserDownloadURL)
	})
	payload, _ := json.Marshal(struct {
		TagName     string
		Name        string
		Body        string
		Prerelease  bool
		Draft       bool
		PublishedAt string
		HTMLURL     string
		Assets      []githubAsset
	}{
		release.TagName, release.Name, release.Body, release.Prerelease,
		release.Draft, release.PublishedAt, release.HTMLURL, assets,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
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
