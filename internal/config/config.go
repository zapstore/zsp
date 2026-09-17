// Package config handles YAML configuration parsing and validation.
package config

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"gopkg.in/yaml.v3"
)

// Config represents the zapstore.yaml configuration file.
type Config struct {
	// Source code repository (for display) - can be URL or NIP-34 naddr
	Repository string `yaml:"repository,omitempty"`

	// NIP34Repo is the parsed NIP-34 repository pointer (set when repository is an naddr)
	NIP34Repo *NIP34RepoPointer `yaml:"-"`

	// Release source (for APK fetching) - can be string or ReleaseSource
	// For local files, use a local path: release_source: ./build/outputs/apk/release/app-release.apk
	ReleaseSource    *ReleaseSource `yaml:"-"`
	ReleaseSourceRaw yaml.Node      `yaml:"release_source,omitempty"`

	// Release filtering (optional, filters releases by tag name)
	ReleaseFilter string `yaml:"release_filter,omitempty"`

	// Asset matching (optional, overrides auto-detection)
	Match string `yaml:"match,omitempty"`

	// PrereleaseChannel selects prerelease source releases when non-empty.
	PrereleaseChannel string `yaml:"prerelease_channel,omitempty"`

	// Channel identifies the NIP-82 channel for published releases.
	Channel string `yaml:"channel,omitempty"`

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

	// Release notes: local file path or URL (optional, if not set uses remote release notes)
	// If URL, contents are fetched. If markdown follows Keep a Changelog format,
	// only the section for this release is extracted.
	ReleaseNotes string `yaml:"release_notes,omitempty"`

	// Changelog is deprecated and has no effect on parsing or validation; use
	// ReleaseNotes instead. Kept only for legacy callers outside this package.
	Changelog string `yaml:"changelog,omitempty"`

	// SupportedNIPs lists Nostr NIPs supported by this application
	SupportedNIPs []string `yaml:"supported_nips,omitempty"`

	// MinAllowedVersion is the minimum allowed version string
	MinAllowedVersion string `yaml:"min_allowed_version,omitempty"`

	// MinAllowedVersionCode is the minimum allowed version code (Android)
	MinAllowedVersionCode int64 `yaml:"min_allowed_version_code,omitempty"`

	// MetadataSources specifies where to fetch additional metadata from.
	// Supported values: "fastlane", "github", "gitlab", "fdroid", "playstore".
	// If not set, GitHub and GitLab repositories use Fastlane metadata first,
	// then fall back to their native repository metadata.
	MetadataSources []string `yaml:"metadata_sources,omitempty"`

	// Pubkey is deprecated and has no effect on parsing, validation, or
	// loading: Load never resolves or checks it against the process signer.
	// Kept only for legacy callers outside this package (e.g. the wizard).
	Pubkey string `yaml:"pubkey,omitempty"`

	// Communities is deprecated and has no effect on parsing or validation.
	// Kept only for legacy callers outside this package.
	Communities []string `yaml:"communities,omitempty"`

	// BaseDir is the directory containing the config file (for relative paths).
	// Not parsed from YAML, set by Load().
	BaseDir string `yaml:"-"`
}

// NIP34RepoPointer represents a parsed NIP-34 repository naddr.
type NIP34RepoPointer struct {
	Pubkey     string   // Repository owner's pubkey (hex)
	Identifier string   // Repository identifier (d tag)
	Relays     []string // Relay hints
}

// ReleaseSource represents a release source configuration.
// It can be a simple URL string, a local file path, or a web source config with version extractors.
type ReleaseSource struct {
	// Simple URL mode (GitHub, GitLab, Gitea, F-Droid)
	URL string

	// LocalPath is set when the release source is a local APK, directory, or glob pattern.
	// When set, URL is empty and this takes precedence.
	LocalPath string

	// Explicit source type (optional, overrides auto-detection)
	// Valid values: "github", "gitlab", "gitea", "fdroid", "local"
	// Useful for self-hosted GitLab/Gitea/Forgejo instances
	Type string

	// Web source mode - version extractor and asset URL template
	IsWebSource bool

	// Version extractor (unified structure for HTML, JSON, and header modes)
	// Mode is determined by which field is set:
	//   - selector: HTML mode (CSS selector)
	//   - path: JSON mode (JSONPath expression)
	//   - header: Header mode (HTTP redirect header)
	Version *VersionExtractor

	// AssetURL is the download URL template for the APK.
	// Can contain {version} placeholder which is replaced with the extracted version.
	// If no version extractor is set, this is the direct download URL and
	// HTTP caching (ETag/Last-Modified) is used to detect changes.
	// Mutually exclusive with Asset.
	AssetURL string

	// Asset extracts the download URL dynamically from a web page, JSON API, or HTTP header.
	// Works like Version but extracts the download URL instead of the version string.
	// If Version is also set, it's used for change detection caching.
	// If Version is not set, version is extracted from the downloaded APK.
	// Mutually exclusive with AssetURL.
	Asset *VersionExtractor
}

