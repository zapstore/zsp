package zsp

import (
	"context"
	"errors"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/source"
)

// Fetch resolves and verifies usable APK candidates. The caller owns each
// returned APK and must call Close when it will not be published.
//
// When SkipETag is false, Fetch sends stored ETags and returns ErrNoNewAPK
// if the source reports that nothing has changed.
func Fetch(ctx context.Context, config FetchConfig, options FetchOptions) ([]*APK, error) {
	internal, err := fetchInternalConfig(config)
	if err != nil {
		return nil, err
	}
	src, err := source.NewWithOptions(internal, source.Options{
		IncludePreReleases: config.PrereleaseChannel != "",
		SkipCache:          config.SkipETag,
	})
	if err != nil {
		return nil, wrapOperationError(ErrInvalidConfig, err, false, "create source")
	}
	report(options, "fetch", "resolve", config.Repository, 0, 0)
	release, err := src.FetchLatestRelease(ctx)
	if err != nil {
		if errors.Is(err, source.ErrNotModified) {
			return nil, operationErr(ErrNoNewAPK, false, "no new APKs")
		}
		if contextErr := contextOperationError(ctx.Err(), "fetch"); contextErr != nil {
			return nil, contextErr
		}
		sentinel, retryable := sourceErrorClassification(err)
		return nil, wrapOperationError(sentinel, err, retryable, "resolve source")
	}
	if release == nil || !releaseMatches(release, config.ReleaseFilter) {
		return nil, operationErr(ErrNoAPK, false, "no matching release")
	}

	candidates, err := filterCandidates(release.Assets, config.Match)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, operationErr(ErrNoAPK, false, "no APK candidates")
	}
	isLocalSource := config.ReleaseSource != nil && config.ReleaseSource.LocalPath != ""
	if len(candidates) > 10 && !isLocalSource {
		return nil, operationErr(ErrTooManyCandidates, false, "found %d candidates; narrow match", len(candidates))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftURL := normalizedCandidateURL(candidates[i].URL)
		rightURL := normalizedCandidateURL(candidates[j].URL)
		if leftURL == rightURL {
			return candidates[i].Name < candidates[j].Name
		}
		return leftURL < rightURL
	})

	results := make([]*APK, 0, len(candidates))
	seen := make(map[string]struct{})
	var lastDownloadError error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			closeAPKs(results)
			return nil, contextOperationError(err, "fetch")
		}
		report(options, "fetch", "download", candidate.Name, 0, candidate.Size)
		path := candidate.LocalPath
		managed := false
		tempDir := ""
		if path == "" {
			tempDir, err = os.MkdirTemp("", "zsp-fetch-*")
			if err != nil {
				closeAPKs(results)
				return nil, wrapOperationError(ErrSourceFailed, err, false, "create temporary directory")
			}
			path, err = src.Download(ctx, candidate, tempDir, func(done, total int64) {
				report(options, "fetch", "download", candidate.Name, done, total)
			})
			if err != nil {
				_ = os.RemoveAll(tempDir)
				if ctx.Err() != nil {
					closeAPKs(results)
					return nil, contextOperationError(ctx.Err(), "fetch")
				}
				lastDownloadError = err
				report(options, "fetch", "warning", candidate.Name+": download failed", 0, 0)
				continue
			}
			managed = true
		}
		report(options, "fetch", "verify", candidate.Name, 0, 0)
		if info, statErr := os.Stat(path); statErr != nil || info.Size() > source.MaxDownloadSize {
			if managed {
				_ = os.RemoveAll(tempDir)
			}
			report(options, "fetch", "warning", candidate.Name+": APK exceeds the size limit", 0, 0)
			continue
		}
		parsed, parseErr := apk.Parse(path)
		if parseErr != nil {
			if managed {
				_ = os.RemoveAll(tempDir)
			}
			report(options, "fetch", "warning", candidate.Name+": invalid or unsigned APK", 0, 0)
			continue
		}
		if parsed.IsWatch() || !parsed.IsArm64() {
			if managed {
				_ = os.RemoveAll(tempDir)
			}
			report(options, "fetch", "warning", candidate.Name+": unsupported Android platform", 0, 0)
			continue
		}
		if _, duplicate := seen[parsed.SHA256]; duplicate {
			if managed {
				_ = os.RemoveAll(tempDir)
			}
			report(options, "fetch", "warning", candidate.Name+": duplicate APK omitted", 0, 0)
			continue
		}
		seen[parsed.SHA256] = struct{}{}
		eventSourceURL := candidate.URL
		if candidate.ExcludeURL || !isPublishableSourceURL(eventSourceURL) {
			eventSourceURL = ""
		}
		results = append(results, &APK{
			Hash:            parsed.SHA256,
			Filename:        candidate.Name,
			SourceURL:       eventSourceURL,
			Size:            parsed.FileSize,
			AppID:           parsed.PackageID,
			VersionName:     parsed.VersionName,
			VersionCode:     parsed.VersionCode,
			MinSDK:          parsed.MinSDK,
			TargetSDK:       parsed.TargetSDK,
			Name:            parsed.Label,
			CertificateHash: parsed.CertFingerprint,
			LineageHashes:   append([]string(nil), parsed.SigningAncestors...),
			Architectures:   append([]string(nil), parsed.Architectures...),
			ownership:       newAPKOwnership(path, tempDir, managed),
			fetchConfig:     cloneFetchConfig(config),
			verified: verifiedAPK{
				hash:            parsed.SHA256,
				filename:        candidate.Name,
				size:            parsed.FileSize,
				appID:           parsed.PackageID,
				versionCode:     parsed.VersionCode,
				certificateHash: parsed.CertFingerprint,
				lineageHashes:   append([]string(nil), parsed.SigningAncestors...),
				originalURL:     eventSourceURL,
				releaseNotes:    release.Changelog,
				releasedAt:      release.CreatedAt,
			},
		})
	}
	for _, candidate := range results {
		candidate.startLifetime()
	}
	if len(results) == 0 {
		if lastDownloadError != nil {
			sentinel, retryable := sourceErrorClassification(lastDownloadError)
			return nil, wrapOperationError(sentinel, lastDownloadError, retryable, "download APK candidates")
		}
		return nil, operationErr(ErrNoAPK, false, "no candidate passed APK verification")
	}
	if !config.SkipETag {
		if committer, ok := src.(source.CacheCommitter); ok {
			_ = committer.CommitCache()
		}
	}
	return results, nil
}

