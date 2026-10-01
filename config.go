package zsp

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	internalconfig "github.com/zapstore/zsp/internal/config"
)

// ParseConfig parses canonical zapstore.yaml from r without performing network
// operations. Both the release source and the application metadata are
// validated. The input has no file location, so relative local paths
// (release_source, icon, images, release_notes) are returned as written; use
// LoadConfig when those paths must be resolved against the config's directory.
func ParseConfig(r io.Reader) (Config, error) {
	parsed, err := parseInternalConfig(r)
	if err != nil {
		return Config{}, err
	}
	if err := parsed.Validate(); err != nil {
		return Config{}, wrapOperationError(ErrInvalidConfig, err, false, "validate config")
	}
	return fromInternalConfig(parsed), nil
}

// ParseFetchConfig parses canonical zapstore.yaml from r, validating only the
// release source configuration, and returns the source resolution half. Use it
// for configs that carry no application metadata.
func ParseFetchConfig(r io.Reader) (FetchConfig, error) {
	parsed, err := parseInternalConfig(r)
	if err != nil {
		return FetchConfig{}, err
	}
	if err := parsed.ValidateSource(); err != nil {
		return FetchConfig{}, wrapOperationError(ErrInvalidConfig, err, false, "validate config")
	}
	return fromInternalFetchConfig(parsed), nil
}

// ParsePublishConfig parses canonical zapstore.yaml from r, validating only the
// application metadata, and returns the publication half. Use it for configs
// that carry no release source.
func ParsePublishConfig(r io.Reader) (PublishConfig, error) {
	parsed, err := parseInternalConfig(r)
	if err != nil {
		return PublishConfig{}, err
	}
	if err := parsed.ValidateMetadata(); err != nil {
		return PublishConfig{}, wrapOperationError(ErrInvalidConfig, err, false, "validate config")
	}
	return fromInternalPublishConfig(parsed), nil
}

func parseInternalConfig(r io.Reader) (*internalconfig.Config, error) {
	parsed, err := internalconfig.Parse(r)
	if err != nil {
		return nil, wrapOperationError(ErrInvalidConfig, err, false, "parse config")
	}
	return parsed, nil
}

// LoadConfig loads canonical zapstore.yaml from path without performing network
// operations, resolving relative local paths against the config's directory.
func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, wrapOperationError(ErrInvalidConfig, err, false, "open config")
	}
	defer file.Close()
	result, err := ParseConfig(file)
	if err != nil {
		return Config{}, err
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, wrapOperationError(ErrInvalidConfig, err, false, "resolve config path")
	}
	baseDir := filepath.Dir(absolutePath)
	if releaseSource := result.Fetch.ReleaseSource; releaseSource != nil && releaseSource.LocalPath != "" && !filepath.IsAbs(releaseSource.LocalPath) {
		releaseSource.LocalPath = filepath.Join(baseDir, releaseSource.LocalPath)
	}
	result.Publish.Icon = resolveConfigPath(result.Publish.Icon, baseDir)
	for index := range result.Publish.Images {
		result.Publish.Images[index] = resolveConfigPath(result.Publish.Images[index], baseDir)
	}
	result.Publish.ReleaseNotes = resolveConfigPath(result.Publish.ReleaseNotes, baseDir)
	return result, nil
}

func resolveConfigPath(value, baseDir string) string {
	if value == "" || strings.Contains(value, "://") || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(baseDir, value)
}

// MarshalYAML renders the configuration in the canonical zapstore.yaml shape.
// The YAML representation itself lives in internal/config.
func (c Config) MarshalYAML() (any, error) {
	return c.toInternalConfig().MarshalYAML()
}

// MarshalYAML renders the source resolution configuration.
func (c FetchConfig) MarshalYAML() (any, error) {
	return Config{Fetch: c}.toInternalConfig().MarshalYAML()
}

// MarshalYAML renders the publication metadata configuration.
func (c PublishConfig) MarshalYAML() (any, error) {
	return Config{Publish: c}.toInternalConfig().MarshalYAML()
}

// toInternalConfig converts the public configuration to the internal
// representation without validating it, so it can be marshaled back to YAML.
func (c Config) toInternalConfig() *internalconfig.Config {
	internal := toPublishInternalConfig(c.Publish)
	internal.Repository = c.Fetch.Repository
	internal.ReleaseFilter = c.Fetch.ReleaseFilter
	internal.Match = c.Fetch.Match
	internal.PrereleaseChannel = c.Fetch.PrereleaseChannel
	internal.ReleaseSource = toInternalReleaseSource(c.Fetch.ReleaseSource)
	return internal
}

