package source

import (
	"bytes"
	"context"
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

// gitlabArchRegex extracts architecture from GitLab asset names like "APK (arm64-v8a)"
var gitlabArchRegex = regexp.MustCompile(`\((arm64-v8a|armeabi-v7a|arm|x86_64|x86)\)`)

// gitlabExternalRedirectMarker is present in GitLab's HTML interstitial for
// externally hosted release assets. GitLab returns HTTP 200 with this page
// instead of a Location header, so http.Client cannot follow it automatically.
const gitlabExternalRedirectMarker = "You are being redirected away from GitLab"

// gitlabExternalRedirectHref matches the external target link on that page.
var gitlabExternalRedirectHref = regexp.MustCompile(`(?i)href=["'](https?://[^"']+)["']`)

// gitlabCache stores the last successfully published release version.
type gitlabCache struct {
	LatestPublishedReleaseVersion string `json:"latest_published_release_version,omitempty"`
}

// GitLab implements Source for GitLab releases.
// Supports both gitlab.com and self-hosted GitLab instances.
type GitLab struct {
	cfg               *config.Config
	baseURL           string // e.g., "https://gitlab.com" or self-hosted URL
	projectID         string // URL-encoded project path (e.g., "user%2Frepo")
	client            *http.Client
	cacheDir          string
	pendingVersion    string
	SkipDownloadCache bool // Set to true to skip saving APKs to download cache
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

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	cacheDir = filepath.Join(cacheDir, "zsp", "gitlab")

	return &GitLab{
		cfg:       cfg,
		baseURL:   baseURL,
		projectID: projectID,
		client:    newSecureHTTPClient(30 * time.Second),
		cacheDir:  cacheDir,
	}, nil
}

func (g *GitLab) cacheFilePath() string {
	name, _ := url.PathUnescape(g.projectID)
	name = strings.ReplaceAll(name, "/", "_")
	return filepath.Join(g.cacheDir, name+".json")
}

func (g *GitLab) loadCache() *gitlabCache {
	data, err := os.ReadFile(g.cacheFilePath())
	if err != nil {
		return nil
	}
	var cache gitlabCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	return &cache
}

func (g *GitLab) saveCache(cache *gitlabCache) error {
	if err := os.MkdirAll(g.cacheDir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return os.WriteFile(g.cacheFilePath(), data, 0644)
}

// CommitCache implements CacheCommitter.
func (g *GitLab) CommitCache() error {
	if g.pendingVersion == "" {
		return nil
	}
	err := g.saveCache(&gitlabCache{LatestPublishedReleaseVersion: g.pendingVersion})
	if err == nil {
		g.pendingVersion = ""
	}
	return err
}

// GetPublishedVersion returns the last successfully published release version.
func (g *GitLab) GetPublishedVersion() string {
	if cache := g.loadCache(); cache != nil {
		return cache.LatestPublishedReleaseVersion
	}
	return ""
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
	Links       struct {
		Self string `json:"self"` // Release page URL
	} `json:"_links"`
	Assets struct {
		Links []gitlabAssetLink `json:"links"`
	} `json:"assets"`
}

// gitlabAssetLink represents a GitLab release asset link.
type gitlabAssetLink struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	DirectAssetURL string `json:"direct_asset_url"` // Contains the actual filename
	LinkType       string `json:"link_type"`        // "other", "runbook", "image", "package"
}

// FetchLatestRelease fetches the latest release from GitLab that contains valid APKs.
// Iterates through up to 10 releases to find one with APK assets (for repos that
// publish desktop and mobile releases separately).
func (g *GitLab) FetchLatestRelease(ctx context.Context) (*Release, error) {
	// GitLab API: GET /projects/:id/releases
	// Returns releases sorted by released_at descending
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/releases?per_page=%d", g.baseURL, g.projectID, maxReleasesToCheck)

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

	// Find the first release with valid APKs
	for _, glRelease := range releases {
		if !g.matchesReleaseFilter(glRelease.TagName) {
			continue
		}
		release := g.convertRelease(&glRelease)
		if HasValidAPKs(release.Assets) {
			g.pendingVersion = release.Version
			return release, nil
		}
	}

	return nil, fmt.Errorf("no releases with valid APKs found in the last %d releases", maxReleasesToCheck)
}

// pickGitLabAssetURL chooses the download URL for a release asset link.
//
// For externally hosted assets (CDN/S3/etc), link.URL is the real file while
// direct_asset_url is a GitLab interstitial HTML page. Prefer link.URL.
// Fall back to direct_asset_url for GitLab-hosted uploads where URL may be empty.
func pickGitLabAssetURL(link gitlabAssetLink) string {
	if link.URL != "" {
		return link.URL
	}
	return link.DirectAssetURL
}

// convertRelease converts a GitLab release to our Release type.
func (g *GitLab) convertRelease(glRelease *gitlabRelease) *Release {
	// Convert asset links to our Asset type
	assets := make([]*Asset, 0, len(glRelease.Assets.Links))
	for _, link := range glRelease.Assets.Links {
		downloadURL := pickGitLabAssetURL(link)

		// Prefer the URL that carries a real filename when extracting asset names.
		nameURL := link.DirectAssetURL
		if nameURL == "" || !strings.Contains(strings.ToLower(nameURL), ".apk") {
			nameURL = downloadURL
		}

		// Extract the actual filename from the URL (strip query parameters)
		assetName := link.Name
		if nameURL != "" {
			// Parse the URL to properly extract the path without query params
			urlPath := nameURL
			if parsedURL, err := url.Parse(nameURL); err == nil {
				urlPath = parsedURL.Path
			}

			if idx := strings.LastIndex(urlPath, "/"); idx >= 0 {
				filename := urlPath[idx+1:]
				if filename != "" && strings.HasSuffix(filename, ".apk") {
					// Extract architecture from link.Name (e.g., "APK (arm64-v8a)")
					// and append to filename if not already present
					if match := gitlabArchRegex.FindStringSubmatch(link.Name); len(match) > 1 {
						arch := match[1]
						// Check if filename already contains this architecture
						if !strings.Contains(filename, arch) {
							// Insert architecture before .apk extension
							assetName = strings.TrimSuffix(filename, ".apk") + "-" + arch + ".apk"
						} else {
							assetName = filename
						}
					} else {
						assetName = filename
					}
				} else if filename != "" && (strings.HasSuffix(filename, ".zip") || strings.HasSuffix(filename, ".tar.gz")) {
					assetName = filename
				}
			}
		}

		assets = append(assets, &Asset{
			Name: assetName,
			URL:  downloadURL,
		})
	}

	// Filter out APKs with unsupported architectures (x86, x86_64, etc.)
	assets = FilterUnsupportedArchitectures(assets)

	// Extract version from tag name
	version := strings.TrimPrefix(glRelease.TagName, "v")

	// Parse release date from released_at (RFC 3339 format)
	var createdAt time.Time
	if glRelease.ReleasedAt != "" {
		if t, err := time.Parse(time.RFC3339, glRelease.ReleasedAt); err == nil {
			createdAt = t
		}
	}

	return &Release{
		Version:   version,
		TagName:   glRelease.TagName,
		Changelog: glRelease.Description,
		Assets:    assets,
		URL:       glRelease.Links.Self,
		CreatedAt: createdAt,
	}
}

// Download downloads an asset from GitLab.
// Uses a download cache to avoid re-downloading the same file.
func (g *GitLab) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
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

	resp, err := g.doAssetDownload(ctx, dlClient, asset.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

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

	// Save to download cache (best-effort, ignore errors) unless skipped
	if !g.SkipDownloadCache {
		if cachedPath, err := SaveToDownloadCache(asset.URL, asset.Name, destPath); err == nil {
			os.Remove(destPath)
			destPath = cachedPath
		}
	}

	asset.LocalPath = destPath
	return destPath, nil
}

// doAssetDownload GETs url and, when GitLab returns its external-redirect
// interstitial (HTTP 200 HTML, no Location), follows the embedded href once.
func (g *GitLab) doAssetDownload(ctx context.Context, client *http.Client, downloadURL string) (*http.Response, error) {
	resp, err := getOK(ctx, client, downloadURL)
	if err != nil {
		return nil, err
	}

	externalURL, ok, err := maybeGitLabExternalRedirect(resp)
	if err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("download failed: %w", err)
	}
	if !ok {
		return resp, nil
	}

	resp.Body.Close()
	resp, err = getOK(ctx, client, externalURL)
	if err != nil {
		return nil, fmt.Errorf("follow GitLab external redirect: %w", err)
	}

	// Refuse a second interstitial — avoid loops if GitLab points at itself.
	if _, again, err := maybeGitLabExternalRedirect(resp); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("follow GitLab external redirect: %w", err)
	} else if again {
		resp.Body.Close()
		return nil, fmt.Errorf("GitLab external redirect loop for %s", downloadURL)
	}

	return resp, nil
}