func cloneFetchConfig(config FetchConfig) FetchConfig {
	cloned := config
	if config.ReleaseSource != nil {
		source := *config.ReleaseSource
		if config.ReleaseSource.VersionExtractor != nil {
			extractor := *config.ReleaseSource.VersionExtractor
			source.VersionExtractor = &extractor
		}
		if config.ReleaseSource.AssetExtractor != nil {
			extractor := *config.ReleaseSource.AssetExtractor
			source.AssetExtractor = &extractor
		}
		cloned.ReleaseSource = &source
	}
	return cloned
}

func normalizedCandidateURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	return parsed.String()
}

// isPublishableSourceURL deliberately accepts a narrower set than the
// downloader. Query-bearing URLs commonly embed short-lived credentials, and
// a signed event must never expose them.
func isPublishableSourceURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != "" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

const apkLifetime = 5 * time.Minute

func newAPKOwnership(path, tempDir string, managed bool) *apkOwnership {
	return &apkOwnership{path: path, tempDir: tempDir, managed: managed}
}

func (apk *APK) startLifetime() {
	if apk == nil || apk.ownership == nil {
		return
	}
	apk.ownership.mu.Lock()
	defer apk.ownership.mu.Unlock()
	apk.ownership.startTimerLocked()
}

func (state *apkOwnership) startTimerLocked() {
	if state.closed || state.publishing {
		return
	}
	if state.timer != nil {
		state.timer.Stop()
	}
	state.timer = scheduleAPKExpiry(apkLifetime, func() {
		_ = state.expire()
	})
}

// Close releases temporary files owned by ZSP. It never deletes a local APK.
func (apk *APK) Close() error {
	if apk == nil || apk.ownership == nil {
		return nil
	}
	if err := apk.ownership.close(); err != nil {
		return operationErr(nil, false, "clean up APK")
	}
	return nil
}

func (state *apkOwnership) close() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	if state.publishing {
		state.closeRequested = true
		return nil
	}
	return state.cleanupLocked()
}

func (state *apkOwnership) expire() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.publishing {
		return nil
	}
	return state.cleanupLocked()
}

func (state *apkOwnership) cleanupLocked() error {
	state.closed = true
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	if !state.managed || state.path == "" {
		return nil
	}
	err := os.RemoveAll(state.tempDir)
	if state.tempDir == "" {
		err = os.Remove(state.path)
	}
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	state.managed = false
	return err
}

func (apk *APK) beginPublish() (string, bool) {
	if apk == nil || apk.ownership == nil {
		return "", false
	}
	state := apk.ownership
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.publishing || state.path == "" {
		return "", false
	}
	state.publishing = true
	if state.timer != nil {
		state.timer.Stop()
	}
	return state.path, true
}

