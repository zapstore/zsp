// Package nostr handles Nostr event generation and signing.
package nostr

import (
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/config"
)

// Event kinds for Zapstore
const (
	KindAppMetadata   = 32267 // Software Application (name, description, icon, platforms)
	KindRelease       = 30063 // Software Release (version, changelog, asset links)
	KindSoftwareAsset = 3063  // Software Asset (hash, size, URLs, cert hash, platforms)
	KindBlossomAuth   = 24242 // Blossom upload authorization
	KindIdentityProof = 30509 // NIP-C1 Cryptographic Identity Proof (SPKI)
)

// AppMetadata contains Software Application metadata (kind 32267).
type AppMetadata struct {
	PackageID   string
	Name        string
	Description string
	Summary     string
	Website     string
	License     string
	Repository  string   // Repository URL (for display)
	NIP34Repo   string   // NIP-34 repository pointer (a tag): "30617:pubkey:identifier"
	NIP34Relay  string   // Relay hint for NIP-34 pointer
	Tags        []string // Category tags
	IconURL     string   // Blossom URL for icon
	ImageURLs   []string // Screenshot URLs
	Platforms   []string // Platform identifiers (e.g., "android-arm64-v8a")
}

// ReleaseMetadata contains Software Release metadata (kind 30063).
type ReleaseMetadata struct {
	PackageID      string
	Version        string
	VersionCode    int64
	Changelog      string   // Release notes (content field)
	Channel        string   // Release channel: main, beta, nightly, dev
	AssetEventIDs  []string // Event IDs of asset events (kind 3063)
	AssetRelayHint string   // Optional relay hint for asset events
	Commit         string   // Git commit hash
	Platforms      []string // Platform identifiers (e.g., "android-arm64-v8a")
}

// AssetMetadata contains Software Asset metadata (kind 3063).
type AssetMetadata struct {
	Identifier            string // Asset identifier (may differ from app identifier)
	Version               string
	VersionCode           int64
	SHA256                string
	Size                  int64
	URLs                  []string // Download URLs (Blossom)
	CertFingerprint       string   // APK signing certificate SHA256
	MinSDK                int32
	TargetSDK             int32
	Platforms             []string // Full platform identifiers (e.g., "android-arm64-v8a")
	Filename              string   // Original filename (for variant detection)
	Commit                string   // Git commit hash for reproducible builds
	SupportedNIPs         []string // Supported Nostr NIPs
	MinAllowedVersion     string   // Minimum allowed version string
	MinAllowedVersionCode int64    // Minimum allowed version code
}

// EventSet contains all events to be published for an app release.
type EventSet struct {
	AppMetadata    *nostr.Event
	Release        *nostr.Event
	SoftwareAssets []*nostr.Event // Multiple assets (e.g., different APK variants)
	IdentityProof  *nostr.Event   // Optional NIP-C1 identity proof (kind 30509)
}

// BuildAppMetadataEvent creates a Software Application event (kind 32267).
func BuildAppMetadataEvent(meta *AppMetadata, pubkey string) *nostr.Event {
	tags := nostr.Tags{}

	tags = append(tags, nostr.Tag{"d", meta.PackageID})
	tags = append(tags, nostr.Tag{"name", meta.Name})

	if meta.Summary != "" {
		tags = append(tags, nostr.Tag{"summary", meta.Summary})
	}
	if meta.IconURL != "" {
		tags = append(tags, nostr.Tag{"icon", meta.IconURL})
	}
	for _, url := range canonicalStrings(meta.ImageURLs) {
		tags = append(tags, nostr.Tag{"image", url})
	}
	for _, tag := range canonicalStrings(meta.Tags) {
		tags = append(tags, nostr.Tag{"t", tag})
	}
	if meta.Website != "" {
		tags = append(tags, nostr.Tag{"url", meta.Website})
	}
	if meta.Repository != "" {
		tags = append(tags, nostr.Tag{"repository", meta.Repository})
	}
	// NIP-34 repository pointer (a tag)
	if meta.NIP34Repo != "" {
		if meta.NIP34Relay != "" {
			tags = append(tags, nostr.Tag{"a", meta.NIP34Repo, meta.NIP34Relay})
		} else {
			tags = append(tags, nostr.Tag{"a", meta.NIP34Repo})
		}
	}
	// Platform identifiers (f tags) - REQUIRED per NIP-82
	for _, platform := range canonicalStrings(meta.Platforms) {
		tags = append(tags, nostr.Tag{"f", platform})
	}
	if meta.License != "" {
		tags = append(tags, nostr.Tag{"license", meta.License})
	}

	return &nostr.Event{
		Kind:      KindAppMetadata,
		PubKey:    pubkey,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
		Content:   meta.Description, // Description goes in content per NIP-82
	}
}

