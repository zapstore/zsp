package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zapstore/zsp/v2/internal/config"
	"github.com/zapstore/zsp/v2/internal/sanitize"
)

// gitlabArchRegex extracts architecture from GitLab asset names like "APK (arm64-v8a)"
var gitlabArchRegex = regexp.MustCompile(`\((arm64-v8a|armeabi-v7a|arm|x86_64|x86)\)`)

// semverCore is major.minor.patch with no pre-release or build suffix.
var semverCore = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// preReleaseIdent matches one SemVer pre-release identifier from NIP-82, plus snapshot.
// A trailing number still matches (rc42, beta2). Longer names come first so preview
// is not read as pre. Edition suffixes such as ee and fips do not match.
var preReleaseIdent = regexp.MustCompile(`(?i)^(preview|develop|alpha|beta|snapshot|dev|rc|pre|a|b)\d*$`)

// gitlabMarkdownLinkRegex matches markdown links in release descriptions.
// Used to find APKs attached via GitLab uploads (e.g. [app.apk](/uploads/.../app.apk))
// rather than formal release asset links.
var gitlabMarkdownLinkRegex = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)

// gitlabExternalRedirectMarker is present in GitLab's HTML interstitial for
// externally hosted release assets. GitLab returns HTTP 200 with this page
// instead of a Location header, so http.Client cannot follow it automatically.
const gitlabExternalRedirectMarker = "You are being redirected away from GitLab"

// gitlabExternalRedirectHref matches the external target link on that page.
var gitlabExternalRedirectHref = regexp.MustCompile(`(?i)href=["'](https?://[^"']+)["']`)

// gitlabCache stores a fingerprint of the release zsp would download. GitLab's
// releases API does not support conditional requests, so the next fetch reads
// the list again and skips the APK download when this fingerprint matches.
type gitlabCache struct {
	Fingerprint string `json:"fingerprint,omitempty"`
}

// GitLab implements Source for GitLab releases.
// Supports both gitlab.com and self-hosted GitLab instances.
type GitLab struct {
	baseURL            string // e.g., "https://gitlab.com" or self-hosted URL
	repoPath           string // namespace/project, for messages
	projectID          string // URL-encoded project path (e.g., "user%2Frepo")
	numericProjectID   int    // GitLab numeric project id (needed for /-/project/:id/uploads/ URLs)
	client             *http.Client
	downloadClient     *http.Client
	filter             *releaseFilter
	cacheDir           string
	SkipHTTPCache      bool
	IncludePreReleases bool
	maxReleases        int
	pendingCache       *gitlabCache
	moved              *movedProbe
}

// NewGitLab creates a new GitLab source.
func NewGitLab(cfg *config.Config) (*GitLab, error) {
	return newGitLab(cfg, Options{})
}

func newGitLab(cfg *config.Config, opts Options) (*GitLab, error) {
	repoURL := cfg.GetAPKSourceURL()

	baseURL, repoPath := config.GetGitLabRepoWithBase(repoURL)
	if repoPath == "" {
		return nil, fmt.Errorf("invalid GitLab URL: %s", repoURL)
	}
	if err := validateDownloadURL(baseURL); err != nil {
		return nil, fmt.Errorf("gitlab base URL: %w", err)
	}
	filter, err := newReleaseFilter(cfg.ReleaseFilter)
	if err != nil {
		return nil, err
	}

	tor := opts.tor()
	return &GitLab{
		baseURL:        baseURL,
		repoPath:       repoPath,
		projectID:      url.PathEscape(repoPath),
		client:         newSecureHTTPClient(30*time.Second, tor),
		downloadClient: newDownloadHTTPClient(tor),
		filter:         filter,
		cacheDir:       sourceCacheDir(opts.CacheDir, "gitlab"),
		moved: newMovedProbe(opts.OnMoved, repoURL, repoPath, func(rawURL string) string {
			_, path := config.GetGitLabRepoWithBase(rawURL)
			return path
		}),
	}, nil
}