// IsLocal returns true if this release source is a local file path.
func (r *ReleaseSource) IsLocal() bool {
	return r != nil && r.LocalPath != ""
}

// VersionExtractor extracts version from a URL using one of three modes:
//   - HTML mode: Uses CSS selector to find element, extracts attribute value
//   - JSON mode: Uses JSONPath to extract value from JSON response
//   - Header mode: Extracts value from HTTP redirect header
//
// Mode is determined by which field is set (selector, path, or header).
type VersionExtractor struct {
	URL string `yaml:"url"` // URL to fetch version from (required)

	// HTML mode fields
	Selector  string `yaml:"selector,omitempty"`  // CSS selector to find the element
	Attribute string `yaml:"attribute,omitempty"` // Attribute to extract (e.g., "href", "data-version"); omit for text content

	// JSON mode field
	Path string `yaml:"path,omitempty"` // JSONPath expression (e.g., "$.tag_name")

	// Header mode field
	Header string `yaml:"header,omitempty"` // Header to check (usually "location")

	// Pattern to extract version (regex with capture group)
	// If omitted, the entire extracted value is used as the version
	Match string `yaml:"match,omitempty"` // Regex with capture group for version (e.g., "v([0-9.]+)")
}

// webReleaseSource is used for YAML unmarshaling of complex release_source.
type webReleaseSource struct {
	URL      string            `yaml:"url"`
	Type     string            `yaml:"type,omitempty"`
	AssetURL string            `yaml:"asset_url,omitempty"`
	Version  *VersionExtractor `yaml:"version,omitempty"`
	Asset    *VersionExtractor `yaml:"asset,omitempty"`
}

// HasVersionExtractor returns true if a version extractor is configured.
func (r *ReleaseSource) HasVersionExtractor() bool {
	return r.Version != nil
}

// HasAssetExtractor returns true if an asset URL extractor is configured.
func (r *ReleaseSource) HasAssetExtractor() bool {
	return r.Asset != nil
}

// HasVersionPlaceholder returns true if AssetURL contains {version} placeholder.
func (r *ReleaseSource) HasVersionPlaceholder() bool {
	return strings.Contains(r.AssetURL, "{version}")
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
// Loading never depends on the process signer: it performs no SIGN_WITH
// resolution and never fails based on the resolved signing identity.
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
	cfg.CanonicalizeForgeURLs()

	// Parse repository if it's an naddr
	if err := cfg.parseRepository(); err != nil {
		return nil, err
	}

	// changelog, pubkey, and communities are deprecated and unknown fields are
	// ignored: they are decoded (when the struct still declares them for
	// legacy callers) but have no effect on parsing, validation, or behavior.

	return &cfg, nil
}

// parseRepository parses the repository field, which can be a URL or NIP-34 naddr.
func (c *Config) parseRepository() error {
	if c.Repository == "" {
		return nil
	}

	// Check if it's an naddr
	if strings.HasPrefix(c.Repository, "naddr1") {
		pointer, err := ParseNaddr(c.Repository)
		if err != nil {
			return fmt.Errorf("invalid repository naddr: %w", err)
		}
		c.NIP34Repo = pointer
	}

	return nil
}

// CanonicalizeForgeURLs reduces recognized forge repository and release URLs
// to their API repository roots. Direct APK and web release sources retain
// their explicit asset URL semantics.
func (c *Config) CanonicalizeForgeURLs() {
	c.Repository, _ = CanonicalForgeRepositoryURL(c.Repository, "")
	if c.ReleaseSource == nil || c.ReleaseSource.URL == "" || c.ReleaseSource.IsWebSource {
		return
	}
	c.ReleaseSource.URL, _ = CanonicalForgeRepositoryURL(c.ReleaseSource.URL, c.ReleaseSource.Type)
}

