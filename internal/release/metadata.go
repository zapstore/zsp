package release

import (
	"context"
	"os"
	"slices"
	"strings"

	"github.com/zapstore/zsp/internal/apk"
	internalconfig "github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/netpolicy"
	"github.com/zapstore/zsp/internal/source"
	"github.com/zapstore/zsp/internal/spdx"
)

func preparePublishMetadata(ctx context.Context, config PublishConfig, candidate *APK, parsed *apk.APKInfo) (PublishConfig, *internalconfig.Config, []string, error) {
	fetch, err := fetchInternalConfig(candidate.fetchConfig)
	if err != nil {
		return PublishConfig{}, nil, nil, err
	}
	if config.baseDir != "" {
		fetch.BaseDir = config.baseDir
	} else if wd, wdErr := os.Getwd(); wdErr == nil {
		fetch.BaseDir = wd
	}
	fetch.MetadataSources = cloneOptionalStrings(config.MetadataSources)

	var warnings []string
	var results []*source.AppMetadata
	if plan := source.PlanMetadata(fetch); len(plan.Sources) > 0 {
		fetched := source.NewMetadataFetcherWithPackageID(fetch, parsed.PackageID).Fetch(ctx, plan)
		if err := ctx.Err(); err != nil {
			return PublishConfig{}, nil, nil, operationCodeErr(err, "cancelled", false, "metadata preparation cancelled")
		}
		for _, metadataError := range fetched.Errors {
			detail := ""
			if metadataError.Err != nil {
				detail = causeDetail(metadataError.Err)
			}
			warnings = append(warnings, "metadata source "+metadataError.Source+" failed"+detail)
		}
		results = fetched.Metadata
	}

	prepared := mergeMetadata(config, parsed.Label, parsed.PackageID, results)
	prepared.baseDir = fetch.BaseDir
	return prepared, eventConfig(fetch, prepared), warnings, nil
}

// mergeMetadata resolves the publication metadata. It is the one place that
// states the priority, for each field:
//
//	name:    explicit YAML, then the APK label, then the first source
//	         that has one, then the package ID
//	others:  explicit YAML, then the first source that has a value
//
// Sources are in the order they were configured. A website must pass the URL
// policy. Explicit media replaces discovered media. Otherwise the icon
// candidates are the metadata icons in source order, tried after the APK icon,
// and the screenshot sets are each source's screenshots in source order,
// of which the first set that yields a usable image is used.
//
// A remote location is kept only if it passes the URL policy; a local
// path is kept only from the local Fastlane tree, which tags it as local.
func mergeMetadata(explicit PublishConfig, apkLabel, packageID string, results []*source.AppMetadata) PublishConfig {
	resolved := explicit
	resolved.Tags = slices.Clone(explicit.Tags)
	resolved.Images = slices.Clone(explicit.Images)
	resolved.iconCandidates = nil
	resolved.imageSets = nil

	if resolved.Name == "" {
		resolved.Name = firstText(apkLabel, firstSourceText(results, func(m *source.AppMetadata) string { return m.Name }), packageID)
	}
	if resolved.Summary == "" {
		resolved.Summary = firstSourceText(results, func(m *source.AppMetadata) string { return m.Summary })
	}
	if resolved.Description == "" {
		resolved.Description = firstSourceText(results, func(m *source.AppMetadata) string { return m.Description })
	}
	if resolved.License == "" {
		resolved.License = firstSourceText(results, func(m *source.AppMetadata) string { return m.License })
	} else if id := spdx.ID(resolved.License); id != "" {
		resolved.License = id
	}
	if resolved.Website == "" {
		resolved.Website = firstSourceText(results, func(m *source.AppMetadata) string {
			if netpolicy.ValidateURL(m.Website) != nil {
				return ""
			}
			return m.Website
		})
	}
	if len(resolved.Tags) == 0 {
		for _, meta := range results {
			if len(meta.Tags) > 0 {
				resolved.Tags = slices.Clone(meta.Tags)
				break
			}
		}
	}

	for _, meta := range results {
		if resolved.Icon == "" {
			if meta.LocalIcon != "" {
				resolved.iconCandidates = append(resolved.iconCandidates, meta.LocalIcon)
			}
			resolved.iconCandidates = append(resolved.iconCandidates, remoteLocations([]string{meta.IconURL})...)
		}
		if len(resolved.Images) == 0 {
			set := append(slices.Clone(meta.LocalImages), remoteLocations(meta.ImageURLs)...)
			if len(set) > 0 {
				resolved.imageSets = append(resolved.imageSets, set)
			}
		}
	}
	return resolved
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstSourceText(results []*source.AppMetadata, field func(*source.AppMetadata) string) string {
	for _, meta := range results {
		if value := field(meta); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func remoteLocations(values []string) []string {
	var accepted []string
	for _, value := range values {
		if netpolicy.ValidateURL(value) == nil {
			accepted = append(accepted, value)
		}
	}
	return accepted
}

// eventConfig is the configuration the application event is built from: the
// resolved publication fields on top of the source configuration.
func eventConfig(fetch *internalconfig.Config, publish PublishConfig) *internalconfig.Config {
	event := toPublishInternalConfig(publish)
	event.Repository = fetch.Repository
	event.NIP34Repo = fetch.NIP34Repo
	event.ReleaseFilter = fetch.ReleaseFilter
	event.Match = fetch.Match
	event.PrereleaseChannel = fetch.PrereleaseChannel
	event.ReleaseSource = fetch.ReleaseSource
	event.BaseDir = fetch.BaseDir
	return event
}