func getOK(ctx context.Context, client *http.Client, downloadURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := doAPKDownload(ctx, client, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download failed with status %d: %s", resp.StatusCode, downloadURL)
	}
	return resp, nil
}

// maybeGitLabExternalRedirect inspects a response for GitLab's interstitial
// HTML page. On match it consumes the body and returns the external URL.
// Non-interstitial bodies are restored so the caller can still read them.
func maybeGitLabExternalRedirect(resp *http.Response) (string, bool, error) {
	if resp == nil || resp.Body == nil {
		return "", false, nil
	}
	if !looksLikeGitLabExternalRedirect(resp) {
		return "", false, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return "", false, fmt.Errorf("read GitLab redirect page: %w", err)
	}

	if !strings.Contains(string(body), gitlabExternalRedirectMarker) {
		// Not the interstitial — put the peeked bytes back for the caller.
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return "", false, nil
	}

	externalURL, ok := parseGitLabExternalRedirect(body)
	if !ok {
		return "", false, fmt.Errorf("GitLab redirect page missing external URL")
	}
	return externalURL, true, nil
}

func looksLikeGitLabExternalRedirect(resp *http.Response) bool {
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html")
}

// parseGitLabExternalRedirect extracts the external download URL from GitLab's
// "redirected away from GitLab" interstitial HTML.
func parseGitLabExternalRedirect(body []byte) (string, bool) {
	if !strings.Contains(string(body), gitlabExternalRedirectMarker) {
		return "", false
	}
	match := gitlabExternalRedirectHref.FindSubmatch(body)
	if len(match) < 2 {
		return "", false
	}
	externalURL := string(match[1])
	parsed, err := url.Parse(externalURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", false
	}
	return externalURL, true
}

// matchesReleaseFilter checks if a tag name matches the configured release_filter.
// Returns true if no filter is configured or if the tag matches the filter.
func (g *GitLab) matchesReleaseFilter(tagName string) bool {
	if g.cfg.ReleaseFilter == "" {
		return true
	}
	matched, err := regexp.MatchString(g.cfg.ReleaseFilter, tagName)
	if err != nil {
		return false
	}
	return matched
}
