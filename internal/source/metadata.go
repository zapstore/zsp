// Package source provides metadata fetching from various sources.
package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/zapstore/zsp/internal/config"
	"gopkg.in/yaml.v3"
)

// AppMetadata contains enriched app metadata from external sources.
type AppMetadata struct {
	Name        string
	Description string
	Summary     string
	Website     string
	License     string
	Tags        []string
	ImageURLs   []string
	IconURL     string // URL to app icon (from Play Store or F-Droid)
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
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewMetadataFetcherWithPackageID creates a new metadata fetcher with a known package ID.
func NewMetadataFetcherWithPackageID(cfg *config.Config, packageID string) *MetadataFetcher {
	return &MetadataFetcher{
		cfg:       cfg,
		client:    &http.Client{Timeout: 30 * time.Second},
		PackageID: packageID,
	}
}

// DefaultMetadataSources returns the metadata sources to use.
// The base source type (github, gitlab, fdroid) is always included automatically.
// Any additional sources from metadata_sources config are appended.
// Returns nil if no metadata source applies.
func DefaultMetadataSources(cfg *config.Config) []string {
	var sources []string
	seen := make(map[string]bool)

	// Helper to add source without duplicates
	addSource := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			sources = append(sources, s)
		}
	}

	// Always include the base source type's metadata source
	sourceType := cfg.GetSourceType()
	switch sourceType {
	case config.SourceGitHub:
		addSource("github")
	case config.SourceGitLab:
		addSource("gitlab")
	case config.SourceFDroid:
		addSource("fdroid")
	default:
		// For local, web, or unknown sources, check repository URL for base metadata
		if cfg.Repository != "" {
			repoType := config.DetectSourceType(cfg.Repository)
			switch repoType {
			case config.SourceGitHub:
				addSource("github")
			case config.SourceGitLab:
				addSource("gitlab")
			}
		}
	}

	// Add any explicitly configured metadata sources
	for _, s := range cfg.MetadataSources {
		addSource(strings.ToLower(strings.TrimSpace(s)))
	}

	if len(sources) == 0 {
		return nil
	}
	return sources
}

