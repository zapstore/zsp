package release

import (
	"context"
	"crypto/x509"
	"errors"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/sanitize"
	"github.com/zapstore/zsp/internal/source"
)

// Fetch resolves and verifies usable APK candidates. The caller owns each
// returned APK and must call Close when it will not be published.
//
// When options.SkipHTTPCache is false, Fetch sends stored HTTP validators
// and returns ErrNoNewAPK if the source reports that nothing has changed.
// Publish deletes that cache only for Retryable failures so the next Fetch
// can retry the same source. Permanent outcomes keep the cache;
// SkipHTTPCache is the caller's lever when their inputs changed.
func Fetch(ctx context.Context, config FetchConfig, options FetchOptions) ([]*APK, error) {
	internal, err := fetchInternalConfig(config)
	if err != nil {
		return nil, err
	}
	src, err := source.NewWithOptions(internal, source.Options{
		IncludePreReleases: config.PrereleaseChannel != "",
		SkipHTTPCache:      options.SkipHTTPCache,
		MaxReleases:        options.MaxReleases,
		CacheDir:           options.HTTPCacheDir,
	})
	if err != nil {
		return nil, wrapOperationError(ErrInvalidConfig, err, false, "create source"+causeDetail(err))
	}
	report(options, "fetch", "resolve", config.Repository, 0, 0)
	release, err := src.FetchLatestRelease(ctx)
	if err != nil {
		if errors.Is(err, source.ErrNotModified) {
			// A source may refresh cached validators even when reporting
			// not modified (e.g. an ETag that moved for volatile fields).
			if !options.SkipHTTPCache {
				commitHTTPCache(src, options)
			}
			return nil, operationErr(ErrNoNewAPK, false, "no new APKs")
		}
		if contextErr := contextOperationError(ctx.Err(), "fetch"); contextErr != nil {
			return nil, contextErr
		}
		sentinel, retryable := sourceErrorClassification(err)
		return nil, wrapOperationError(sentinel, err, retryable, "resolve source"+causeDetail(err))
	}
	if release == nil || !releaseMatches(release, config.ReleaseFilter) {
		return nil, operationErr(ErrNoAPK, false, "no matching release")
	}

	candidates, err := filterCandidates(release.Assets, config.Match)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, noCandidateError(release.Assets, config.Match)
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
	var clearHTTPCache func() error
	if clearer, ok := src.(source.CacheClearer); ok {
		clearHTTPCache = clearer.ClearHTTPCache
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			closeAPKs(results)
			return nil, contextOperationError(err, "fetch")
		}
		displayName := safeAssetName(candidate.Name)
		report(options, "fetch", "download", displayName, 0, candidate.Size)
		path := candidate.LocalPath
		managed := false
		tempDir := ""
		if path == "" {
			tempDir, err = os.MkdirTemp("", "zsp-fetch-*")
			if err != nil {
				closeAPKs(results)
				return nil, wrapOperationError(ErrSourceFailed, err, false, "create temporary directory"+causeDetail(err))
			}
			path, err = src.Download(ctx, candidate, tempDir, func(done, total int64) {
				report(options, "fetch", "download", displayName, done, total)
			})
			if err != nil {
				_ = os.RemoveAll(tempDir)
				if ctx.Err() != nil {
					closeAPKs(results)
					return nil, contextOperationError(ctx.Err(), "fetch")
				}
				lastDownloadError = err
				report(options, "fetch", "warning", displayName+": download failed", 0, 0)
				continue
			}
			managed = true
		}
		report(options, "fetch", "verify", displayName, 0, 0)
		if info, statErr := os.Stat(path); statErr != nil || info.Size() > source.MaxDownloadSize {
			if managed {
				_ = os.RemoveAll(tempDir)
			}
			report(options, "fetch", "warning", displayName+": APK exceeds the size limit", 0, 0)
			continue
		}
		parsed, parseErr := apk.Parse(ctx, path)
		if parseErr != nil {
			if managed {
				_ = os.RemoveAll(tempDir)
			}
			if ctx.Err() != nil {
				closeAPKs(results)
				return nil, contextOperationError(ctx.Err(), "fetch")
			}
			report(options, "fetch", "warning", displayName+": invalid or unsigned APK", 0, 0)
			continue
		}
		if parsed.IsWatch() || !parsed.IsArm64() {
			if managed {
				_ = os.RemoveAll(tempDir)
			}
			report(options, "fetch", "warning", displayName+": unsupported Android platform", 0, 0)
			continue
		}
		if _, duplicate := seen[parsed.SHA256]; duplicate {
			if managed {
				_ = os.RemoveAll(tempDir)
			}
			report(options, "fetch", "warning", displayName+": duplicate APK omitted", 0, 0)
			continue
		}
		seen[parsed.SHA256] = struct{}{}
		eventSourceURL := candidate.URL
		if candidate.ExcludeURL || !isPublishableSourceURL(eventSourceURL) {
			eventSourceURL = ""
		}
		ownership := newAPKOwnership(path, tempDir, managed)
		ownership.clearHTTPCache = clearHTTPCache
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
			ownership:       ownership,
			fetchConfig:     cloneFetchConfig(config),
			verified: verifiedAPK{
				hash:            parsed.SHA256,
				filename:        candidate.Name,
				size:            parsed.FileSize,
				appID:           parsed.PackageID,
				versionCode:     parsed.VersionCode,
				certificateHash: parsed.CertFingerprint,
				lineageHashes:   append([]string(nil), parsed.SigningAncestors...),
				certificates:    append([]*x509.Certificate(nil), parsed.Certificates()...),
				originalURL:     eventSourceURL,
				releaseNotes:    release.Changelog,
				releaseVersion:  release.Version,
				releasedAt:      release.CreatedAt,
			},
		})
	}
	if len(results) == 0 {
		if lastDownloadError != nil {
			sentinel, retryable := sourceErrorClassification(lastDownloadError)
			return nil, wrapOperationError(sentinel, lastDownloadError, retryable, "download APK candidates"+causeDetail(lastDownloadError))
		}
		return nil, operationErr(ErrNoAPK, false, "no candidate passed APK verification")
	}
	if !options.SkipHTTPCache {
		commitHTTPCache(src, options)
	}
	return selectFetchedAPKs(results), nil
}

