package source

import (
	"context"
	"errors"
	"net/http"

	"github.com/zapstore/zsp/v2/internal/config"
)

// AvailableSources reports metadata sources that can fill a listing for this
// fetcher, in the order a listing reads them: Fastlane, the repository host,
// F-Droid, then Play Store. A missing source is omitted. A lookup that fails
// for another reason still includes Fastlane when a local tree or forge can
// host it, and F-Droid when the release source is F-Droid.
func (f *MetadataFetcher) AvailableSources(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var sources []string
	ok, err := f.metadataSourceAvailable(ctx, f.fetchFastlaneMetadata, f.keepFastlaneOnError())
	if err != nil {
		return nil, err
	}
	if ok {
		sources = append(sources, "fastlane")
	}
	switch repositoryMetadataHost(f.cfg) {
	case config.SourceGitHub:
		sources = append(sources, "github")
	case config.SourceGitLab:
		sources = append(sources, "gitlab")
	case config.SourceGitea:
		sources = append(sources, "gitea")
	}
	ok, err = f.metadataSourceAvailable(ctx, f.fetchFDroidMetadata, f.cfg.GetSourceType() == config.SourceFDroid)
	if err != nil {
		return nil, err
	}
	if ok {
		sources = append(sources, "fdroid")
	}
	ok, err = f.metadataSourceAvailable(ctx, f.fetchPlayStoreMetadata, false)
	if err != nil {
		return nil, err
	}
	if ok {
		sources = append(sources, "playstore")
	}
	return sources, nil
}

// keepFastlaneOnError reports whether a failed Fastlane lookup, other than a
// confirmed absence, should still offer Fastlane.
func (f *MetadataFetcher) keepFastlaneOnError() bool {
	if hasLocalFastlane(f.cfg.BaseDir) {
		return true
	}
	switch repositoryMetadataHost(f.cfg) {
	case config.SourceGitHub, config.SourceGitLab, config.SourceGitea:
		return true
	default:
		return false
	}
}

// metadataSourceAvailable reports whether fetch produced metadata. Absence is
// false. Cancellation is returned. Any other failure is keepOnError.
func (f *MetadataFetcher) metadataSourceAvailable(ctx context.Context, fetch func(context.Context) (*AppMetadata, error), keepOnError bool) (bool, error) {
	_, err := fetch(ctx)
	if err == nil {
		return true, nil
	}
	if isCancellation(err) {
		return false, err
	}
	if errors.Is(err, errFastlaneUnavailable) || isStatus(err, http.StatusNotFound) {
		return false, nil
	}
	return keepOnError, nil
}