// CanonicalForgeRepositoryURL reduces a recognized forge URL to its
// repository root. The sourceType hint supports explicitly configured
// self-hosted GitLab and Gitea-compatible forges.
func CanonicalForgeRepositoryURL(source, sourceType string) (string, bool) {
	detected := DetectSourceType(source)
	if hint := ParseSourceType(sourceType); hint != SourceUnknown {
		detected = hint
	}
	switch detected {
	case SourceGitHub:
		if repository := GetGitHubRepo(source); repository != "" {
			return "https://github.com/" + strings.ToLower(repository), true
		}
	case SourceGitLab:
		baseURL, repository := GetGitLabRepoWithBase(source)
		if repository != "" {
			return baseURL + "/" + repository, true
		}
	case SourceGitea:
		baseURL, repository := GetGiteaRepo(source)
		if repository != "" {
			return baseURL + "/" + strings.TrimSuffix(repository, ".git"), true
		}
	}
	return source, false
}

// ValidateForgeRepositoryURL verifies that a recognized forge URL identifies
// a repository. URLs for unrecognized hosts are validated as general URLs.
func ValidateForgeRepositoryURL(source, sourceType string) error {
	detected := DetectSourceType(source)
	if hint := ParseSourceType(sourceType); hint != SourceUnknown {
		detected = hint
	}
	switch detected {
	case SourceGitHub, SourceGitLab, SourceGitea:
		if _, ok := CanonicalForgeRepositoryURL(source, sourceType); !ok {
			return fmt.Errorf("must include an owner and repository path")
		}
	}
	return nil
}

// ParseNaddr parses a NIP-34 repository naddr and validates it.
// Returns the parsed pointer or an error if invalid.
func ParseNaddr(naddrStr string) (*NIP34RepoPointer, error) {
	prefix, data, err := nip19.Decode(naddrStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode naddr: %w", err)
	}

	if prefix != "naddr" {
		return nil, fmt.Errorf("expected naddr prefix, got %s", prefix)
	}

	// nip19.Decode returns nostr.EntityPointer for naddr
	ep, ok := data.(nostr.EntityPointer)
	if !ok {
		return nil, fmt.Errorf("unexpected naddr data type: %T", data)
	}

	// Validate kind 30617 (NIP-34 repository)
	if ep.Kind != 30617 {
		return nil, fmt.Errorf("expected kind 30617 (NIP-34 repository), got %d", ep.Kind)
	}

	return &NIP34RepoPointer{
		Pubkey:     ep.PublicKey,
		Identifier: ep.Identifier,
		Relays:     ep.Relays,
	}, nil
}

// parseReleaseSource handles the polymorphic release_source field.
func (c *Config) parseReleaseSource() error {
	if c.ReleaseSourceRaw.Kind == 0 {
		return nil // Not specified
	}

	switch c.ReleaseSourceRaw.Kind {
	case yaml.ScalarNode:
		// Simple string - could be URL or local path
		var value string
		if err := c.ReleaseSourceRaw.Decode(&value); err != nil {
			return fmt.Errorf("failed to parse release_source: %w", err)
		}

		// Check if it's a local path (starts with . or / or contains glob patterns without http)
		if isLocalPath(value) {
			c.ReleaseSource = &ReleaseSource{LocalPath: value}
		} else if DetectSourceType(value) == SourceUnknown {
			// If the URL is from an unknown source (not GitHub, GitLab, etc.),
			// treat it as a web source with the URL as the asset_url.
			// This allows: release_source: https://example.com/app.apk
			// to be shorthand for: release_source: { asset_url: https://example.com/app.apk }
			// Uses HTTP caching (ETag/Last-Modified) to detect changes.
			c.ReleaseSource = &ReleaseSource{
				IsWebSource: true,
				AssetURL:    value,
			}
		} else {
			c.ReleaseSource = &ReleaseSource{URL: value}
		}

	case yaml.MappingNode:
		// Complex release source config
		var web webReleaseSource
		if err := c.ReleaseSourceRaw.Decode(&web); err != nil {
			return fmt.Errorf("failed to parse release_source config: %w", err)
		}

		// Any extractor or asset setting selects structured web mode. This also
		// makes an otherwise-unused version extractor fail validation unless an
		// asset URL or asset extractor is provided.
		isWebSource := web.AssetURL != "" || web.Version != nil || web.Asset != nil

		c.ReleaseSource = &ReleaseSource{
			URL:         web.URL,
			Type:        web.Type,
			IsWebSource: isWebSource,
			AssetURL:    web.AssetURL,
			Version:     web.Version,
			Asset:       web.Asset,
		}
		if ParseSourceType(web.Type) == SourceLocal {
			c.ReleaseSource.LocalPath = web.URL
			c.ReleaseSource.URL = ""
		}

	default:
		return fmt.Errorf("release_source must be a string or map")
	}

	return nil
}

