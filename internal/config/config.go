// Package config handles YAML configuration parsing and validation.
package config

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the zapstore.yaml configuration file.
type Config struct {
	// Source code repository (for display)
	Repository string `yaml:"repository,omitempty"`

	// Release source (for APK fetching) - can be string or ReleaseSource
	ReleaseSource    *ReleaseSource `yaml:"-"`
	ReleaseSourceRaw yaml.Node      `yaml:"release_source,omitempty"`

	// Local APK override (takes precedence over remote sources)
	Local string `yaml:"local,omitempty"`

	// Asset matching (optional, overrides auto-detection)
	Match string `yaml:"match,omitempty"`

	// App metadata (all optional, overrides APK-extracted values)
	Name        string   `yaml:"name,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Summary     string   `yaml:"summary,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`
	License     string   `yaml:"license,omitempty"`
	Website     string   `yaml:"website,omitempty"`

	// Media (optional)
	Icon   string   `yaml:"icon,omitempty"`
	Images []string `yaml:"images,omitempty"`

	// Changelog file path (optional, if not set uses remote release notes)
	Changelog string `yaml:"changelog,omitempty"`

	// MetadataSources specifies where to fetch additional metadata from.
	// Supported values: "github", "gitlab", "fdroid", "playstore"
	// If not set, defaults are inferred from release_source or repository.
	MetadataSources []string `yaml:"metadata_sources,omitempty"`

	// BaseDir is the directory containing the config file (for relative paths).
	// Not parsed from YAML, set by Load().
	BaseDir string `yaml:"-"`
}

// ReleaseSource represents a release source configuration.
// It can be a simple URL string or a web source config with asset_url pattern.
type ReleaseSource struct {
	// Simple URL mode (GitHub, GitLab, Gitea, F-Droid)
	URL string

	// Explicit source type (optional, overrides auto-detection)
	// Valid values: "github", "gitlab", "gitea", "fdroid"
	// Useful for self-hosted GitLab/Gitea/Forgejo instances
	Type string

	// Web source mode - AssetURL is a regex pattern to find APK URLs
	// in the response (headers, body). Version is extracted from downloaded APK.
	IsWebSource bool
	AssetURL    string `yaml:"asset_url,omitempty"` // Regex pattern to match APK URL
}

// webReleaseSource is used for YAML unmarshaling of complex release_source.
type webReleaseSource struct {
	URL      string `yaml:"url"`
	Type     string `yaml:"type,omitempty"`      // Explicit source type override
	AssetURL string `yaml:"asset_url,omitempty"` // Regex pattern for APK URL
}

// SourceType represents the type of source for APK fetching.
type SourceType int

const (
	SourceUnknown SourceType = iota
	SourceLocal
	SourceGitHub
	SourceGitLab
	SourceGitea // Covers Codeberg, Forgejo, and self-hosted Gitea instances
	SourceFDroid
	SourceWeb
	SourcePlayStore
)

func (s SourceType) String() string {
	switch s {
	case SourceLocal:
		return "local"
	case SourceGitHub:
		return "github"
	case SourceGitLab:
		return "gitlab"
	case SourceGitea:
		return "gitea"
	case SourceFDroid:
		return "fdroid"
	case SourceWeb:
		return "web"
	case SourcePlayStore:
		return "playstore"
	default:
		return "unknown"
	}
}

// ParseSourceType converts a string to a SourceType.
// Returns SourceUnknown if the string is not recognized.
func ParseSourceType(s string) SourceType {
	switch strings.ToLower(s) {
	case "local":
		return SourceLocal
	case "github":
		return SourceGitHub
	case "gitlab":
		return SourceGitLab
	case "gitea":
		return SourceGitea
	case "fdroid":
		return SourceFDroid
	case "web":
		return SourceWeb
	case "playstore":
		return SourcePlayStore
	default:
		return SourceUnknown
	}
}

// Load reads and parses a config file.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer f.Close()

	cfg, err := Parse(f)
	if err != nil {
		return nil, err
	}

	// Set base directory for relative path resolution
	absPath, err := filepath.Abs(path)
	if err == nil {
		cfg.BaseDir = filepath.Dir(absPath)
	}

	return cfg, nil
}

