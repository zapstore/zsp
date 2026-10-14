// Package source provides metadata fetching from various sources.
package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/sanitize"
	"github.com/zapstore/zsp/internal/spdx"
	"gopkg.in/yaml.v3"
)

// AppMetadata contains enriched app metadata from one external source.
type AppMetadata struct {
	// Source is the name of the metadata source that produced this value.
	Source      string
	Name        string
	Description string
	Summary     string
	Website     string
	License     string
	Tags        []string
	// ImageURLs and IconURL are remote locations. A remote source is untrusted,
	// so whoever consumes them must apply the URL policy.
	ImageURLs []string
	IconURL   string
	// LocalIcon and LocalImages are absolute paths inside the config's base
	// directory. Only the local Fastlane loader sets them; they are paths to
	// open with the localpath package, never to trust.
	LocalIcon   string
	LocalImages []string
}

// MetadataFetcher fetches metadata from external sources.
type MetadataFetcher struct {
	cfg       *config.Config
	client    *http.Client
	PackageID string // App package ID (e.g., "com.example.app") - set from APK parsing
}

// NewMetadataFetcher creates a new metadata fetcher.
func NewMetadataFetcher(cfg *config.Config) *MetadataFetcher {
	return &MetadataFetcher{
		cfg:    cfg,
		client: newSecureHTTPClient(30*time.Second, nil),
	}
}

// NewMetadataFetcherWithPackageID creates a new metadata fetcher with a known package ID.
func NewMetadataFetcherWithPackageID(cfg *config.Config, packageID string) *MetadataFetcher {
	return &MetadataFetcher{
		cfg:       cfg,
		client:    newSecureHTTPClient(30*time.Second, nil),
		PackageID: packageID,
	}
}

// repositoryMetadataHost returns the forge that hosts cfg.Repository for
// metadata purposes. Self-hosted Gitea/Forgejo may not be detectable from the
// hostname alone; in that case an explicit release_source type of gitea is used.
func repositoryMetadataHost(cfg *config.Config) config.SourceType {
	if cfg == nil || cfg.Repository == "" {
		return config.SourceUnknown
	}
	if t := config.DetectSourceType(cfg.Repository); t != config.SourceUnknown {
		return t
	}
	if cfg.GetSourceType() == config.SourceGitea {
		return config.SourceGitea
	}
	return config.SourceUnknown
}

// MetadataPlan lists the metadata sources to query. It is explicit unless
// Automatic is set; an automatic plan starts with Fastlane and queries the
// sources after it only when Fastlane is missing, has no description, or
// could not be read.
type MetadataPlan struct {
	Sources   []string
	Automatic bool
}

// PlanMetadata returns the sources cfg selects. A non-nil MetadataSources
// is an explicit list, and an empty one disables metadata. A nil one is
// automatic: Fastlane first, then the native source of a GitHub or GitLab
// repository. Gitea uses Fastlane only, and a local Fastlane tree is used
// without a repository. Automatic metadata never infers F-Droid or Play Store.
func PlanMetadata(cfg *config.Config) MetadataPlan {
	if cfg.MetadataSources != nil {
		var sources []string
		for _, name := range cfg.MetadataSources {
			name = strings.ToLower(strings.TrimSpace(name))
			if name != "" && !slices.Contains(sources, name) {
				sources = append(sources, name)
			}
		}
		return MetadataPlan{Sources: sources}
	}

	switch repositoryMetadataHost(cfg) {
	case config.SourceGitHub:
		return MetadataPlan{Sources: []string{"fastlane", "github"}, Automatic: true}
	case config.SourceGitLab:
		return MetadataPlan{Sources: []string{"fastlane", "gitlab"}, Automatic: true}
	case config.SourceGitea:
		return MetadataPlan{Sources: []string{"fastlane"}, Automatic: true}
	default:
		if hasLocalFastlane(cfg.BaseDir) {
			return MetadataPlan{Sources: []string{"fastlane"}, Automatic: true}
		}
		return MetadataPlan{}
	}
}