// Type returns the source type.
func (g *GitLab) Type() config.SourceType {
	return config.SourceGitLab
}

// gitlabRelease represents a GitLab release API response.
type gitlabRelease struct {
	TagName         string `json:"tag_name"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	ReleasedAt      string `json:"released_at"`
	UpcomingRelease bool   `json:"upcoming_release"`
	Links           struct {
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
// Iterates through the newest releases to find one with APK assets (for repos that
// publish desktop and mobile releases separately). The scan size defaults to 10.
// Upcoming releases are always skipped. Pre-release tags (v1.2.3-rc.1, v1.2.0-alpha)
// are skipped unless IncludePreReleases is set. GitLab has no prerelease flag;
// the tag is the marker.
//
// Returns ErrNotModified when the release zsp would download matches the
// fingerprint stored after the previous fetch. The releases list is still
// read: GitLab does not support conditional requests. Set SkipHTTPCache to
// bypass the check and always fetch fresh data.
func (g *GitLab) FetchLatestRelease(ctx context.Context) (*Release, error) {
	// GitLab API: GET /projects/:id/releases
	// Returns releases sorted by released_at descending
	limit := releaseScanLimit(g.maxReleases)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/releases?per_page=%d", g.baseURL, g.projectID, limit)

	req, err := newForgeRequest(ctx, config.SourceGitLab, apiURL)
	if err != nil {
		return nil, err
	}
	var cache *gitlabCache
	if !g.SkipHTTPCache {
		cache = g.loadCache()
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch releases: %w", err)
	}
	defer resp.Body.Close()
	g.moved.observe(ctx, g.client, resp)

	if err := checkHTTPStatus(resp, "GitLab API"); err != nil {
		return nil, describeNotFound(err, fmt.Sprintf("no releases found or project %s not accessible", g.repoPath))
	}

	var releases []gitlabRelease
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &releases); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}

	if len(releases) == 0 {
		return nil, fmt.Errorf("no releases found for %s", g.repoPath)
	}

	// Find the first release with valid APKs.
	// Upcoming releases are not published yet. Pre-release tags are skipped
	// unless the caller opted in; edition suffixes such as -ee stay eligible.
	for i := range releases {
		glRelease := &releases[i]
		if glRelease.UpcomingRelease {
			continue
		}
		if !g.IncludePreReleases && semverPreRelease(glRelease.TagName) {
			continue
		}
		if !g.filter.matches(glRelease.TagName, glRelease.Name) {
			continue
		}
		fingerprint := gitlabReleaseFingerprint(glRelease)
		if !g.SkipHTTPCache && cache != nil && cache.Fingerprint == fingerprint {
			return nil, ErrNotModified
		}
		// Numeric id resolves markdown /uploads/ attachments. An unchanged
		// release returns above and does not spend this request.
		if err := g.ensureNumericProjectID(ctx); err != nil {
			return nil, err
		}
		release := g.convertRelease(glRelease)
		if !HasValidAPKs(release.Assets) {
			continue
		}
		if !g.SkipHTTPCache {
			g.pendingCache = &gitlabCache{Fingerprint: fingerprint}
		}
		return release, nil
	}

	return nil, fmt.Errorf("no releases with valid APKs found in the last %d releases for %s", limit, g.repoPath)
}

func (g *GitLab) cacheFilePath() string {
	parts := strings.Split(g.repoPath, "/")
	for i, part := range parts {
		parts[i] = filepath.Base(part)
	}
	name := strings.Join(parts, "_")
	if name == "" || name == "." || name == ".." {
		name = "project"
	}
	return filepath.Join(g.cacheDir, name+".json")
}

func (g *GitLab) loadCache() *gitlabCache {
	if g.cacheDir == "" {
		return nil
	}
	data, err := os.ReadFile(g.cacheFilePath())
	if err != nil {
		return nil
	}
	var cache gitlabCache
	if err := json.Unmarshal(data, &cache); err != nil || cache.Fingerprint == "" {
		return nil
	}
	return &cache
}

func (g *GitLab) saveCache(cache *gitlabCache) error {
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
func (g *GitLab) CommitCache() error {
	if g.pendingCache == nil {
		return nil
	}
	if err := g.saveCache(g.pendingCache); err != nil {
		return err
	}
	g.pendingCache = nil
	return nil
}

// ClearHTTPCache deletes the persisted validators so the next fetch is unconditional.
func (g *GitLab) ClearHTTPCache() error {
	g.pendingCache = nil
	if g.cacheDir == "" {
		return nil
	}
	err := os.Remove(g.cacheFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// gitlabReleaseFingerprint hashes the release fields that decide which APK to
// download. Fields GitLab adds and this client does not read stay out of the
// hash, so they do not force another download.
func gitlabReleaseFingerprint(release *gitlabRelease) string {
	links := slices.Clone(release.Assets.Links)
	slices.SortFunc(links, func(a, b gitlabAssetLink) int {
		if byName := strings.Compare(a.Name, b.Name); byName != 0 {
			return byName
		}
		if byURL := strings.Compare(a.URL, b.URL); byURL != 0 {
			return byURL
		}
		return strings.Compare(a.DirectAssetURL, b.DirectAssetURL)
	})
	payload, _ := json.Marshal(struct {
		TagName         string
		Name            string
		Description     string
		ReleasedAt      string
		UpcomingRelease bool
		Self            string
		Links           []gitlabAssetLink
	}{
		release.TagName, release.Name, release.Description, release.ReleasedAt,
		release.UpcomingRelease, release.Links.Self, links,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// ensureNumericProjectID loads the project's numeric id from the GitLab API once.
// Markdown upload links resolve to /-/project/:id/uploads/..., not /group/repo/uploads/...
func (g *GitLab) ensureNumericProjectID(ctx context.Context) error {
	if g.numericProjectID > 0 {
		return nil
	}

	apiURL := fmt.Sprintf("%s/api/v4/projects/%s", g.baseURL, g.projectID)
	req, err := newForgeRequest(ctx, config.SourceGitLab, apiURL)
	if err != nil {
		return err
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch GitLab project: %w", err)
	}
	defer resp.Body.Close()

	if err := checkHTTPStatus(resp, "GitLab API"); err != nil {
		return describeNotFound(err, fmt.Sprintf("GitLab project %s not found or not accessible", g.repoPath))
	}

	var project struct {
		ID int `json:"id"`
	}
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &project); err != nil {
		return fmt.Errorf("parse GitLab project: %w", err)
	}
	if project.ID <= 0 {
		return fmt.Errorf("GitLab project %s response missing id", g.repoPath)
	}

	g.numericProjectID = project.ID
	return nil
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

	// Some projects (e.g. AuroraStore since 4.8.1) attach APKs only as markdown
	// uploads in the description, with no formal assets.links entries.
	assets = mergeGitLabAssets(assets, g.descriptionAPKAssets(glRelease.Description))

	return &Release{
		Version:    versionFromTag(glRelease.TagName),
		TagName:    glRelease.TagName,
		Name:       glRelease.Name,
		Changelog:  glRelease.Description,
		Assets:     assets,
		PreRelease: semverPreRelease(glRelease.TagName),
		URL:        glRelease.Links.Self,
		CreatedAt:  parseReleaseTime(glRelease.ReleasedAt),
	}
}

// Download downloads an asset from GitLab, using the shared DownloadHTTP
// retry/size/stall pipeline while preserving GitLab-specific handling of the
// external-redirect interstitial page (see doAssetDownload).
func (g *GitLab) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", fmt.Errorf("asset has no download URL")
	}
	if err := validateDownloadURL(asset.URL); err != nil {
		return "", err
	}

	destPath, err := prepareDownloadDest(destDir, asset.Name)
	if err != nil {
		return "", err
	}

	client := g.downloadClient
	if client == nil {
		client = newDownloadHTTPClient(nil)
	}
	fetch := func(ctx context.Context) (*http.Response, error) {
		return g.doAssetDownload(ctx, client, asset.URL)
	}
	if err := downloadWithRetries(ctx, destPath, asset.Size, asset.SHA256, progress, fetch); err != nil {
		return "", err
	}

	asset.LocalPath = destPath
	return destPath, nil
}

// doAssetDownload GETs url and, when GitLab returns its external-redirect
// interstitial (HTTP 200 HTML, no Location), follows the embedded href once.
// The returned response's body holds the actual asset bytes; the caller
// (downloadWithRetries, via writeDownloadResponse) applies the shared size
// limit and stall detection when streaming it to disk.
func (g *GitLab) doAssetDownload(ctx context.Context, client *http.Client, downloadURL string) (*http.Response, error) {
	resp, err := doGet(ctx, client, downloadURL, forgeAuthHeaders(config.SourceGitLab, downloadURL))
	if err != nil {
		return nil, err
	}

	externalURL, ok, err := maybeGitLabExternalRedirect(resp)
	if err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("download: %w", err)
	}
	if !ok {
		return resp, nil
	}

	resp.Body.Close()
	if err := validateDownloadURL(externalURL); err != nil {
		return nil, fmt.Errorf("follow GitLab external redirect: %w", err)
	}
	resp, err = doGet(ctx, client, externalURL, forgeAuthHeaders(config.SourceGitLab, externalURL))
	if err != nil {
		return nil, fmt.Errorf("follow GitLab external redirect: %w", err)
	}

	// Refuse a second interstitial — avoid loops if GitLab points at itself.
	if _, again, err := maybeGitLabExternalRedirect(resp); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("follow GitLab external redirect: %w", err)
	} else if again {
		resp.Body.Close()
		return nil, fmt.Errorf("GitLab external redirect loop for %s", sanitize.URL(downloadURL))
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
		// Not the interstitial: put the peeked bytes back in front of the rest
		// of the body, which stays the one to close.
		resp.Body = &peekedBody{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), Closer: resp.Body}
		return "", false, nil
	}

	externalURL, ok := parseGitLabExternalRedirect(body)
	if !ok {
		return "", false, fmt.Errorf("GitLab redirect page missing external URL")
	}
	return externalURL, true, nil
}

// peekedBody reads previously peeked bytes followed by the rest of the original
// body, and closes that body.
type peekedBody struct {
	io.Reader
	io.Closer
}

func looksLikeGitLabExternalRedirect(resp *http.Response) bool {
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html")
}

// parseGitLabExternalRedirect extracts the external download URL from GitLab's
// "redirected away from GitLab" interstitial HTML. The extracted URL must
// satisfy the same HTTPS-outside-loopback rule as an explicit configuration
// URL; doAssetDownload re-validates it before following it, but rejecting an
// unsafe target here keeps this function's contract self-contained.
func parseGitLabExternalRedirect(body []byte) (string, bool) {
	if !strings.Contains(string(body), gitlabExternalRedirectMarker) {
		return "", false
	}
	match := gitlabExternalRedirectHref.FindSubmatch(body)
	if len(match) < 2 {
		return "", false
	}
	externalURL := string(match[1])
	if validateDownloadURL(externalURL) != nil {
		return "", false
	}
	return externalURL, true
}

// semverPreRelease reports whether tag is a SemVer version with a pre-release
// identifier. GitLab stores that only in the tag. v19.4.0-rc42-ee matches
// because of rc42; v15.5.4-ee does not. Build metadata after + is ignored.
func semverPreRelease(tag string) bool {
	tag = strings.TrimSpace(tag)
	if strings.HasPrefix(tag, "v") || strings.HasPrefix(tag, "V") {
		tag = tag[1:]
	}
	tag, _, _ = strings.Cut(tag, "+")
	core, rest, ok := strings.Cut(tag, "-")
	if !ok || rest == "" || !semverCore.MatchString(core) {
		return false
	}
	for _, seg := range strings.FieldsFunc(rest, func(r rune) bool {
		return r == '.' || r == '-'
	}) {
		if preReleaseIdent.MatchString(seg) {
			return true
		}
	}
	return false
}

// descriptionAPKAssets extracts APK assets from markdown links in a release description.
// GitLab projects often attach binaries via /uploads/... links instead of assets.links.
func (g *GitLab) descriptionAPKAssets(description string) []*Asset {
	if description == "" {
		return nil
	}

	var assets []*Asset
	seen := make(map[string]bool)

	for _, match := range gitlabMarkdownLinkRegex.FindAllStringSubmatch(description, -1) {
		if len(match) < 3 {
			continue
		}
		linkText := strings.TrimSpace(match[1])
		rawURL := strings.TrimSpace(match[2])
		if rawURL == "" {
			continue
		}

		downloadURL, ok := resolveGitLabDescriptionURL(g.baseURL, g.numericProjectID, rawURL)
		if !ok {
			continue
		}

		name := apkAssetName(linkText, downloadURL)
		if !IsAPKAsset(name, downloadURL) {
			continue
		}

		key := strings.ToLower(urlPathBase(name))
		if key == "" || key == "." || seen[key] {
			continue
		}
		seen[key] = true

		assets = append(assets, &Asset{
			Name: name,
			URL:  downloadURL,
		})
	}

	return assets
}

// resolveGitLabDescriptionURL turns a markdown href into an absolute download URL.
// Relative /uploads/... paths become /-/project/:id/uploads/... (GitLab's public upload URL).
func resolveGitLabDescriptionURL(baseURL string, numericProjectID int, rawURL string) (string, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", false
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}

	switch {
	case parsed.Scheme == "http" || parsed.Scheme == "https":
		if parsed.Host == "" {
			return "", false
		}
		return parsed.String(), true
	case parsed.Scheme != "":
		// Reject javascript:, data:, etc.
		return "", false
	}

	// Relative path — typically /uploads/<hash>/<file>.apk
	path := parsed.Path
	if path == "" {
		path = rawURL
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if baseURL == "" || numericProjectID <= 0 {
		return "", false
	}
	if !strings.HasPrefix(path, "/uploads/") {
		// Only auto-resolve GitLab upload attachments; other relative links are ambiguous.
		return "", false
	}

	// Namespace-path uploads (/{group}/{repo}/uploads/...) 404 on gitlab.com;
	// the working form is /-/project/:id/uploads/...
	return fmt.Sprintf("%s/-/project/%d%s", strings.TrimRight(baseURL, "/"), numericProjectID, path), true
}

// apkAssetName prefers a .apk link label, otherwise the URL basename.
func apkAssetName(linkText, downloadURL string) string {
	if strings.HasSuffix(strings.ToLower(linkText), ".apk") {
		return urlPathBase(linkText)
	}
	if parsed, err := url.Parse(downloadURL); err == nil {
		if base := urlPathBase(parsed.Path); base != "" && base != "." {
			return base
		}
	}
	if linkText != "" {
		return linkText
	}
	return urlPathBase(downloadURL)
}

// urlPathBase returns the final path segment using '/' separators (URL-safe).
func urlPathBase(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// mergeGitLabAssets appends extras whose basenames are not already present in existing.
// Formal assets.links win over description uploads when both exist.
func mergeGitLabAssets(existing, extras []*Asset) []*Asset {
	if len(extras) == 0 {
		return existing
	}
	seen := make(map[string]bool, len(existing))
	for _, a := range existing {
		if a == nil {
			continue
		}
		seen[strings.ToLower(urlPathBase(a.Name))] = true
	}
	for _, a := range extras {
		if a == nil {
			continue
		}
		key := strings.ToLower(urlPathBase(a.Name))
		if key == "" || key == "." || seen[key] {
			continue
		}
		seen[key] = true
		existing = append(existing, a)
	}
	return existing
}