// SameAppMetadata reports whether two application events have the same body.
// created_at, id, and sig are ignored. Tag order is ignored.
func SameAppMetadata(a, b *nostr.Event) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind || a.PubKey != b.PubKey || a.Content != b.Content {
		return false
	}
	return equalTags(a.Tags, b.Tags)
}

func equalTags(a, b nostr.Tags) bool {
	left := sortedTags(a)
	right := sortedTags(b)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !slices.Equal(left[index], right[index]) {
			return false
		}
	}
	return true
}

func sortedTags(tags nostr.Tags) nostr.Tags {
	result := make(nostr.Tags, len(tags))
	for index, tag := range tags {
		result[index] = slices.Clone(tag)
	}
	slices.SortFunc(result, compareTags)
	return result
}

func compareTags(a, b nostr.Tag) int {
	limit := min(len(a), len(b))
	for index := 0; index < limit; index++ {
		if cmp := strings.Compare(a[index], b[index]); cmp != 0 {
			return cmp
		}
	}
	return len(a) - len(b)
}

// versionOrCode returns version, or the decimal version_code when version is empty.
// Kinds 30063 and 3063 must always carry a non-empty version tag.
func versionOrCode(version string, versionCode int64) string {
	if version != "" {
		return version
	}
	return strconv.FormatInt(versionCode, 10)
}

// BuildReleaseEvent creates a Software Release event (kind 30063).
func BuildReleaseEvent(meta *ReleaseMetadata, pubkey string) *nostr.Event {
	tags := nostr.Tags{}

	// Channel defaults to "main" if not specified
	channel := meta.Channel
	if channel == "" {
		channel = "main"
	}

	version := versionOrCode(meta.Version, meta.VersionCode)

	tags = append(tags,
		nostr.Tag{"a", "32267:" + pubkey + ":" + meta.PackageID},
		nostr.Tag{"i", meta.PackageID},
		nostr.Tag{"version", version},
		nostr.Tag{"version_code", strconv.FormatInt(meta.VersionCode, 10)},
		nostr.Tag{"d", meta.PackageID + "@" + version},
		nostr.Tag{"c", channel},
	)

	// Platform identifiers (f tags) - same as kind 32267
	for _, platform := range canonicalStrings(meta.Platforms) {
		tags = append(tags, nostr.Tag{"f", platform})
	}

	// Asset event references (e tags)
	for _, eventID := range canonicalStrings(meta.AssetEventIDs) {
		if meta.AssetRelayHint != "" {
			tags = append(tags, nostr.Tag{"e", eventID, meta.AssetRelayHint})
		} else {
			tags = append(tags, nostr.Tag{"e", eventID})
		}
	}

	return &nostr.Event{
		Kind:      KindRelease,
		PubKey:    pubkey,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
		Content:   meta.Changelog, // Release notes go in content per NIP-82
	}
}