// MetadataError represents a non-fatal error when fetching metadata from a source.
type MetadataError struct {
	Source string
	Err    error
}

func (e *MetadataError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("fetch %s metadata", e.Source)
	}
	return fmt.Sprintf("fetch %s metadata: %s", e.Source, sanitize.Text(e.Err.Error()))
}

func (e *MetadataError) Unwrap() error { return e.Err }

// MetadataResult contains what a metadata plan produced.
type MetadataResult struct {
	// Metadata holds one entry per source that answered, in plan order.
	Metadata []*AppMetadata
	// Errors contains non-fatal errors from individual sources.
	// The fetch continues even if some sources fail.
	Errors []*MetadataError
}

// HasErrors returns true if any metadata sources failed.
func (r *MetadataResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// Fetch queries the sources of plan and returns every answer in order. It
// changes nothing: choosing among the answers is the caller's job. A failing
// source is recorded and the next one is tried; cancellation stops the plan.
func (f *MetadataFetcher) Fetch(ctx context.Context, plan MetadataPlan) *MetadataResult {
	result := &MetadataResult{}
	if !plan.Automatic {
		f.fetchInto(ctx, result, plan.Sources)
		return result
	}
	if len(plan.Sources) == 0 {
		return result
	}

	first := plan.Sources[0]
	meta, err := f.fetchMetadataSource(ctx, first)
	switch {
	case err == nil:
		result.add(first, meta)
		if strings.TrimSpace(meta.Description) != "" {
			return result
		}
	case isCancellation(err):
		result.fail(first, err)
		return result
	case !errors.Is(err, errFastlaneUnavailable):
		result.fail(first, err)
	}
	f.fetchInto(ctx, result, plan.Sources[1:])
	return result
}

func (f *MetadataFetcher) fetchInto(ctx context.Context, result *MetadataResult, sources []string) {
	for _, name := range sources {
		if err := ctx.Err(); err != nil {
			result.fail(name, err)
			return
		}
		meta, err := f.fetchMetadataSource(ctx, name)
		if err != nil {
			result.fail(name, err)
			if isCancellation(err) {
				return
			}
			continue
		}
		result.add(name, meta)
	}
}

func (r *MetadataResult) add(source string, meta *AppMetadata) {
	if meta == nil {
		return
	}
	meta.Source = source
	r.Metadata = append(r.Metadata, meta)
}

func (r *MetadataResult) fail(source string, err error) {
	r.Errors = append(r.Errors, &MetadataError{Source: source, Err: err})
}

func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (f *MetadataFetcher) fetchMetadataSource(ctx context.Context, source string) (*AppMetadata, error) {
	switch source {
	case "fastlane":
		return f.fetchFastlaneMetadata(ctx)
	case "github":
		return f.fetchGitHubMetadata(ctx)
	case "gitlab":
		return f.fetchGitLabMetadata(ctx)
	case "gitea":
		return f.fetchGiteaMetadata(ctx)
	case "fdroid":
		return f.fetchFDroidMetadata(ctx)
	case "playstore":
		return f.fetchPlayStoreMetadata(ctx)
	default:
		return nil, fmt.Errorf("unknown metadata source")
	}
}

// fetchGitHubMetadata fetches metadata from GitHub repository.
func (f *MetadataFetcher) fetchGitHubMetadata(ctx context.Context) (*AppMetadata, error) {
	repoPath := config.GetGitHubRepo(f.cfg.Repository)
	if repoPath == "" {
		return nil, fmt.Errorf("no GitHub repository configured")
	}

	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid GitHub repo path: %s", repoPath)
	}

	owner, repo := parts[0], parts[1]

	// Fetch repository info
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := newForgeRequest(ctx, config.SourceGitHub, url)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch repo info: %w", err)
	}
	defer resp.Body.Close()

	if err := checkHTTPStatus(resp, "GitHub API"); err != nil {
		return nil, err
	}

	var repoInfo struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Homepage    string   `json:"homepage"`
		Topics      []string `json:"topics"`
		License     *struct {
			SPDXID string `json:"spdx_id"`
		} `json:"license"`
	}

	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &repoInfo); err != nil {
		return nil, fmt.Errorf("parse repo info: %w", err)
	}

	meta := &AppMetadata{
		Name:        repoInfo.Name,
		Description: repoInfo.Description,
		Website:     repoInfo.Homepage,
		Tags:        repoInfo.Topics,
	}

	if repoInfo.License != nil {
		meta.License = spdx.ID(repoInfo.License.SPDXID)
	}

	// Try to fetch README for a longer description if the repo description is short
	if len(repoInfo.Description) < 100 {
		readme, err := f.fetchGitHubReadme(ctx, owner, repo)
		if err == nil && readme != "" {
			// Use first paragraph of README as description
			firstPara := extractFirstParagraph(readme)
			if len(firstPara) > len(repoInfo.Description) {
				meta.Description = firstPara
			}
		}
	}

	return meta, nil
}

