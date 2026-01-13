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

	// Release source (for APK fetching) - can be string or ReleaseRepository
	ReleaseRepository *ReleaseRepository `yaml:"-"`
	ReleaseRepoRaw    yaml.Node          `yaml:"release_repository,omitempty"`

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

	// BaseDir is the directory containing the config file (for relative paths).
	// Not parsed from YAML, set by Load().
	BaseDir string `yaml:"-"`
}

// ReleaseRepository represents a release source configuration.
// It can be a simple URL string or a complex web scraping config.
type ReleaseRepository struct {
	// Simple URL mode (GitHub, GitLab, F-Droid)
	URL string

	// Web scraping mode
	IsWebSource bool
	AssetURL    string             `yaml:"asset_url,omitempty"`
	HTML        *HTMLExtractor     `yaml:"html,omitempty"`
	JSON        *JSONExtractor     `yaml:"json,omitempty"`
	Redirect    *RedirectExtractor `yaml:"redirect,omitempty"`
}

// HTMLExtractor configures HTML scraping for version extraction.
type HTMLExtractor struct {
	Selector  string `yaml:"selector"`
	Attribute string `yaml:"attribute,omitempty"` // defaults to "text"
	Pattern   string `yaml:"pattern,omitempty"`   // optional regex
}

// JSONExtractor configures JSON API parsing for version extraction.
type JSONExtractor struct {
	Path    string `yaml:"path"`              // JSONPath expression
	Pattern string `yaml:"pattern,omitempty"` // optional regex
}

// RedirectExtractor configures HTTP redirect header parsing.
type RedirectExtractor struct {
	Header  string `yaml:"header"`  // e.g., "location"
	Pattern string `yaml:"pattern"` // regex to extract version
}

// webReleaseRepository is used for YAML unmarshaling of complex release_repository.
type webReleaseRepository struct {
	URL      string             `yaml:"url"`
	AssetURL string             `yaml:"asset_url"`
	HTML     *HTMLExtractor     `yaml:"html,omitempty"`
	JSON     *JSONExtractor     `yaml:"json,omitempty"`
	Redirect *RedirectExtractor `yaml:"redirect,omitempty"`
}

// SourceType represents the type of source for APK fetching.
type SourceType int

const (
	SourceUnknown SourceType = iota
	SourceLocal
	SourceGitHub
	SourceGitLab
	SourceFDroid
	SourceWeb
)

func (s SourceType) String() string {
	switch s {
	case SourceLocal:
		return "local"
	case SourceGitHub:
		return "github"
	case SourceGitLab:
		return "gitlab"
	case SourceFDroid:
		return "fdroid"
	case SourceWeb:
		return "web"
	default:
		return "unknown"
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

	// Parse release_repository which can be string or map
	if err := cfg.parseReleaseRepository(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// parseReleaseRepository handles the polymorphic release_repository field.
func (c *Config) parseReleaseRepository() error {
	if c.ReleaseRepoRaw.Kind == 0 {
		return nil // Not specified
	}

	switch c.ReleaseRepoRaw.Kind {
	case yaml.ScalarNode:
		// Simple string URL
		var url string
		if err := c.ReleaseRepoRaw.Decode(&url); err != nil {
			return fmt.Errorf("failed to parse release_repository URL: %w", err)
		}
		c.ReleaseRepository = &ReleaseRepository{URL: url}

	case yaml.MappingNode:
		// Complex web scraping config
		var web webReleaseRepository
		if err := c.ReleaseRepoRaw.Decode(&web); err != nil {
			return fmt.Errorf("failed to parse release_repository config: %w", err)
		}

		// Validate: only one extractor allowed
		extractorCount := 0
		if web.HTML != nil {
			extractorCount++
		}
		if web.JSON != nil {
			extractorCount++
		}
		if web.Redirect != nil {
			extractorCount++
		}
		if extractorCount > 1 {
			return fmt.Errorf("release_repository: only one of html, json, or redirect can be specified")
		}

		c.ReleaseRepository = &ReleaseRepository{
			URL:         web.URL,
			IsWebSource: true,
			AssetURL:    web.AssetURL,
			HTML:        web.HTML,
			JSON:        web.JSON,
			Redirect:    web.Redirect,
		}

	default:
		return fmt.Errorf("release_repository must be a string or map")
	}

	return nil
}

// Validate checks if the config has required fields and valid URLs.
func (c *Config) Validate() error {
	if c.Local == "" && c.Repository == "" && c.ReleaseRepository == nil {
		return fmt.Errorf("no source specified: need 'local', 'repository', or 'release_repository'")
	}

	// Validate repository URL if provided
	if c.Repository != "" {
		if err := ValidateURL(c.Repository); err != nil {
			return fmt.Errorf("invalid repository URL: %w", err)
		}
	}

	// Validate release_repository URL if it's a simple string URL
	if c.ReleaseRepository != nil && !c.ReleaseRepository.IsWebSource && c.ReleaseRepository.URL != "" {
		if err := ValidateURL(c.ReleaseRepository.URL); err != nil {
			return fmt.Errorf("invalid release_repository URL: %w", err)
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
// Follows precedence: local > release_repository > repository
func (c *Config) GetSourceType() SourceType {
	// Local always takes precedence
	if c.Local != "" {
		return SourceLocal
	}

	// Check release_repository
	if c.ReleaseRepository != nil {
		if c.ReleaseRepository.IsWebSource {
			return SourceWeb
		}
		return DetectSourceType(c.ReleaseRepository.URL)
	}

	// Fallback to repository
	return DetectSourceType(c.Repository)
}

// GetAPKSourceURL returns the URL to fetch APKs from.
func (c *Config) GetAPKSourceURL() string {
	if c.ReleaseRepository != nil {
		return c.ReleaseRepository.URL
	}
	return c.Repository
}

// DetectSourceType detects the source type from a URL.
func DetectSourceType(url string) SourceType {
	if url == "" {
		return SourceUnknown
	}

	lower := strings.ToLower(url)

	if strings.Contains(lower, "github.com") {
		return SourceGitHub
	}
	if strings.Contains(lower, "gitlab.com") {
		return SourceGitLab
	}
	if strings.Contains(lower, "f-droid.org") {
		return SourceFDroid
	}

	return SourceUnknown
}

// GetFDroidPackageID extracts the package ID from an F-Droid URL.
func GetFDroidPackageID(url string) string {
	// Handle: https://f-droid.org/packages/com.example.app
	// Handle: https://f-droid.org/en/packages/com.example.app
	lower := strings.ToLower(url)
	if idx := strings.Index(lower, "/packages/"); idx != -1 {
		path := url[idx+len("/packages/"):]
		// Remove trailing slash if present
		path = strings.TrimSuffix(path, "/")
		return path
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
func GetGitLabRepo(url string) string {
	// Handle: https://gitlab.com/owner/repo
	lower := strings.ToLower(url)
	if idx := strings.Index(lower, "gitlab.com/"); idx != -1 {
		path := url[idx+len("gitlab.com/"):]
		// Remove trailing parts
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return ""
}