// BuildSoftwareAssetEvent creates a Software Asset event (kind 3063).
func BuildSoftwareAssetEvent(meta *AssetMetadata, pubkey string) *nostr.Event {
	tags := nostr.Tags{}

	tags = append(tags,
		nostr.Tag{"i", meta.Identifier},
		nostr.Tag{"x", meta.SHA256},
		nostr.Tag{"version", versionOrCode(meta.Version, meta.VersionCode)},
	)

	// Download URLs
	for _, url := range canonicalStrings(meta.URLs) {
		tags = append(tags, nostr.Tag{"url", url})
	}

	// MIME type
	tags = append(tags, nostr.Tag{"m", "application/vnd.android.package-archive"})

	// File size
	if meta.Size > 0 {
		tags = append(tags, nostr.Tag{"size", strconv.FormatInt(meta.Size, 10)})
	}

	// Platform identifiers (f tags) - REQUIRED per NIP-82
	for _, platform := range canonicalStrings(meta.Platforms) {
		tags = append(tags, nostr.Tag{"f", platform})
	}

	// Platform version info
	if meta.MinSDK > 0 {
		tags = append(tags, nostr.Tag{"min_platform_version", strconv.Itoa(int(meta.MinSDK))})
	}
	if meta.TargetSDK > 0 {
		tags = append(tags, nostr.Tag{"target_platform_version", strconv.Itoa(int(meta.TargetSDK))})
	}

	// Filename for variant detection (fallback when no explicit variant)
	if meta.Filename != "" {
		tags = append(tags, nostr.Tag{"filename", meta.Filename})
	}

	// Git commit hash for reproducible builds
	if meta.Commit != "" {
		tags = append(tags, nostr.Tag{"commit", meta.Commit})
	}

	// Supported NIPs
	for _, nip := range canonicalStrings(meta.SupportedNIPs) {
		tags = append(tags, nostr.Tag{"supported_nip", nip})
	}

	// Android-specific tags
	tags = append(tags, nostr.Tag{"version_code", strconv.FormatInt(meta.VersionCode, 10)})

	// Minimum allowed version
	if meta.MinAllowedVersion != "" {
		tags = append(tags, nostr.Tag{"min_allowed_version", meta.MinAllowedVersion})
	}
	if meta.MinAllowedVersionCode > 0 {
		tags = append(tags, nostr.Tag{"min_allowed_version_code", strconv.FormatInt(meta.MinAllowedVersionCode, 10)})
	}

	// APK certificate hash - REQUIRED for Android per NIP-82
	if meta.CertFingerprint != "" {
		tags = append(tags, nostr.Tag{"apk_certificate_hash", meta.CertFingerprint})
	}

	return &nostr.Event{
		Kind:      KindSoftwareAsset,
		PubKey:    pubkey,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
		Content:   "",
	}
}

// BuildBlossomAuthEvent creates a kind 24242 event for Blossom upload authorization.
// server is the lowercase Blossom host when the authorization is server-scoped.
func BuildBlossomAuthEvent(fileHash string, pubkey string, expiration time.Time, server ...string) *nostr.Event {
	tags := nostr.Tags{
		{"t", "upload"},
		{"x", fileHash},
		{"expiration", strconv.FormatInt(expiration.Unix(), 10)},
	}
	if len(server) > 0 && server[0] != "" {
		tags = append(tags, nostr.Tag{"server", server[0]})
	}

	return &nostr.Event{
		Kind:      KindBlossomAuth,
		PubKey:    pubkey,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
		Content:   "Upload " + fileHash,
	}
}

// archToPlatform converts Android architecture names to NIP-82 platform identifiers.
func archToPlatform(arch string) string {
	switch arch {
	case "arm64-v8a":
		return "android-arm64-v8a"
	case "armeabi-v7a":
		return "android-armeabi-v7a"
	case "x86":
		return "android-x86"
	case "x86_64":
		return "android-x86_64"
	default:
		return "android-" + arch
	}
}

func canonicalStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	return compactStrings(result)
}

// BuildEventSetParams contains parameters for building an event set.
type BuildEventSetParams struct {
	APKInfo     *apk.APKInfo
	Config      *config.Config
	Pubkey      string
	OriginalURL string // Original download URL (from release source)
	// BlossomURL is retained for source compatibility. Asset URL tags are
	// deliberately built from OriginalURL only.
	BlossomURL       string
	IconURL          string
	ImageURLs        []string
	Changelog        string    // Release notes (from remote source or local file)
	Commit           string    // Git commit hash for reproducible builds
	Channel          string    // Release channel: main (default), beta, nightly, dev
	ReleaseTimestamp time.Time // Release publish date (zero means use current time)
	// UseReleaseTimestampForApp sets kind 32267 created_at to ReleaseTimestamp.
	// When false, app metadata keeps current-time created_at.
	UseReleaseTimestampForApp bool
	// MinReleaseTimestamp ensures Release.CreatedAt is strictly greater than this value.
	// Used with --overwrite-release to guarantee NIP-33 replacement when the relay
	// has an existing event with the same or newer timestamp.
	MinReleaseTimestamp time.Time
}

