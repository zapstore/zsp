package zsp

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	internalconfig "github.com/zapstore/zsp/internal/config"
)

// LoadConfig loads canonical zapstore.yaml without performing network operations.
func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, wrapOperationError(ErrInvalidConfig, err, false, "open config")
	}
	defer file.Close()

	parsed, err := internalconfig.Parse(file)
	if err != nil {
		return Config{}, wrapOperationError(ErrInvalidConfig, err, false, "parse config")
	}
	if err := parsed.Validate(); err != nil {
		return Config{}, wrapOperationError(ErrInvalidConfig, err, false, "validate config")
	}
	result := fromInternalConfig(parsed)
	if releaseSource := result.ReleaseSource; releaseSource != nil && releaseSource.LocalPath != "" && !filepath.IsAbs(releaseSource.LocalPath) {
		absolutePath, err := filepath.Abs(path)
		if err != nil {
			return Config{}, wrapOperationError(ErrInvalidConfig, err, false, "resolve config path")
		}
		releaseSource.LocalPath = filepath.Join(filepath.Dir(absolutePath), releaseSource.LocalPath)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, wrapOperationError(ErrInvalidConfig, err, false, "resolve config path")
	}
	baseDir := filepath.Dir(absolutePath)
	result.Icon = resolveConfigPath(result.Icon, baseDir)
	for index := range result.Images {
		result.Images[index] = resolveConfigPath(result.Images[index], baseDir)
	}
	result.ReleaseNotes = resolveConfigPath(result.ReleaseNotes, baseDir)
	return result, nil
}

func resolveConfigPath(value, baseDir string) string {
	if value == "" || strings.Contains(value, "://") || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(baseDir, value)
}

func fromInternalConfig(config *internalconfig.Config) Config {
	result := Config{
		FetchConfig: FetchConfig{
			Repository:        config.Repository,
			ReleaseFilter:     config.ReleaseFilter,
			Match:             config.Match,
			PrereleaseChannel: config.PrereleaseChannel,
		},
		PublishConfig: PublishConfig{
			Channel: config.Channel, Name: config.Name, Summary: config.Summary, Description: config.Description,
			Tags: append([]string(nil), config.Tags...), License: config.License, Website: config.Website,
			Icon: config.Icon, Images: append([]string(nil), config.Images...), ReleaseNotes: config.ReleaseNotes,
			SupportedNIPs:     append([]string(nil), config.SupportedNIPs...),
			MinAllowedVersion: config.MinAllowedVersion, MinAllowedVersionCode: config.MinAllowedVersionCode,
			MetadataSources: cloneOptionalStrings(config.MetadataSources),
		},
	}
	if config.ReleaseSource != nil {
		result.ReleaseSource = fromInternalReleaseSource(config.ReleaseSource)
	}
	return result
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
		PrereleaseChannel: config.PrereleaseChannel,
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
	if config.Website != "" {
		if err := internalconfig.ValidateURL(config.Website); err != nil {
			return operationErr(ErrInvalidConfig, false, "website must use HTTPS outside loopback")
		}
	}
	for _, location := range append(append([]string{}, config.Icon), config.Images...) {
		if strings.Contains(location, "://") {
			if err := internalconfig.ValidateURL(location); err != nil {
				return operationErr(ErrInvalidConfig, false, "media URL must use HTTPS outside loopback")
			}
		}
		if location != "" && !strings.Contains(location, "://") && !filepath.IsAbs(location) {
			return operationErr(ErrInvalidConfig, false, "local media paths must be absolute")
		}
	}
	if strings.Contains(config.ReleaseNotes, "://") {
		if err := internalconfig.ValidateURL(config.ReleaseNotes); err != nil {
			return operationErr(ErrInvalidConfig, false, "release notes URL must use HTTPS outside loopback")
		}
	}
	if config.ReleaseNotes != "" && !strings.Contains(config.ReleaseNotes, "://") && !filepath.IsAbs(config.ReleaseNotes) {
		return operationErr(ErrInvalidConfig, false, "local release notes path must be absolute")
	}
	for _, source := range config.MetadataSources {
		switch strings.ToLower(strings.TrimSpace(source)) {
		case "fastlane", "github", "gitlab", "gitea", "fdroid", "playstore":
		default:
			return operationErr(ErrInvalidConfig, false, "unsupported metadata source %q", source)
		}
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