func (apk *APK) finishPublish(success bool) error {
	if apk == nil || apk.ownership == nil {
		return nil
	}
	state := apk.ownership
	state.mu.Lock()
	if !state.publishing {
		state.mu.Unlock()
		return nil
	}
	state.publishing = false
	if !success && !state.closeRequested {
		state.startTimerLocked()
		state.mu.Unlock()
		return nil
	}
	err := state.cleanupLocked()
	state.mu.Unlock()
	return err
}

func filterCandidates(assets []*source.Asset, match string) ([]*source.Asset, error) {
	var expression *regexp.Regexp
	var err error
	if match != "" {
		expression, err = regexp.Compile(match)
		if err != nil {
			return nil, wrapOperationError(ErrInvalidConfig, err, false, "validate match")
		}
	}
	result := make([]*source.Asset, 0, len(assets))
	for _, asset := range assets {
		if !source.IsAPKAsset(asset.Name, asset.URL) || source.HasUnsupportedArchitecture(asset.Name) || excludedFilename(asset.Name) {
			continue
		}
		if expression != nil && !expression.MatchString(asset.Name) {
			continue
		}
		result = append(result, asset)
	}
	result = omitGooglePlayCandidates(result)
	result = preferNonFDroidCandidates(result)
	return omitUniversalCandidatesWhenArm64Exists(result), nil
}

var excludedFilenamePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(x86_64|x86|armeabi-v7a|armeabi|unsigned|split|config)([^a-z0-9]|$)`)
var (
	fdroidFilenamePattern     = regexp.MustCompile(`(?i)(^|[^a-z0-9])f-?droid([^a-z0-9]|$)`)
	googlePlayFilenamePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(google|play|playstore)([^a-z0-9]|$)`)
	arm64FilenamePattern      = regexp.MustCompile(`(?i)(^|[^a-z0-9])arm64-v8a([^a-z0-9]|$)`)
	universalFilenamePattern  = regexp.MustCompile(`(?i)(^|[^a-z0-9])universal([^a-z0-9]|$)`)
)

func excludedFilename(name string) bool {
	return excludedFilenamePattern.MatchString(name)
}

func omitGooglePlayCandidates(candidates []*source.Asset) []*source.Asset {
	return filterAssets(candidates, func(candidate *source.Asset) bool {
		return !googlePlayFilenamePattern.MatchString(candidate.Name)
	})
}

func preferNonFDroidCandidates(candidates []*source.Asset) []*source.Asset {
	for _, candidate := range candidates {
		if !fdroidFilenamePattern.MatchString(candidate.Name) {
			return filterAssets(candidates, func(candidate *source.Asset) bool {
				return !fdroidFilenamePattern.MatchString(candidate.Name)
			})
		}
	}
	return candidates
}

func omitUniversalCandidatesWhenArm64Exists(candidates []*source.Asset) []*source.Asset {
	for _, candidate := range candidates {
		if arm64FilenamePattern.MatchString(candidate.Name) {
			return filterAssets(candidates, func(candidate *source.Asset) bool {
				return !universalFilenamePattern.MatchString(candidate.Name)
			})
		}
	}
	return candidates
}

func filterAssets(candidates []*source.Asset, keep func(*source.Asset) bool) []*source.Asset {
	filtered := make([]*source.Asset, 0, len(candidates))
	for _, candidate := range candidates {
		if keep(candidate) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func releaseMatches(release *source.Release, filter string) bool {
	if filter == "" {
		return true
	}
	expression, err := regexp.Compile(filter)
	return err == nil && (expression.MatchString(release.Version) ||
		expression.MatchString(release.TagName) ||
		expression.MatchString(release.Name))
}

func report(options FetchOptions, operation, phase, target string, completed, total int64) {
	if options.OnProgress != nil {
		options.OnProgress(Progress{Operation: operation, Phase: phase, Target: target, Completed: completed, Total: total})
	}
}

func isRetryableSourceError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{"rate limit", "temporar", "timeout", "connection reset", "unavailable", "status 429", "status 502", "status 503", "status 504"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func sourceErrorClassification(err error) (error, bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrTemporaryFailure, true
	}
	if err == nil {
		return ErrSourceFailed, false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "rate limit") || strings.Contains(message, "status 429") {
		return ErrRateLimited, true
	}
	if isRetryableSourceError(err) {
		return ErrTemporaryFailure, true
	}
	return ErrSourceFailed, false
}

func closeAPKs(apks []*APK) {
	for _, apk := range apks {
		_ = apk.Close()
	}
}