// isLocalPath returns true if the value looks like a local file path.
// Local paths start with . or / or are relative paths without URL scheme.
func isLocalPath(value string) bool {
	if value == "" {
		return false
	}
	// Starts with ./ or ../ or /
	if value == "." || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") {
		return true
	}
	// Contains glob patterns but no URL scheme
	if (strings.Contains(value, "*") || strings.Contains(value, "?")) && !strings.Contains(value, "://") {
		return true
	}
	// Ends with .apk and has no URL scheme (simple filename or relative path)
	if strings.HasSuffix(strings.ToLower(value), ".apk") && !strings.Contains(value, "://") {
		return true
	}
	return false
}

// Validate checks if the config has required fields and valid URLs.
func (c *Config) Validate() error {
	if c.Repository == "" && c.ReleaseSource == nil {
		return fmt.Errorf("no source specified: need 'repository' or 'release_source'")
	}

	// Validate repository URL if provided (skip if it's an naddr)
	if c.Repository != "" && c.NIP34Repo == nil {
		if err := ValidateURL(c.Repository); err != nil {
			return fmt.Errorf("invalid repository URL: %w", err)
		}
		if err := ValidateForgeRepositoryURL(c.Repository, ""); err != nil {
			return fmt.Errorf("invalid repository URL: %w", err)
		}
	}

	// Validate release_source URL if it's a simple string URL
	if c.ReleaseSource != nil && !c.ReleaseSource.IsWebSource && c.ReleaseSource.URL != "" {
		if err := ValidateURL(c.ReleaseSource.URL); err != nil {
			return fmt.Errorf("invalid release_source URL: %w", err)
		}
		if err := ValidateForgeRepositoryURL(c.ReleaseSource.URL, c.ReleaseSource.Type); err != nil {
			return fmt.Errorf("invalid release_source URL: %w", err)
		}
	}
	if c.ReleaseSource != nil && c.ReleaseSource.Type != "" {
		sourceType := ParseSourceType(c.ReleaseSource.Type)
		if sourceType == SourceUnknown {
			return fmt.Errorf("unsupported release_source type %q", c.ReleaseSource.Type)
		}
		if sourceType == SourceFDroid && GetFDroidRepoInfo(c.ReleaseSource.URL) == nil {
			return fmt.Errorf("unsupported F-Droid release_source URL")
		}
	}

	// Validate web source version extractors
	if c.ReleaseSource != nil && c.ReleaseSource.IsWebSource {
		if err := c.ReleaseSource.Validate(); err != nil {
			return fmt.Errorf("invalid release_source: %w", err)
		}
	}
	if c.Website != "" {
		if err := ValidateURL(c.Website); err != nil {
			return fmt.Errorf("invalid website URL: %w", err)
		}
	}
	for _, mediaURL := range append([]string{c.Icon}, c.Images...) {
		if strings.Contains(mediaURL, "://") {
			if err := ValidateURL(mediaURL); err != nil {
				return fmt.Errorf("invalid media URL: %w", err)
			}
		}
	}
	if strings.Contains(c.ReleaseNotes, "://") {
		if err := ValidateURL(c.ReleaseNotes); err != nil {
			return fmt.Errorf("invalid release_notes URL: %w", err)
		}
	}

	// Validate release_filter regex pattern
	if c.ReleaseFilter != "" {
		if _, err := regexp.Compile(c.ReleaseFilter); err != nil {
			return fmt.Errorf("invalid release_filter pattern %q: %w", c.ReleaseFilter, err)
		}
	}

	// Validate match regex pattern
	if c.Match != "" {
		if _, err := regexp.Compile(c.Match); err != nil {
			return fmt.Errorf("invalid match pattern %q: %w", c.Match, err)
		}
	}
	for _, source := range c.MetadataSources {
		switch strings.ToLower(strings.TrimSpace(source)) {
		case "fastlane", "github", "gitlab", "gitea", "fdroid", "playstore":
		default:
			return fmt.Errorf("unsupported metadata source %q", source)
		}
	}

	return nil
}