// commitHTTPCache stores the source's validators. A failed write is a
// warning: the fetch succeeded, but the next one cannot answer ErrNoNewAPK.
func commitHTTPCache(src source.Source, options FetchOptions) {
	committer, ok := src.(source.CacheCommitter)
	if !ok {
		return
	}
	if err := committer.CommitCache(); err != nil {
		report(options, "fetch", "warning", "couldn't save the HTTP cache; the next fetch reads the source again", 0, 0)
	}
}

// selectFetchedAPKs keeps, for each app, the greatest version_code whose
// native code includes arm64-v8a. With none, it keeps an APK that has no
// native code. At that version_code a fat APK is dropped when an arm64-only
// APK is present. Several arm64-only files, or more than one app, stay in
// the result so the caller can ask. An app is one package and certificate.
func selectFetchedAPKs(candidates []*APK) []*APK {
	if len(candidates) <= 1 {
		return candidates
	}
	keep := make(map[*APK]struct{}, len(candidates))
	for _, group := range groupAPKs(candidates) {
		for _, chosen := range chooseAppAPKs(group) {
			keep[chosen] = struct{}{}
		}
	}
	selected := make([]*APK, 0, len(keep))
	for _, candidate := range candidates {
		if _, ok := keep[candidate]; ok {
			selected = append(selected, candidate)
			continue
		}
		_ = candidate.Close()
	}
	return selected
}

func groupAPKs(candidates []*APK) [][]*APK {
	var order []string
	groups := make(map[string][]*APK)
	for _, candidate := range candidates {
		key := candidate.AppID + "\x00" + candidate.CertificateHash
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], candidate)
	}
	result := make([][]*APK, 0, len(order))
	for _, key := range order {
		result = append(result, groups[key])
	}
	return result
}

func chooseAppAPKs(candidates []*APK) []*APK {
	pool := filterAPKs(candidates, hasArm64Native)
	if len(pool) == 0 {
		pool = filterAPKs(candidates, func(candidate *APK) bool {
			return len(candidate.Architectures) == 0
		})
	}
	if len(pool) == 0 {
		return candidates
	}
	best := pool[0].VersionCode
	for _, candidate := range pool[1:] {
		if candidate.VersionCode > best {
			best = candidate.VersionCode
		}
	}
	atBest := filterAPKs(pool, func(candidate *APK) bool {
		return candidate.VersionCode == best
	})
	arm64Only := filterAPKs(atBest, func(candidate *APK) bool {
		return onlyArm64(candidate.Architectures)
	})
	if len(arm64Only) > 0 {
		return arm64Only
	}
	return atBest
}

