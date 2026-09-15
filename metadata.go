package zsp

import (
	"context"

	"github.com/zapstore/zsp/internal/apk"
	internalconfig "github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/source"
)

func preparePublishMetadata(ctx context.Context, config PublishConfig, candidate *APK, parsed *apk.APKInfo) (PublishConfig, *internalconfig.Config, []string, error) {
	internal, err := fetchInternalConfig(candidate.fetchConfig)
	if err != nil {
		return PublishConfig{}, nil, nil, err
	}
	overrides := toPublishInternalConfig(config)
	internal.Name = overrides.Name
	internal.Summary = overrides.Summary
	internal.Description = overrides.Description
	internal.Tags = overrides.Tags
	internal.License = overrides.License
	internal.Website = overrides.Website
	internal.Icon = overrides.Icon
	internal.Images = overrides.Images
	internal.ReleaseNotes = overrides.ReleaseNotes
	internal.SupportedNIPs = overrides.SupportedNIPs
	internal.MinAllowedVersion = overrides.MinAllowedVersion
	internal.MinAllowedVersionCode = overrides.MinAllowedVersionCode
	internal.MetadataSources = overrides.MetadataSources

	var warnings []string
	if config.MetadataSources == nil || len(config.MetadataSources) > 0 {
		sources := source.DefaultMetadataSources(internal)
		fetcher := source.NewMetadataFetcherWithPackageID(internal, parsed.PackageID)
		fetcher.APKName = parsed.Label
		var result *source.MetadataResult
		if config.MetadataSources == nil && len(sources) == 2 && sources[0] == "fastlane" {
			result = fetcher.FetchAutomaticMetadataWithResult(ctx, sources[1])
		} else {
			result = fetcher.FetchMetadataWithResult(ctx, sources)
		}
		if err := ctx.Err(); err != nil {
			return PublishConfig{}, nil, nil, operationCodeErr(err, "cancelled", false, "metadata preparation cancelled")
		}
		for _, metadataError := range result.Errors {
			warnings = append(warnings, "metadata source "+metadataError.Source+" failed")
		}
	}
	prepared := fromInternalConfig(internal).PublishConfig
	return prepared, internal, warnings, nil
}