// Validate checks if the ReleaseSource configuration is valid.
func (r *ReleaseSource) Validate() error {
	if !r.IsWebSource {
		return nil
	}

	// asset_url and asset are mutually exclusive
	if r.AssetURL != "" && r.Asset != nil {
		return fmt.Errorf("asset_url and asset are mutually exclusive: use asset_url for static/template URLs or asset for dynamic URL extraction")
	}

	// Must have either asset_url or asset
	if r.AssetURL == "" && r.Asset == nil {
		return fmt.Errorf("must specify either asset_url or asset")
	}

	// Check if asset_url has {version} placeholder but no version extractor
	if r.HasVersionPlaceholder() && r.Version == nil {
		return fmt.Errorf("asset_url contains {version} placeholder but no version extractor is configured")
	}

	// Validate asset_url if provided (check HTTPS requirement)
	// Note: asset_url may contain {version} placeholder, so we validate a sample URL
	if r.AssetURL != "" {
		testURL := strings.ReplaceAll(r.AssetURL, "{version}", "1.0.0")
		if err := ValidateURL(testURL); err != nil {
			return fmt.Errorf("invalid asset_url: %w", err)
		}
	}

	// Validate version extractor if present
	if r.Version != nil {
		if err := r.Version.Validate(); err != nil {
			return fmt.Errorf("version: %w", err)
		}
	}

	// Validate asset extractor if present
	if r.Asset != nil {
		if err := r.Asset.Validate(); err != nil {
			return fmt.Errorf("asset: %w", err)
		}
	}

	return nil
}

// Validate checks if the VersionExtractor configuration is valid.
func (v *VersionExtractor) Validate() error {
	if v.URL == "" {
		return fmt.Errorf("url is required")
	}

	// Validate URL format and security requirements (HTTPS required)
	if err := ValidateURL(v.URL); err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}

	// Determine mode and validate accordingly
	mode := v.Mode()
	if mode == "" {
		return fmt.Errorf("must specify one of: selector (HTML), path (JSON), or header")
	}

	// match is optional for all modes - defaults to (.*) if omitted
	switch mode {
	case "html":
		// attribute is optional - defaults to text extraction when omitted
	case "json":
		// path extracts the value directly
		if v.Attribute != "" {
			return fmt.Errorf("attribute is only valid with selector")
		}
	case "header":
		// header value is used directly
		if v.Attribute != "" {
			return fmt.Errorf("attribute is only valid with selector")
		}
	}

	// Validate match pattern if provided
	if v.Match != "" {
		if _, err := regexp.Compile(v.Match); err != nil {
			return fmt.Errorf("invalid match pattern: %w", err)
		}
	}

	return nil
}

// Mode returns the extraction mode based on which field is set.
// Returns "html", "json", "header", or "" if none/multiple set.
func (v *VersionExtractor) Mode() string {
	count := 0
	mode := ""

	if v.Selector != "" {
		count++
		mode = "html"
	}
	if v.Path != "" {
		count++
		mode = "json"
	}
	if v.Header != "" {
		count++
		mode = "header"
	}

	if count != 1 {
		return ""
	}
	return mode
}

// ValidateURL checks if a string is a valid URL with http/https scheme.
// For security, HTTPS is required for all remote URLs except localhost.
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
	if parsed.User != nil {
		return fmt.Errorf("URL must not contain userinfo")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("URL must not contain a fragment")
	}

	// Host must be localhost or contain a dot (domain.tld)
	host := parsed.Hostname()
	if host != "localhost" && net.ParseIP(host) == nil && !strings.Contains(host, ".") {
		return fmt.Errorf("invalid host %q: must be a valid domain (e.g., github.com/user/repo)", host)
	}

	// Security: Require HTTPS for all remote URLs except localhost
	// This prevents man-in-the-middle attacks on APK downloads and metadata
	if parsed.Scheme == "http" && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return fmt.Errorf("HTTPS is required for remote URLs (got http://%s)", host)
	}

	return nil
}