// fetchGitHubReadme fetches the README content from GitHub.
func (f *MetadataFetcher) fetchGitHubReadme(ctx context.Context, owner, repo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/readme", owner, repo)
	req, err := newForgeRequest(ctx, config.SourceGitHub, url)
	if err != nil {
		return "", err
	}

	// Request raw content
	req.Header.Set("Accept", "application/vnd.github.raw")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if err := checkHTTPStatus(resp, "GitHub readme"); err != nil {
		return "", err
	}

	// Security: bound the response to prevent memory exhaustion
	body, err := readBounded(resp.Body, MaxRemoteDownloadSize)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// fetchGitLabMetadata fetches metadata from GitLab repository.
func (f *MetadataFetcher) fetchGitLabMetadata(ctx context.Context) (*AppMetadata, error) {
	baseURL, repoPath := config.GetGitLabRepoWithBase(f.cfg.Repository)
	if repoPath == "" {
		// Try release_source if repository doesn't have GitLab
		if f.cfg.ReleaseSource != nil {
			baseURL, repoPath = config.GetGitLabRepoWithBase(f.cfg.ReleaseSource.URL)
		}
	}
	if repoPath == "" {
		return nil, fmt.Errorf("no GitLab repository configured")
	}

	// URL-encode the project path for API calls
	encodedPath := strings.ReplaceAll(repoPath, "/", "%2F")

	// Fetch project info
	url := fmt.Sprintf("%s/api/v4/projects/%s", baseURL, encodedPath)
	req, err := newForgeRequest(ctx, config.SourceGitLab, url)
	if err != nil {
		return nil, err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch project info: %w", err)
	}
	defer resp.Body.Close()

	if err := checkHTTPStatus(resp, "GitLab API"); err != nil {
		return nil, describeNotFound(err, fmt.Sprintf("GitLab project %s not found or not accessible", repoPath))
	}

	var projectInfo struct {
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		Topics        []string `json:"topics"`
		DefaultBranch string   `json:"default_branch"`
		ReadmeURL     string   `json:"readme_url"`
		ForksCount    int      `json:"forks_count"`
		StarCount     int      `json:"star_count"`
		License       *struct {
			Key      string `json:"key"`
			Name     string `json:"name"`
			Nickname string `json:"nickname"`
		} `json:"license"`
	}

	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &projectInfo); err != nil {
		return nil, fmt.Errorf("parse project info: %w", err)
	}

	// Website stays empty: web_url is the GitLab project page, not a homepage.
	meta := &AppMetadata{
		Name:        projectInfo.Name,
		Description: projectInfo.Description,
		Tags:        projectInfo.Topics,
	}

	if projectInfo.License != nil {
		meta.License = spdx.ID(projectInfo.License.Key)
		if meta.License == "" {
			meta.License = spdx.ID(projectInfo.License.Name)
		}
	}

	// Try to fetch README for a longer description if the project description is short
	if len(projectInfo.Description) < 100 && projectInfo.DefaultBranch != "" {
		readme, err := f.fetchGitLabReadme(ctx, baseURL, encodedPath, projectInfo.DefaultBranch)
		if err == nil && readme != "" {
			firstPara := extractFirstParagraph(readme)
			if len(firstPara) > len(projectInfo.Description) {
				meta.Description = firstPara
			}
		}
	}

	return meta, nil
}

