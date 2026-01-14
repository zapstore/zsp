// Package nostr handles Nostr event generation and signing.
package nostr

import (
	"path/filepath"
	"strconv"
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
)

// AppMetadata contains Software Application metadata (kind 32267).
type AppMetadata struct {
	PackageID   string
	Name        string
	Description string
	Summary     string
	Website     string
	License     string
	Repository  string
	Tags        []string
	IconURL     string   // Blossom URL for icon
	ImageURLs   []string // Screenshot URLs
	Platforms   []string // Platform identifiers (e.g., "android-arm64-v8a")
}

// ReleaseMetadata contains Software Release metadata (kind 30063).
type ReleaseMetadata struct {
	PackageID      string
	Version        string
	VersionCode    int64
	Changelog      string
	Channel        string   // Release channel: main, beta, nightly, dev
	AssetEventIDs  []string // Event IDs of asset events (kind 3063)
	AssetRelayHint string   // Optional relay hint for asset events
}

// AssetMetadata contains Software Asset metadata (kind 3063).
type AssetMetadata struct {
	Identifier      string // Asset identifier (may differ from app identifier)
	Version         string
	VersionCode     int64
	SHA256          string
	Size            int64
	URLs            []string // Download URLs (Blossom)
	CertFingerprint string   // APK signing certificate SHA256
	MinSDK          int32
	TargetSDK       int32
	Platforms       []string // Full platform identifiers (e.g., "android-arm64-v8a")
	Filename        string   // Original filename (for variant detection)
}

// EventSet contains all events to be published for an app release.
type EventSet struct {
	AppMetadata    *nostr.Event
	Release        *nostr.Event
	SoftwareAssets []*nostr.Event // Multiple assets (e.g., different APK variants)
}