// BuildEventSet creates all events for an APK release.
func BuildEventSet(params BuildEventSetParams) *EventSet {
	apkInfo := params.APKInfo
	cfg := params.Config

	// Determine app name
	name := cfg.Name
	if name == "" {
		name = apkInfo.Label
	}
	if name == "" {
		name = apkInfo.PackageID
	}

	// Asset events may contain a stable source URL. Blossom discovery uses the
	// content hash and configured server, so publishing its derived URL here
	// would create a second, non-source URL tag.
	var apkURLs []string
	if params.OriginalURL != "" {
		apkURLs = append(apkURLs, params.OriginalURL)
	}
	// Convert architectures to platform identifiers
	platforms := make([]string, 0, len(apkInfo.Architectures))
	for _, arch := range apkInfo.Architectures {
		platforms = append(platforms, archToPlatform(arch))
	}
	sort.Strings(platforms)
	if len(platforms) > 0 {
		platforms = compactStrings(platforms)
	}
	// Build NIP-34 repository pointer if available
	var nip34Repo, nip34Relay string
	if cfg.NIP34Repo != nil {
		// Format: "30617:pubkey:identifier"
		nip34Repo = "30617:" + cfg.NIP34Repo.Pubkey + ":" + cfg.NIP34Repo.Identifier
		if len(cfg.NIP34Repo.Relays) > 0 {
			nip34Relay = cfg.NIP34Repo.Relays[0]
		}
	}

	// Software Application event
	appMeta := &AppMetadata{
		PackageID:   apkInfo.PackageID,
		Name:        name,
		Description: cfg.Description,
		Summary:     cfg.Summary,
		Website:     cfg.Website,
		License:     cfg.License,
		Repository:  cfg.Repository,
		NIP34Repo:   nip34Repo,
		NIP34Relay:  nip34Relay,
		Tags:        cfg.Tags,
		IconURL:     params.IconURL,
		ImageURLs:   params.ImageURLs,
		Platforms:   platforms,
	}

	// Determine release channel (default: main)
	channel := params.Channel
	if channel == "" {
		channel = "main"
	}

	// Software Release event
	releaseMeta := &ReleaseMetadata{
		PackageID:     apkInfo.PackageID,
		Version:       apkInfo.VersionName,
		VersionCode:   apkInfo.VersionCode,
		Changelog:     params.Changelog,
		Channel:       channel,
		AssetEventIDs: []string{},
		Commit:        params.Commit,
		Platforms:     platforms,
	}

	// Software Asset event
	assetMeta := &AssetMetadata{
		Identifier:            apkInfo.PackageID, // Asset ID same as app ID for APKs
		Version:               apkInfo.VersionName,
		VersionCode:           apkInfo.VersionCode,
		SHA256:                apkInfo.SHA256,
		Size:                  apkInfo.FileSize,
		URLs:                  apkURLs,
		CertFingerprint:       apkInfo.CertFingerprint,
		MinSDK:                apkInfo.MinSDK,
		TargetSDK:             apkInfo.TargetSDK,
		Platforms:             platforms,
		Filename:              filepath.Base(apkInfo.FilePath),
		Commit:                params.Commit,
		SupportedNIPs:         cfg.SupportedNIPs,
		MinAllowedVersion:     cfg.MinAllowedVersion,
		MinAllowedVersionCode: cfg.MinAllowedVersionCode,
	}

	eventSet := &EventSet{
		AppMetadata:    BuildAppMetadataEvent(appMeta, params.Pubkey),
		Release:        BuildReleaseEvent(releaseMeta, params.Pubkey),
		SoftwareAssets: []*nostr.Event{BuildSoftwareAssetEvent(assetMeta, params.Pubkey)},
	}
	eventSet.UpdateReleasePlatforms()

	// If a release timestamp is provided, use it for release and asset events
	// by default. Optionally, app metadata can also use the release timestamp.
	if !params.ReleaseTimestamp.IsZero() {
		ts := nostr.Timestamp(params.ReleaseTimestamp.Unix())
		eventSet.Release.CreatedAt = ts
		for _, asset := range eventSet.SoftwareAssets {
			asset.CreatedAt = ts
		}
		if params.UseReleaseTimestampForApp {
			eventSet.AppMetadata.CreatedAt = ts
		}
	}

	// When overwriting a release, ensure created_at is strictly greater than the
	// existing event's timestamp so the relay's NIP-33 replacement guard fires.
	if !params.MinReleaseTimestamp.IsZero() {
		minTS := nostr.Timestamp(params.MinReleaseTimestamp.Unix())
		if eventSet.Release.CreatedAt <= minTS {
			bumpTS := minTS + 1
			eventSet.Release.CreatedAt = bumpTS
			for _, asset := range eventSet.SoftwareAssets {
				asset.CreatedAt = bumpTS
			}
		}
	}

	return eventSet
}