// FetchMetadata fetches metadata from the specified sources and merges into config.
// Sources can be: "github", "gitlab", "fdroid", "playstore"
// Only empty fields in config are populated (existing values are preserved).
func (f *MetadataFetcher) FetchMetadata(ctx context.Context, sources []string) error {
	for _, source := range sources {
		source = strings.TrimSpace(strings.ToLower(source))
		var meta *AppMetadata
		var err error

		switch source {
		case "github":
			meta, err = f.fetchGitHubMetadata(ctx)
		case "gitlab":
			meta, err = f.fetchGitLabMetadata(ctx)
		case "fdroid":
			meta, err = f.fetchFDroidMetadata(ctx)
		case "playstore":
			meta, err = f.fetchPlayStoreMetadata(ctx)
		default:
			return fmt.Errorf("unknown metadata source: %s", source)
		}

		if err != nil {
			// Log warning but continue with other sources
			fmt.Printf("  Warning: failed to fetch %s metadata: %v\n", source, err)
			continue
		}

		// Merge metadata into config (only fill empty fields)
		f.mergeMetadata(meta)
	}

	return nil
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
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch repo info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error: %d", resp.StatusCode)
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

	if err := json.NewDecoder(resp.Body).Decode(&repoInfo); err != nil {
		return nil, fmt.Errorf("failed to parse repo info: %w", err)
	}

	meta := &AppMetadata{
		Name:        repoInfo.Name,
		Description: repoInfo.Description,
		Website:     repoInfo.Homepage,
		Tags:        repoInfo.Topics,
	}

	if repoInfo.License != nil && repoInfo.License.SPDXID != "" && repoInfo.License.SPDXID != "NOASSERTION" {
		meta.License = repoInfo.License.SPDXID
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
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	// Request raw content
	req.Header.Set("Accept", "application/vnd.github.raw")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch readme: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// fetchGitLabMetadata fetches metadata from GitLab repository.
func (f *MetadataFetcher) fetchGitLabMetadata(ctx context.Context) (*AppMetadata, error) {
	repoPath := config.GetGitLabRepo(f.cfg.Repository)
	if repoPath == "" {
		// Try release_source if repository doesn't have GitLab
		if f.cfg.ReleaseSource != nil {
			repoPath = config.GetGitLabRepo(f.cfg.ReleaseSource.URL)
		}
	}
	if repoPath == "" {
		return nil, fmt.Errorf("no GitLab repository configured")
	}

	// URL-encode the project path for API calls
	encodedPath := strings.ReplaceAll(repoPath, "/", "%2F")

	// Fetch project info
	url := fmt.Sprintf("https://gitlab.com/api/v4/projects/%s", encodedPath)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch project info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitLab API error: %d", resp.StatusCode)
	}

	var projectInfo struct {
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		WebURL        string   `json:"web_url"`
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

	if err := json.NewDecoder(resp.Body).Decode(&projectInfo); err != nil {
		return nil, fmt.Errorf("failed to parse project info: %w", err)
	}

	meta := &AppMetadata{
		Name:        projectInfo.Name,
		Description: projectInfo.Description,
		Website:     projectInfo.WebURL,
		Tags:        projectInfo.Topics,
	}

	if projectInfo.License != nil && projectInfo.License.Key != "" {
		// Convert license key to SPDX-like format (GitLab uses lowercase keys)
		meta.License = strings.ToUpper(projectInfo.License.Key)
	}

	// Try to fetch README for a longer description if the project description is short
	if len(projectInfo.Description) < 100 && projectInfo.DefaultBranch != "" {
		readme, err := f.fetchGitLabReadme(ctx, encodedPath, projectInfo.DefaultBranch)
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
func (f *MetadataFetcher) fetchGitLabReadme(ctx context.Context, encodedPath, branch string) (string, error) {
	// GitLab API: GET /projects/:id/repository/files/:file_path/raw?ref=:branch
	// Try common README filenames
	readmeNames := []string{"README.md", "README", "readme.md", "Readme.md"}

	for _, name := range readmeNames {
		encodedName := strings.ReplaceAll(name, ".", "%2E")
		url := fmt.Sprintf("https://gitlab.com/api/v4/projects/%s/repository/files/%s/raw?ref=%s",
			encodedPath, encodedName, branch)

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			continue
		}

		resp, err := f.client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				continue
			}
			return string(body), nil
		}
	}

	return "", fmt.Errorf("no README found")
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

	// Scrape the F-Droid website for icon and screenshots
	webMeta, err := f.scrapeFDroidWebsite(ctx, packageID)
	if err != nil {
		return nil, err
	}

	// Also fetch metadata from fdroiddata YAML for detailed description, categories, etc.
	metadataURL := fmt.Sprintf("https://gitlab.com/fdroid/fdroiddata/-/raw/master/metadata/%s.yml", packageID)
	fdMeta, yamlErr := f.fetchFDroidYAML(ctx, metadataURL)

	meta := &AppMetadata{
		IconURL:   webMeta.IconURL,
		ImageURLs: webMeta.ImageURLs,
	}

	// Merge YAML metadata if available
	if yamlErr == nil && fdMeta != nil {
		meta.Summary = fdMeta.Summary
		meta.Description = fdMeta.Description
		meta.Website = fdMeta.WebSite
		meta.License = fdMeta.License

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
	IconURL   string
	ImageURLs []string
}

// scrapeFDroidWebsite scrapes the F-Droid package page for icon and screenshots.
func (f *MetadataFetcher) scrapeFDroidWebsite(ctx context.Context, packageID string) (*fdroidWebMeta, error) {
	url := fmt.Sprintf("https://f-droid.org/en/packages/%s/", packageID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch F-Droid page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("package %s not found on F-Droid", packageID)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("F-Droid returned status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
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
	req, err := http.NewRequestWithContext(ctx, "GET", metadataURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata not found (status %d)", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var meta fdroidMetadata
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
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

// mergeMetadata merges fetched metadata into config, only filling empty fields.
func (f *MetadataFetcher) mergeMetadata(meta *AppMetadata) {
	if meta == nil {
		return
	}

	if f.cfg.Name == "" && meta.Name != "" {
		f.cfg.Name = meta.Name
	}
	if f.cfg.Description == "" && meta.Description != "" {
		f.cfg.Description = meta.Description
	}
	if f.cfg.Summary == "" && meta.Summary != "" {
		f.cfg.Summary = meta.Summary
	}
	if f.cfg.Website == "" && meta.Website != "" {
		f.cfg.Website = meta.Website
	}
	if f.cfg.License == "" && meta.License != "" {
		f.cfg.License = meta.License
	}
	if len(f.cfg.Tags) == 0 && len(meta.Tags) > 0 {
		f.cfg.Tags = meta.Tags
	}
	if len(f.cfg.Images) == 0 && len(meta.ImageURLs) > 0 {
		f.cfg.Images = meta.ImageURLs
	}
	if f.cfg.Icon == "" && meta.IconURL != "" {
		f.cfg.Icon = meta.IconURL
	}
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