// BuildAppMetadataEvent creates a Software Application event (kind 32267).
func BuildAppMetadataEvent(meta *AppMetadata, pubkey string) *nostr.Event {
	tags := nostr.Tags{
		{"d", meta.PackageID},
		{"name", meta.Name},
	}

	if meta.Summary != "" {
		tags = append(tags, nostr.Tag{"summary", meta.Summary})
	}
	if meta.IconURL != "" {
		tags = append(tags, nostr.Tag{"icon", meta.IconURL})
	}
	for _, url := range meta.ImageURLs {
		tags = append(tags, nostr.Tag{"image", url})
	}
	for _, tag := range meta.Tags {
		tags = append(tags, nostr.Tag{"t", tag})
	}
	if meta.Website != "" {
		tags = append(tags, nostr.Tag{"url", meta.Website})
	}
	if meta.Repository != "" {
		tags = append(tags, nostr.Tag{"repository", meta.Repository})
	}
	// Platform identifiers (f tags) - REQUIRED per NIP-82
	for _, platform := range meta.Platforms {
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

// BuildReleaseEvent creates a Software Release event (kind 30063).
func BuildReleaseEvent(meta *ReleaseMetadata, pubkey string) *nostr.Event {
	// Channel defaults to "main" if not specified
	channel := meta.Channel
	if channel == "" {
		channel = "main"
	}

	tags := nostr.Tags{
		{"i", meta.PackageID},
		{"version", meta.Version},
		{"d", meta.PackageID + "@" + meta.Version},
		{"c", channel},
	}

	// Asset event references (e tags)
	for _, eventID := range meta.AssetEventIDs {
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
	tags := nostr.Tags{
		{"i", meta.Identifier},
		{"x", meta.SHA256},
		{"version", meta.Version},
	}

	// Download URLs
	for _, url := range meta.URLs {
		tags = append(tags, nostr.Tag{"url", url})
	}

	// MIME type
	tags = append(tags, nostr.Tag{"m", "application/vnd.android.package-archive"})

	// File size
	if meta.Size > 0 {
		tags = append(tags, nostr.Tag{"size", strconv.FormatInt(meta.Size, 10)})
	}

	// Platform identifiers (f tags) - REQUIRED per NIP-82
	for _, platform := range meta.Platforms {
		tags = append(tags, nostr.Tag{"f", platform})
	}

	// Platform version info
	if meta.MinSDK > 0 {
		tags = append(tags, nostr.Tag{"min_platform_version", strconv.Itoa(int(meta.MinSDK))})
	}
	if meta.TargetSDK > 0 {
		tags = append(tags, nostr.Tag{"target_platform_version", strconv.Itoa(int(meta.TargetSDK))})
	}

	// Filename for variant detection
	if meta.Filename != "" {
		tags = append(tags, nostr.Tag{"filename", meta.Filename})
	}

	// Android-specific tags
	tags = append(tags, nostr.Tag{"version_code", strconv.FormatInt(meta.VersionCode, 10)})

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
func BuildBlossomAuthEvent(fileHash string, pubkey string, expiration time.Time) *nostr.Event {
	tags := nostr.Tags{
		{"t", "upload"},
		{"x", fileHash},
		{"expiration", strconv.FormatInt(expiration.Unix(), 10)},
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

// BuildEventSetParams contains parameters for building an event set.
type BuildEventSetParams struct {
	APKInfo    *apk.APKInfo
	Config     *config.Config
	Pubkey     string
	BlossomURL string
	IconURL    string
	ImageURLs  []string
	Changelog  string // Release notes (from remote source or local file)
}

// BuildEventSet creates all events for an APK release.
// The Release event's asset references (e tags) are populated by SignEventSet
// after the asset event is signed.
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

	// Build APK URL
	apkURL := ""
	if params.BlossomURL != "" {
		apkURL = params.BlossomURL + "/" + apkInfo.SHA256
	}

	// Convert architectures to platform identifiers
	platforms := make([]string, 0, len(apkInfo.Architectures))
	for _, arch := range apkInfo.Architectures {
		platforms = append(platforms, archToPlatform(arch))
	}
	// If no native libs, it's architecture-independent - support all Android platforms
	if len(platforms) == 0 {
		platforms = []string{"android-arm64-v8a", "android-armeabi-v7a", "android-x86", "android-x86_64"}
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
		Tags:        cfg.Tags,
		IconURL:     params.IconURL,
		ImageURLs:   params.ImageURLs,
		Platforms:   platforms,
	}

	// Software Release event
	// AssetEventIDs will be populated by SignEventSet after asset is signed
	releaseMeta := &ReleaseMetadata{
		PackageID:     apkInfo.PackageID,
		Version:       apkInfo.VersionName,
		VersionCode:   apkInfo.VersionCode,
		Changelog:     params.Changelog,
		Channel:       "main",
		AssetEventIDs: []string{}, // Populated after signing
	}

	// Software Asset event
	assetMeta := &AssetMetadata{
		Identifier:      apkInfo.PackageID, // Asset ID same as app ID for APKs
		Version:         apkInfo.VersionName,
		VersionCode:     apkInfo.VersionCode,
		SHA256:          apkInfo.SHA256,
		Size:            apkInfo.FileSize,
		URLs:            []string{apkURL},
		CertFingerprint: apkInfo.CertFingerprint,
		MinSDK:          apkInfo.MinSDK,
		TargetSDK:       apkInfo.TargetSDK,
		Platforms:       platforms,
		Filename:        filepath.Base(apkInfo.FilePath),
	}

	return &EventSet{
		AppMetadata:    BuildAppMetadataEvent(appMeta, params.Pubkey),
		Release:        BuildReleaseEvent(releaseMeta, params.Pubkey),
		SoftwareAssets: []*nostr.Event{BuildSoftwareAssetEvent(assetMeta, params.Pubkey)},
	}
}

// AddAssetReference adds an asset event ID reference to the Release event.
// This must be called after the asset event is signed but before the release is signed.
func (es *EventSet) AddAssetReference(assetEventID string, relayHint string) {
	if relayHint != "" {
		es.Release.Tags = append(es.Release.Tags, nostr.Tag{"e", assetEventID, relayHint})
	} else {
		es.Release.Tags = append(es.Release.Tags, nostr.Tag{"e", assetEventID})
	}
}

// AddAssetReferences adds all asset event ID references to the Release event.
// This must be called after the asset events are signed but before the release is signed.
func (es *EventSet) AddAssetReferences(relayHint string) {
	for _, asset := range es.SoftwareAssets {
		es.AddAssetReference(asset.ID, relayHint)
	}
}