// fetchGitLabReadme fetches the README content from GitLab.
func (f *MetadataFetcher) fetchGitLabReadme(ctx context.Context, baseURL, encodedPath, branch string) (string, error) {
	// GitLab API: GET /projects/:id/repository/files/:file_path/raw?ref=:branch
	// Try common README filenames
	readmeNames := []string{"README.md", "README", "readme.md", "Readme.md"}

	for _, name := range readmeNames {
		encodedName := strings.ReplaceAll(name, ".", "%2E")
		url := fmt.Sprintf("%s/api/v4/projects/%s/repository/files/%s/raw?ref=%s",
			baseURL, encodedPath, encodedName, neturl.QueryEscape(branch))

		body, ok := f.fetchGitLabReadmeFile(ctx, url)
		if ok {
			return body, nil
		}
	}

	return "", errors.New("no README found")
}

// fetchGitLabReadmeFile reads one README candidate. It reports false for
// anything but a complete 200 response, so the caller tries the next name.
func (f *MetadataFetcher) fetchGitLabReadmeFile(ctx context.Context, url string) (string, bool) {
	req, err := newForgeRequest(ctx, config.SourceGitLab, url)
	if err != nil {
		return "", false
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	// Security: bound the response to prevent memory exhaustion
	body, err := readBounded(resp.Body, MaxRemoteDownloadSize)
	if err != nil {
		return "", false
	}
	return string(body), true
}

// fetchGiteaMetadata fetches native repository metadata from a
// Gitea/Forgejo/Codeberg-compatible API.
func (f *MetadataFetcher) fetchGiteaMetadata(ctx context.Context) (*AppMetadata, error) {
	baseURL, repoPath := config.GetGiteaRepo(f.cfg.Repository)
	if repoPath == "" && f.cfg.ReleaseSource != nil {
		baseURL, repoPath = config.GetGiteaRepo(f.cfg.ReleaseSource.URL)
	}
	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("no Gitea repository configured")
	}

	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s", baseURL, parts[0], parts[1])
	req, err := newForgeRequest(ctx, config.SourceGitea, apiURL)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch repository info: %w", err)
	}
	defer resp.Body.Close()
	if err := checkHTTPStatus(resp, "Gitea API"); err != nil {
		return nil, err
	}

	var repoInfo struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Website     string   `json:"website"`
		Topics      []string `json:"topics"`
	}
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &repoInfo); err != nil {
		return nil, fmt.Errorf("parse repository info: %w", err)
	}
	return &AppMetadata{
		Name:        repoInfo.Name,
		Description: repoInfo.Description,
		Website:     repoInfo.Website,
		Tags:        repoInfo.Topics,
	}, nil
}