// GetSourceType returns the detected source type for APK fetching.
// Follows precedence: release_source > repository
// If release_source has an explicit type, it overrides auto-detection.
func (c *Config) GetSourceType() SourceType {
	// Check release_source
	if c.ReleaseSource != nil {
		// Local path takes precedence
		if c.ReleaseSource.IsLocal() {
			return SourceLocal
		}

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

	// Only forge repositories are release sources. F-Droid and Play Store
	// require an explicit release_source.
	sourceType := DetectSourceType(c.Repository)
	if sourceType == SourceFDroid {
		return SourceUnknown
	}
	return sourceType
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

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return SourceUnknown
	}
	host := strings.ToLower(parsed.Hostname())

	if host == "github.com" || host == "www.github.com" {
		return SourceGitHub
	}
	// GitLab: gitlab.com and self-hosted instances with "gitlab" in the domain
	if host == "gitlab.com" || host == "www.gitlab.com" || containsGitLab(rawURL) {
		return SourceGitLab
	}
	// Gitea-compatible forges: Codeberg, and hosts with gitea/forgejo in the domain
	if host == "codeberg.org" || host == "www.codeberg.org" || containsGitea(rawURL) {
		return SourceGitea
	}
	// Only the recognized F-Droid and IzzyOnDroid package URLs are accepted.
	if GetFDroidRepoInfo(rawURL) != nil {
		return SourceFDroid
	}
	if host == "play.google.com" {
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

// containsGitea checks if a URL's domain contains "gitea" or "forgejo"
// (for self-hosted Gitea/Forgejo instances).
func containsGitea(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return strings.Contains(host, "gitea") || strings.Contains(host, "forgejo")
}

// FDroidRepoInfo contains information about an F-Droid compatible repository.
type FDroidRepoInfo struct {
	RepoURL     string // Base repo URL (e.g., "https://f-droid.org/repo")
	IndexURL    string // Index JSON URL (e.g., "https://f-droid.org/repo/index-v1.json")
	PackageID   string // Package identifier (e.g., "com.example.app")
	MetadataURL string // Metadata YAML URL (empty if not available)
}

// GetFDroidRepoInfo extracts repository info from recognized F-Droid and
// IzzyOnDroid package URLs.
func GetFDroidRepoInfo(rawURL string) *FDroidRepoInfo {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	host := strings.ToLower(parsed.Hostname())
	path := strings.Trim(parsed.Path, "/")
	segments := strings.Split(path, "/")

	// F-Droid official repo
	// Handle: https://f-droid.org/packages/com.example.app and locale variants.
	if host == "f-droid.org" || host == "www.f-droid.org" {
		if len(segments) == 2 && segments[0] == "packages" || len(segments) == 3 && segments[1] == "packages" {
			packageID := segments[len(segments)-1]
			if packageID == "" {
				return nil
			}
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
	if host == "apt.izzysoft.de" {
		if len(segments) == 4 && strings.Join(segments[:3], "/") == "fdroid/index/apk" {
			packageID := segments[3]
			if packageID == "" {
				return nil
			}
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
func GetGitHubRepo(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	}
	return ""
}

// GetGitLabRepo extracts the project path from a gitlab.com URL.
func GetGitLabRepo(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "gitlab.com") {
		return ""
	}
	return gitLabProjectPath(parsed.Path)
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

// GetGitLabRepoWithBase extracts project path and base URL from a GitLab URL.
// Returns baseURL (e.g., "https://gitlab.com") and repoPath (e.g., "owner/repo").
// Supports self-hosted GitLab instances.
func GetGitLabRepoWithBase(rawURL string) (baseURL, repoPath string) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", ""
	}

	// Extract base URL
	baseURL = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)

	repoPath = gitLabProjectPath(parsed.Path)

	return baseURL, repoPath
}

func gitLabProjectPath(path string) string {
	path = strings.Trim(path, "/")
	if idx := strings.Index(path, "/-/"); idx >= 0 {
		path = path[:idx]
	}
	if strings.Count(path, "/") < 1 {
		return ""
	}
	return path
}