func toInternalReleaseSource(source *ReleaseSource) *internalconfig.ReleaseSource {
	if source == nil {
		return nil
	}
	result := &internalconfig.ReleaseSource{
		URL: source.URL, LocalPath: source.LocalPath, Type: source.Type, AssetURL: source.AssetURL,
	}
	if source.VersionExtractor != nil {
		result.Version = toInternalExtractor(source.VersionExtractor)
	}
	if source.AssetExtractor != nil {
		result.Asset = toInternalExtractor(source.AssetExtractor)
	}
	return result
}

func fromInternalConfig(config *internalconfig.Config) Config {
	return Config{
		Fetch:   fromInternalFetchConfig(config),
		Publish: fromInternalPublishConfig(config),
	}
}

func fromInternalFetchConfig(config *internalconfig.Config) FetchConfig {
	result := FetchConfig{
		Repository:        config.Repository,
		ReleaseFilter:     config.ReleaseFilter,
		Match:             config.Match,
		PrereleaseChannel: config.PrereleaseChannel,
		baseDir:           config.BaseDir,
	}
	if config.ReleaseSource != nil {
		result.ReleaseSource = fromInternalReleaseSource(config.ReleaseSource)
	}
	return result
}

func fromInternalPublishConfig(config *internalconfig.Config) PublishConfig {
	return PublishConfig{
		Channel: config.Channel, Name: config.Name, Summary: config.Summary, Description: config.Description,
		Tags: append([]string(nil), config.Tags...), License: config.License, Website: config.Website,
		Icon: config.Icon, Images: append([]string(nil), config.Images...), ReleaseNotes: config.ReleaseNotes,
		SupportedNIPs:     append([]string(nil), config.SupportedNIPs...),
		MinAllowedVersion: config.MinAllowedVersion, MinAllowedVersionCode: config.MinAllowedVersionCode,
		MetadataSources: cloneOptionalStrings(config.MetadataSources),
		baseDir:         config.BaseDir,
	}
}

func fromInternalReleaseSource(source *internalconfig.ReleaseSource) *ReleaseSource {
	result := &ReleaseSource{URL: source.URL, LocalPath: source.LocalPath, Type: source.Type, AssetURL: source.AssetURL}
	if source.Version != nil {
		result.VersionExtractor = fromInternalExtractor(source.Version)
	}
	if source.Asset != nil {
		result.AssetExtractor = fromInternalExtractor(source.Asset)
	}
	return result
}

func fromInternalExtractor(extractor *internalconfig.VersionExtractor) *Extractor {
	return &Extractor{URL: extractor.URL, Selector: extractor.Selector, Attribute: extractor.Attribute, Path: extractor.Path, Header: extractor.Header, Match: extractor.Match}
}

func fetchInternalConfig(config FetchConfig) (*internalconfig.Config, error) {
	if config.Repository == "" && config.ReleaseSource == nil {
		return nil, operationErr(ErrInvalidConfig, false, "repository or release source is required")
	}
	internal := &internalconfig.Config{
		Repository: config.Repository, ReleaseFilter: config.ReleaseFilter, Match: config.Match,
		PrereleaseChannel: config.PrereleaseChannel, BaseDir: config.baseDir,
	}
	if strings.HasPrefix(config.Repository, "naddr1") {
		if config.ReleaseSource == nil {
			return nil, operationErr(ErrInvalidConfig, false, "a NIP-34 repository requires release_source")
		}
		pointer, err := internalconfig.ParseNaddr(config.Repository)
		if err != nil {
			return nil, wrapOperationError(ErrInvalidConfig, err, false, "validate repository naddr")
		}
		internal.NIP34Repo = pointer
	}
	if config.ReleaseSource != nil {
		sourceURL := config.ReleaseSource.URL
		localPath := config.ReleaseSource.LocalPath
		sourceType := strings.ToLower(strings.TrimSpace(config.ReleaseSource.Type))
		if sourceType == "local" {
			if localPath != "" && sourceURL != "" {
				return nil, operationErr(ErrInvalidConfig, false, "local release source must specify one path")
			}
			if localPath == "" {
				localPath = sourceURL
				sourceURL = ""
			}
		}
		if localPath != "" && sourceType != "" && sourceType != "local" {
			return nil, operationErr(ErrInvalidConfig, false, "local release source cannot use a remote source type")
		}
		if localPath != "" && (sourceURL != "" || config.ReleaseSource.AssetURL != "" ||
			config.ReleaseSource.VersionExtractor != nil || config.ReleaseSource.AssetExtractor != nil) {
			return nil, operationErr(ErrInvalidConfig, false, "local release source cannot include remote source settings")
		}
		if localPath != "" && !filepath.IsAbs(localPath) {
			return nil, operationErr(ErrInvalidConfig, false, "local release source must be absolute")
		}
		directAPK := directAPKURL(sourceURL) && config.ReleaseSource.AssetURL == ""
		internal.ReleaseSource = &internalconfig.ReleaseSource{
			URL: sourceURL, LocalPath: localPath, Type: sourceType,
			AssetURL: config.ReleaseSource.AssetURL, IsWebSource: directAPK || config.ReleaseSource.AssetURL != "" ||
				config.ReleaseSource.VersionExtractor != nil || config.ReleaseSource.AssetExtractor != nil,
		}
		if directAPK {
			internal.ReleaseSource.AssetURL = sourceURL
		}
		if config.ReleaseSource.VersionExtractor != nil {
			internal.ReleaseSource.Version = toInternalExtractor(config.ReleaseSource.VersionExtractor)
		}
		if config.ReleaseSource.AssetExtractor != nil {
			internal.ReleaseSource.Asset = toInternalExtractor(config.ReleaseSource.AssetExtractor)
		}
	}
	internal.CanonicalizeForgeURLs()
	if err := internal.Validate(); err != nil {
		return nil, wrapOperationError(ErrInvalidConfig, err, false, "validate fetch configuration")
	}
	if config.Match != "" {
		if _, err := regexp.Compile(config.Match); err != nil {
			return nil, wrapOperationError(ErrInvalidConfig, err, false, "validate match")
		}
	}
	switch internal.GetSourceType() {
	case internalconfig.SourceLocal, internalconfig.SourceGitHub, internalconfig.SourceGitLab,
		internalconfig.SourceGitea, internalconfig.SourceFDroid, internalconfig.SourceWeb:
		// Supported APK release sources.
	default:
		return nil, operationErr(ErrInvalidConfig, false, "unsupported release source")
	}
	return internal, nil
}

func directAPKURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") &&
		strings.EqualFold(filepath.Ext(parsed.Path), ".apk")
}

func toInternalExtractor(extractor *Extractor) *internalconfig.VersionExtractor {
	return &internalconfig.VersionExtractor{URL: extractor.URL, Selector: extractor.Selector, Attribute: extractor.Attribute, Path: extractor.Path, Header: extractor.Header, Match: extractor.Match}
}

func toPublishInternalConfig(config PublishConfig) *internalconfig.Config {
	return &internalconfig.Config{
		Channel: config.Channel, Name: config.Name, Summary: config.Summary, Description: config.Description,
		Tags: append([]string(nil), config.Tags...), License: config.License, Website: config.Website,
		Icon: config.Icon, Images: append([]string(nil), config.Images...), ReleaseNotes: config.ReleaseNotes,
		SupportedNIPs:     append([]string(nil), config.SupportedNIPs...),
		MinAllowedVersion: config.MinAllowedVersion, MinAllowedVersionCode: config.MinAllowedVersionCode,
		MetadataSources: cloneOptionalStrings(config.MetadataSources),
	}
}

func cloneOptionalStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func selectedChannel(options PublishOptions, config PublishConfig) string {
	for _, channel := range []string{options.Channel, config.Channel} {
		if channel = strings.TrimSpace(channel); channel != "" {
			return channel
		}
	}
	return "main"
}

func validatePublishInput(config PublishConfig, options PublishOptions) error {
	if err := validateRelayURLs(options.Relays); err != nil {
		return err
	}
	if options.BrowserPort < 0 || options.BrowserPort > 65535 {
		return operationErr(ErrInvalidConfig, false, "browser port must be between 0 and 65535")
	}
	return validatePublishConfig(config)
}

// validatePublishConfig validates application metadata. It reuses the internal
// metadata validator and adds the public API rule that local media and
// release-notes paths must be absolute.
func validatePublishConfig(config PublishConfig) error {
	if err := toPublishInternalConfig(config).ValidateMetadata(); err != nil {
		return wrapOperationError(ErrInvalidConfig, err, false, "validate publish configuration")
	}
	for _, location := range append([]string{config.Icon}, config.Images...) {
		if location != "" && !strings.Contains(location, "://") && !filepath.IsAbs(location) {
			return operationErr(ErrInvalidConfig, false, "local media paths must be absolute")
		}
	}
	if config.ReleaseNotes != "" && !strings.Contains(config.ReleaseNotes, "://") && !filepath.IsAbs(config.ReleaseNotes) {
		return operationErr(ErrInvalidConfig, false, "local release notes path must be absolute")
	}
	return nil
}

func validateRelayURLs(relayURLs []string) error {
	for _, relayURL := range relayURLs {
		parsed, err := url.Parse(strings.TrimSpace(relayURL))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "wss" && !(parsed.Scheme == "ws" && isLoopbackHost(parsed.Hostname()))) {
			return operationErr(ErrInvalidConfig, false, "relay must use wss outside loopback")
		}
		if parsed.User != nil || parsed.Fragment != "" {
			return operationErr(ErrInvalidConfig, false, "relay URL must not contain credentials or a fragment")
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