func filterAPKs(candidates []*APK, keep func(*APK) bool) []*APK {
	filtered := make([]*APK, 0, len(candidates))
	for _, candidate := range candidates {
		if keep(candidate) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func hasArm64Native(candidate *APK) bool {
	for _, arch := range candidate.Architectures {
		if arch == "arm64-v8a" {
			return true
		}
	}
	return false
}

func onlyArm64(architectures []string) bool {
	if len(architectures) == 0 {
		return false
	}
	for _, arch := range architectures {
		if arch != "arm64-v8a" {
			return false
		}
	}
	return true
}

// safeAssetName reduces a source-controlled asset name to display-safe text
// for progress output and warnings, which may reach JSON consumers.
func safeAssetName(name string) string {
	return sanitize.Truncate(sanitize.Text(name), 120)
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

func newAPKOwnership(path, tempDir string, managed bool) *apkOwnership {
	return &apkOwnership{path: path, tempDir: tempDir, managed: managed}
}

// Path is the verified APK on disk. It stays valid until Close.
func (apk *APK) Path() (string, error) {
	if apk == nil || apk.ownership == nil {
		return "", operationErr(nil, false, "apk file is not open")
	}
	state := apk.ownership
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.path == "" {
		return "", operationErr(nil, false, "apk file is not open")
	}
	return state.path, nil
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

func (state *apkOwnership) cleanupLocked() error {
	state.closed = true
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
	return state.path, true
}

func (apk *APK) clearHTTPCache() {
	if apk == nil || apk.ownership == nil {
		return
	}
	state := apk.ownership
	state.mu.Lock()
	clear := state.clearHTTPCache
	state.mu.Unlock()
	if clear != nil {
		_ = clear()
	}
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
		if !asset.IsAPK() || source.HasUnsupportedArchitecture(asset.Name) || excludedFilename(asset.Name) {
			continue
		}
		if expression != nil && !expression.MatchString(asset.Name) {
			continue
		}
		result = append(result, asset)
	}
	result = omitGooglePlayCandidates(result)
	// A debug or test-signed APK must not count as the preferred non-fdroid build.
	result = preferWhenAlternativeExists(result, nonProductionFilenamePattern.MatchString)
	result = preferWhenAlternativeExists(result, fdroidFilenamePattern.MatchString)
	return omitUniversalCandidatesWhenArm64Exists(result), nil
}

var excludedFilenamePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(x86_64|x86|armeabi-v7a|armeabi|unsigned|split|config)([^a-z0-9]|$)`)
var unsignedFilenamePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])unsigned([^a-z0-9]|$)`)
var (
	fdroidFilenamePattern        = regexp.MustCompile(`(?i)(^|[^a-z0-9])f-?droid([^a-z0-9]|$)`)
	nonProductionFilenamePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(debug|test[-_]?signed)([^a-z0-9]|$)`)
	googlePlayFilenamePattern    = regexp.MustCompile(`(?i)(^|[^a-z0-9])(google|play|playstore)([^a-z0-9]|$)`)
	arm64FilenamePattern         = regexp.MustCompile(`(?i)(^|[^a-z0-9])arm64-v8a([^a-z0-9]|$)`)
	universalFilenamePattern     = regexp.MustCompile(`(?i)(^|[^a-z0-9])universal([^a-z0-9]|$)`)
)

func excludedFilename(name string) bool {
	return excludedFilenamePattern.MatchString(name)
}

// noCandidateError reports an unsigned APK by name. no_apk is only for a
// source that has no APK left after filtering. Signing is not this package's job.
func noCandidateError(assets []*source.Asset, match string) error {
	name, err := unsignedAPKName(assets, match)
	if err != nil {
		return err
	}
	if name != "" {
		return operationErr(ErrInvalidAPK, false, "unsigned APK %s", name)
	}
	return operationErr(ErrNoAPK, false, "no APK candidates")
}

// unsignedAPKName is an APK the filename filter dropped because its name says
// unsigned. An arm64-v8a name wins. A match that excludes the file yields "".
func unsignedAPKName(assets []*source.Asset, match string) (string, error) {
	var expression *regexp.Regexp
	if match != "" {
		var err error
		expression, err = regexp.Compile(match)
		if err != nil {
			return "", wrapOperationError(ErrInvalidConfig, err, false, "validate match")
		}
	}
	var names []string
	for _, asset := range assets {
		if asset == nil || !asset.IsAPK() {
			continue
		}
		if expression != nil && !expression.MatchString(asset.Name) {
			continue
		}
		if !unsignedFilenamePattern.MatchString(asset.Name) {
			continue
		}
		names = append(names, asset.Name)
	}
	for _, name := range names {
		if arm64FilenamePattern.MatchString(name) {
			return name, nil
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	return names[0], nil
}

func omitGooglePlayCandidates(candidates []*source.Asset) []*source.Asset {
	return filterAssets(candidates, func(candidate *source.Asset) bool {
		return !googlePlayFilenamePattern.MatchString(candidate.Name)
	})
}

// preferWhenAlternativeExists drops names matched by excluded when any other
// candidate remains. Matched names stay when they are the only candidates.
func preferWhenAlternativeExists(candidates []*source.Asset, excluded func(string) bool) []*source.Asset {
	for _, candidate := range candidates {
		if !excluded(candidate.Name) {
			return filterAssets(candidates, func(candidate *source.Asset) bool {
				return !excluded(candidate.Name)
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

func sourceErrorClassification(err error) (error, bool) {
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return ErrTemporaryFailure, true
	case err == nil:
		return ErrSourceFailed, false
	case errors.Is(err, source.ErrRateLimited):
		return ErrRateLimited, true
	case source.IsTransient(err):
		return ErrTemporaryFailure, true
	}
	return ErrSourceFailed, false
}

func closeAPKs(apks []*APK) {
	for _, apk := range apks {
		_ = apk.Close()
	}
}