// fetchFDroidMetadata fetches metadata from F-Droid by scraping the website.
// This is much more efficient than downloading the huge index-v1.json file.
func (f *MetadataFetcher) fetchFDroidMetadata(ctx context.Context) (*AppMetadata, error) {
	// Determine package ID
	packageID := f.PackageID

	// Try to get from release_source if not set
	if packageID == "" && f.cfg.ReleaseSource != nil {
		repoInfo := config.GetFDroidRepoInfo(f.cfg.ReleaseSource.URL)
		if repoInfo != nil {
			packageID = repoInfo.PackageID
		}
	}

	if packageID == "" {
		return nil, fmt.Errorf("no F-Droid package configured and no package ID available")
	}

	// Scrape the F-Droid website for description, summary, icon, and screenshots.
	// fdroiddata YAML no longer carries Summary/Description for most apps;
	// the package page is the published text.
	webMeta, err := f.scrapeFDroidWebsite(ctx, packageID)
	if err != nil {
		return nil, err
	}

	// Also fetch metadata from fdroiddata YAML for categories, license, and
	// any Summary/Description that the file still contains.
	metadataURL := fmt.Sprintf("https://gitlab.com/fdroid/fdroiddata/-/raw/master/metadata/%s.yml", packageID)
	fdMeta, yamlErr := f.fetchFDroidYAML(ctx, metadataURL)

	meta := &AppMetadata{
		IconURL:     webMeta.IconURL,
		ImageURLs:   webMeta.ImageURLs,
		Summary:     webMeta.Summary,
		Description: webMeta.Description,
	}

	// Merge YAML metadata if available. Non-empty YAML text wins over the page.
	if yamlErr == nil && fdMeta != nil {
		if fdMeta.Summary != "" {
			meta.Summary = fdMeta.Summary
		}
		if fdMeta.Description != "" {
			meta.Description = fdMeta.Description
		}
		meta.Website = fdMeta.WebSite
		meta.License = spdx.ID(fdMeta.License)

		if fdMeta.Name != "" {
			meta.Name = fdMeta.Name
		} else if fdMeta.AutoName != "" {
			meta.Name = fdMeta.AutoName
		}

		for _, cat := range fdMeta.Categories {
			meta.Tags = append(meta.Tags, strings.ToLower(cat))
		}
	}

	return meta, nil
}

// fdroidWebMeta contains metadata scraped from the F-Droid website.
type fdroidWebMeta struct {
	Summary     string
	Description string
	IconURL     string
	ImageURLs   []string
}

// scrapeFDroidWebsite scrapes the F-Droid package page for description, summary, icon, and screenshots.
func (f *MetadataFetcher) scrapeFDroidWebsite(ctx context.Context, packageID string) (*fdroidWebMeta, error) {
	url := fmt.Sprintf("https://f-droid.org/en/packages/%s/", packageID)

	req, err := newRequest(ctx, http.MethodGet, url)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch F-Droid page: %w", err)
	}
	defer resp.Body.Close()

	// Validate HTTP status; a 404 names the package.
	if err := checkHTTPStatus(resp, "F-Droid website"); err != nil {
		return nil, describeNotFound(err, fmt.Sprintf("package %s not found on F-Droid", packageID))
	}

	// Security: bound the response to prevent memory exhaustion
	page, err := readBounded(resp.Body, MaxRemoteDownloadSize)
	if err != nil {
		return nil, fmt.Errorf("read F-Droid page: %w", err)
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}

	meta := &fdroidWebMeta{}

	// Extract icon URL from the package header
	// F-Droid uses: <img class="package-icon" src="...">
	doc.Find("img.package-icon").First().Each(func(i int, s *goquery.Selection) {
		if src, exists := s.Attr("src"); exists {
			meta.IconURL = f.normalizeURL(src, "https://f-droid.org")
		}
	})

	// Also try: header img inside .package-header
	if meta.IconURL == "" {
		doc.Find(".package-header img").First().Each(func(i int, s *goquery.Selection) {
			if src, exists := s.Attr("src"); exists {
				meta.IconURL = f.normalizeURL(src, "https://f-droid.org")
			}
		})
	}

	doc.Find(".package-summary").First().Each(func(i int, s *goquery.Selection) {
		meta.Summary = strings.TrimSpace(s.Text())
	})
	doc.Find(".package-description").First().Each(func(i int, s *goquery.Selection) {
		html, err := s.Html()
		if err == nil {
			meta.Description = htmlToMarkdown(html)
		}
		if meta.Description == "" {
			meta.Description = strings.TrimSpace(s.Text())
		}
	})

	// Extract screenshot URLs from the screenshot gallery
	// F-Droid uses: <li class="js_slide screenshot"><img src="..." />
	// or: <div class="screenshots">...<img>
	doc.Find(".screenshot img, .screenshots img, #screenshots img").Each(func(i int, s *goquery.Selection) {
		src := ""
		if imgSrc, exists := s.Attr("src"); exists {
			src = imgSrc
		}

		if src != "" && strings.Contains(src, "/repo/") {
			fullURL := f.normalizeURL(src, "https://f-droid.org")
			// Avoid duplicates
			found := false
			for _, existing := range meta.ImageURLs {
				if existing == fullURL {
					found = true
					break
				}
			}
			if !found {
				meta.ImageURLs = append(meta.ImageURLs, fullURL)
			}
		}
	})

	return meta, nil
}