// AddAssetReference adds an asset event ID reference to the Release event.
func (es *EventSet) AddAssetReference(assetEventID string, relayHint string) {
	if relayHint != "" {
		es.Release.Tags = append(es.Release.Tags, nostr.Tag{"e", assetEventID, relayHint})
	} else {
		es.Release.Tags = append(es.Release.Tags, nostr.Tag{"e", assetEventID})
	}
}

// FinalizeEventSet fills every deterministic event ID and release reference
// before preview or signing. Signing must not change the resulting event body.
func FinalizeEventSet(events *EventSet, relayHint string) {
	events.SetApplicationRelayHint(relayHint)
	for _, asset := range events.SoftwareAssets {
		asset.ID = asset.GetID()
		events.AddAssetReference(asset.ID, relayHint)
	}
	events.Release.ID = events.Release.GetID()
	if events.AppMetadata != nil {
		events.AppMetadata.ID = events.AppMetadata.GetID()
	}
	if events.IdentityProof != nil {
		events.IdentityProof.ID = events.IdentityProof.GetID()
	}
}

// SetApplicationRelayHint adds the configured publication relay to the
// release event's required application coordinate.
func (es *EventSet) SetApplicationRelayHint(relayHint string) {
	if relayHint == "" {
		return
	}
	for index, tag := range es.Release.Tags {
		if len(tag) >= 2 && tag[0] == "a" {
			es.Release.Tags[index] = nostr.Tag{"a", tag[1], relayHint}
			return
		}
	}
}

// SetApplicationPubkey points the release at an existing application event
// owner without changing the release signer's pubkey.
func (es *EventSet) SetApplicationPubkey(pubkey string) {
	if pubkey == "" {
		return
	}
	for index, tag := range es.Release.Tags {
		if len(tag) >= 2 && tag[0] == "a" {
			parts := strings.SplitN(tag[1], ":", 3)
			if len(parts) != 3 {
				return
			}
			tag[1] = parts[0] + ":" + pubkey + ":" + parts[2]
			es.Release.Tags[index] = tag
			return
		}
	}
}

// UpdateReleasePlatforms aggregates platform identifiers (f tags) from all Software Assets
// and updates the Release event. This should be called after all assets are added to the EventSet
// but before the Release event is signed. This is useful when publishing multiple APK assets
// in a single release, where each asset may support different architectures.
func (es *EventSet) UpdateReleasePlatforms() {
	// Collect unique platforms from all assets
	platformSet := make(map[string]bool)
	for _, asset := range es.SoftwareAssets {
		// Extract f tags from asset
		for _, tag := range asset.Tags {
			if len(tag) > 1 && tag[0] == "f" {
				platformSet[tag[1]] = true
			}
		}
	}

	// Remove existing f tags from Release event
	newTags := nostr.Tags{}
	for _, tag := range es.Release.Tags {
		if len(tag) > 0 && tag[0] != "f" {
			newTags = append(newTags, tag)
		}
	}

	// Find the position to insert f tags (after c tag, before e tags)
	insertPos := 0
	for i, tag := range newTags {
		if len(tag) > 0 && tag[0] == "c" {
			insertPos = i + 1
			break
		}
	}

	// Insert f tags at the correct position
	platforms := make([]string, 0, len(platformSet))
	for platform := range platformSet {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	for _, platform := range platforms {
		fTag := nostr.Tag{"f", platform}
		newTags = append(newTags[:insertPos], append(nostr.Tags{fTag}, newTags[insertPos:]...)...)
		insertPos++
	}

	es.Release.Tags = newTags
}