// Parse reads and parses config from a reader.
func Parse(r io.Reader) (*Config, error) {
	var cfg Config
	decoder := yaml.NewDecoder(r)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Parse release_source which can be string or map
	if err := cfg.parseReleaseSource(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// parseReleaseSource handles the polymorphic release_source field.
func (c *Config) parseReleaseSource() error {
	if c.ReleaseSourceRaw.Kind == 0 {
		return nil // Not specified
	}

	switch c.ReleaseSourceRaw.Kind {
	case yaml.ScalarNode:
		// Simple string URL
		var url string
		if err := c.ReleaseSourceRaw.Decode(&url); err != nil {
			return fmt.Errorf("failed to parse release_source URL: %w", err)
		}
		c.ReleaseSource = &ReleaseSource{URL: url}

	case yaml.MappingNode:
		// Complex release source config
		var web webReleaseSource
		if err := c.ReleaseSourceRaw.Decode(&web); err != nil {
			return fmt.Errorf("failed to parse release_source config: %w", err)
		}

		// Web source mode requires asset_url (regex pattern for APK URL)
		isWebSource := web.AssetURL != ""

		c.ReleaseSource = &ReleaseSource{
			URL:         web.URL,
			Type:        web.Type,
			IsWebSource: isWebSource,
			AssetURL:    web.AssetURL,
		}

	default:
		return fmt.Errorf("release_source must be a string or map")
	}

	return nil
}

// Validate checks if the config has required fields and valid URLs.
func (c *Config) Validate() error {
	if c.Local == "" && c.Repository == "" && c.ReleaseSource == nil {
		return fmt.Errorf("no source specified: need 'local', 'repository', or 'release_source'")
	}

	// Validate repository URL if provided
	if c.Repository != "" {
		if err := ValidateURL(c.Repository); err != nil {
			return fmt.Errorf("invalid repository URL: %w", err)
		}
	}

	// Validate release_source URL if it's a simple string URL
	if c.ReleaseSource != nil && !c.ReleaseSource.IsWebSource && c.ReleaseSource.URL != "" {
		if err := ValidateURL(c.ReleaseSource.URL); err != nil {
			return fmt.Errorf("invalid release_source URL: %w", err)
		}
	}

	return nil
}

// ValidateURL checks if a string is a valid URL with http/https scheme.
func ValidateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("malformed URL: %w", err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL must have http or https scheme, got %q", parsed.Scheme)
	}

	if parsed.Host == "" {
		return fmt.Errorf("URL must have a host")
	}

	// Host must be localhost or contain a dot (domain.tld)
	host := parsed.Hostname()
	if host != "localhost" && !strings.Contains(host, ".") {
		return fmt.Errorf("invalid host %q: must be a valid domain (e.g., github.com/user/repo)", host)
	}

	return nil
}

// GetSourceType returns the detected source type for APK fetching.
// Follows precedence: local > release_source > repository
// If release_source has an explicit type, it overrides auto-detection.
func (c *Config) GetSourceType() SourceType {
	// Local always takes precedence
	if c.Local != "" {
		return SourceLocal
	}

	// Check release_source
	if c.ReleaseSource != nil {
		// Web scraping mode (has extractors or asset_url)
		if c.ReleaseSource.IsWebSource {
			return SourceWeb
		}

		// Explicit type override (for self-hosted GitLab/Gitea/Forgejo)
		if c.ReleaseSource.Type != "" {
			if t := ParseSourceType(c.ReleaseSource.Type); t != SourceUnknown {
				return t
			}
		}

		return DetectSourceType(c.ReleaseSource.URL)
	}

	// Fallback to repository
	return DetectSourceType(c.Repository)
}

// GetAPKSourceURL returns the URL to fetch APKs from.
func (c *Config) GetAPKSourceURL() string {
	if c.ReleaseSource != nil {
		return c.ReleaseSource.URL
	}
	return c.Repository
}

// DetectSourceType detects the source type from a URL.
func DetectSourceType(rawURL string) SourceType {
	if rawURL == "" {
		return SourceUnknown
	}

	lower := strings.ToLower(rawURL)

	if strings.Contains(lower, "github.com") {
		return SourceGitHub
	}
	// GitLab: gitlab.com and self-hosted instances with "gitlab" in the domain
	if strings.Contains(lower, "gitlab.com") || containsGitLab(lower) {
		return SourceGitLab
	}
	if strings.Contains(lower, "codeberg.org") {
		return SourceGitea
	}
	// F-Droid compatible repositories
	if strings.Contains(lower, "f-droid.org") ||
		strings.Contains(lower, "apt.izzysoft.de") ||
		strings.Contains(lower, "izzysoft.de") {
		return SourceFDroid
	}
	if strings.Contains(lower, "play.google.com") {
		return SourcePlayStore
	}

	return SourceUnknown
}

// containsGitLab checks if a URL's domain contains "gitlab" (for self-hosted instances).
func containsGitLab(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return strings.Contains(host, "gitlab")
}

// FDroidRepoInfo contains information about an F-Droid compatible repository.
type FDroidRepoInfo struct {
	RepoURL     string // Base repo URL (e.g., "https://f-droid.org/repo")
	IndexURL    string // Index JSON URL (e.g., "https://f-droid.org/repo/index-v1.json")
	PackageID   string // Package identifier (e.g., "com.example.app")
	MetadataURL string // Metadata YAML URL (empty if not available)
}

// GetFDroidRepoInfo extracts repository info from an F-Droid compatible URL.
// Supports: f-droid.org, apt.izzysoft.de (IzzyOnDroid), and other F-Droid repos.
func GetFDroidRepoInfo(rawURL string) *FDroidRepoInfo {
	lower := strings.ToLower(rawURL)

	// F-Droid official repo
	// Handle: https://f-droid.org/packages/com.example.app
	// Handle: https://f-droid.org/en/packages/com.example.app
	if strings.Contains(lower, "f-droid.org") {
		if idx := strings.Index(lower, "/packages/"); idx != -1 {
			packageID := rawURL[idx+len("/packages/"):]
			packageID = strings.TrimSuffix(packageID, "/")
			return &FDroidRepoInfo{
				RepoURL:     "https://f-droid.org/repo",
				IndexURL:    "https://f-droid.org/repo/index-v1.json",
				PackageID:   packageID,
				MetadataURL: fmt.Sprintf("https://gitlab.com/fdroid/fdroiddata/-/raw/master/metadata/%s.yml", packageID),
			}
		}
	}

	// IzzyOnDroid repo
	// Handle: https://apt.izzysoft.de/fdroid/index/apk/com.example.app
	if strings.Contains(lower, "apt.izzysoft.de") || strings.Contains(lower, "izzysoft.de") {
		if idx := strings.Index(lower, "/apk/"); idx != -1 {
			packageID := rawURL[idx+len("/apk/"):]
			packageID = strings.TrimSuffix(packageID, "/")
			return &FDroidRepoInfo{
				RepoURL:   "https://apt.izzysoft.de/fdroid/repo",
				IndexURL:  "https://apt.izzysoft.de/fdroid/repo/index-v1.json",
				PackageID: packageID,
				// IzzyOnDroid stores metadata in their own GitLab repo
				MetadataURL: fmt.Sprintf("https://gitlab.com/AW-HB/IzzyOnDroid-fdroid-index/-/raw/main/source/metadata/%s.yml", packageID),
			}
		}
	}

	return nil
}

// GetFDroidPackageID extracts the package ID from an F-Droid URL.
// Deprecated: Use GetFDroidRepoInfo instead for full repo information.
func GetFDroidPackageID(rawURL string) string {
	info := GetFDroidRepoInfo(rawURL)
	if info != nil {
		return info.PackageID
	}
	return ""
}

// GetGitHubRepo extracts owner/repo from a GitHub URL.
func GetGitHubRepo(url string) string {
	// Handle: https://github.com/owner/repo
	lower := strings.ToLower(url)
	if idx := strings.Index(lower, "github.com/"); idx != -1 {
		path := url[idx+len("github.com/"):]
		// Remove trailing parts like /releases, etc.
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return ""
}

// GetGitLabRepo extracts owner/repo from a GitLab URL.
func GetGitLabRepo(rawURL string) string {
	// Handle: https://gitlab.com/owner/repo
	lower := strings.ToLower(rawURL)
	if idx := strings.Index(lower, "gitlab.com/"); idx != -1 {
		path := rawURL[idx+len("gitlab.com/"):]
		// Remove trailing parts
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return ""
}

// GetGiteaRepo extracts owner/repo and base URL from a Gitea-compatible URL.
// Returns baseURL (e.g., "https://codeberg.org") and repoPath (e.g., "owner/repo").
// Supports Codeberg and other Gitea/Forgejo instances.
func GetGiteaRepo(rawURL string) (baseURL, repoPath string) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", ""
	}

	// Extract base URL
	baseURL = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)

	// Extract repo path (first two segments after host)
	path := strings.TrimPrefix(parsed.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) >= 2 {
		repoPath = parts[0] + "/" + parts[1]
	}

	return baseURL, repoPath
}

// GetGitLabRepoWithBase extracts owner/repo and base URL from a GitLab URL.
// Returns baseURL (e.g., "https://gitlab.com") and repoPath (e.g., "owner/repo").
// Supports self-hosted GitLab instances.
func GetGitLabRepoWithBase(rawURL string) (baseURL, repoPath string) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", ""
	}

	// Extract base URL
	baseURL = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)

	// Extract repo path (first two segments after host)
	path := strings.TrimPrefix(parsed.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) >= 2 {
		repoPath = parts[0] + "/" + parts[1]
	}

	return baseURL, repoPath
}