// normalizeURL converts relative URLs to absolute URLs.
func (f *MetadataFetcher) normalizeURL(urlStr, baseURL string) string {
	if strings.HasPrefix(urlStr, "http://") || strings.HasPrefix(urlStr, "https://") {
		return urlStr
	}
	if strings.HasPrefix(urlStr, "//") {
		return "https:" + urlStr
	}
	if strings.HasPrefix(urlStr, "/") {
		return baseURL + urlStr
	}
	return baseURL + "/" + urlStr
}

// fetchFDroidYAML fetches metadata from the fdroiddata YAML file.
func (f *MetadataFetcher) fetchFDroidYAML(ctx context.Context, metadataURL string) (*fdroidMetadata, error) {
	req, err := newRequest(ctx, http.MethodGet, metadataURL)
	if err != nil {
		return nil, err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch metadata: %w", err)
	}
	defer resp.Body.Close()

	// Validate HTTP status
	if err := checkHTTPStatus(resp, "fdroiddata YAML"); err != nil {
		return nil, err
	}

	// Security: bound the response to prevent memory exhaustion
	data, err := readBounded(resp.Body, MaxRemoteDownloadSize)
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	var meta fdroidMetadata
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}

	return &meta, nil
}

// fetchPlayStoreMetadata fetches metadata from Google Play Store.
func (f *MetadataFetcher) fetchPlayStoreMetadata(ctx context.Context) (*AppMetadata, error) {
	// Get package ID - prefer the one set from APK parsing
	packageID := f.PackageID

	// Fallback: try to get from Play Store URL in release_source
	if packageID == "" && f.cfg.ReleaseSource != nil {
		packageID = GetPlayStorePackageID(f.cfg.ReleaseSource.URL)
	}

	// Fallback: try repository URL
	if packageID == "" {
		packageID = GetPlayStorePackageID(f.cfg.Repository)
	}

	if packageID == "" {
		return nil, fmt.Errorf("no package ID available - ensure APK is parsed first or provide a play.google.com URL")
	}

	// Create Play Store fetcher and get metadata
	ps := NewPlayStore(packageID)
	psMeta, err := ps.FetchMetadata(ctx)
	if err != nil {
		return nil, err
	}

	meta := &AppMetadata{
		Name:        psMeta.Name,
		Description: psMeta.Description,
		ImageURLs:   psMeta.ImageURLs,
		IconURL:     psMeta.IconURL,
	}

	return meta, nil
}

// extractFirstParagraph extracts the first meaningful paragraph from markdown.
func extractFirstParagraph(markdown string) string {
	lines := strings.Split(markdown, "\n")
	var paragraph []string
	inParagraph := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip headers, badges, empty lines at start
		if strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "![") ||
			strings.HasPrefix(trimmed, "[![") ||
			strings.HasPrefix(trimmed, "<") ||
			trimmed == "" {
			if inParagraph && trimmed == "" {
				// End of paragraph
				break
			}
			continue
		}

		inParagraph = true
		paragraph = append(paragraph, trimmed)
	}

	result := strings.Join(paragraph, " ")

	// Truncate if too long
	if len(result) > 500 {
		// Find last sentence boundary
		for i := 500; i > 200; i-- {
			if result[i] == '.' || result[i] == '!' || result[i] == '?' {
				return result[:i+1]
			}
		}
		return result[:500] + "..."
	}

	return result
}
