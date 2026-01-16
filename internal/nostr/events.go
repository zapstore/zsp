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

	// Legacy kind used by older relay.zapstore.dev format
	KindFileMetadataLegacy = 1063 // NIP-94 File Metadata (legacy asset format)
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

	// Legacy format fields (for old relay.zapstore.dev compatibility)
	LegacyFormat   bool   // Enable legacy format
	ReleaseVersion string // Version for the a-tag pointing to release (legacy only)
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

	// Legacy format fields (for old relay.zapstore.dev compatibility)
	LegacyFormat bool   // Enable legacy format
	ReleaseURL   string // Release page URL (url/r tags in legacy mode)
	Commit       string // Git commit hash (in release for legacy, in asset for new)
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
	Variant               string   // Explicit variant name (e.g., "fdroid", "google")
	Commit                string   // Git commit hash for reproducible builds
	Permissions           []string // Android permissions
	SupportedNIPs         []string // Supported Nostr NIPs
	MinAllowedVersion     string   // Minimum allowed version string
	MinAllowedVersionCode int64    // Minimum allowed version code

	// Legacy format fields (for old relay.zapstore.dev compatibility)
	LegacyFormat bool // Enable legacy format (kind 1063 with different tags)
}

// EventSet contains all events to be published for an app release.
type EventSet struct {
	AppMetadata    *nostr.Event
	Release        *nostr.Event
	SoftwareAssets []*nostr.Event // Multiple assets (e.g., different APK variants)
}

// BuildAppMetadataEvent creates a Software Application event (kind 32267).
func BuildAppMetadataEvent(meta *AppMetadata, pubkey string) *nostr.Event {
	tags := nostr.Tags{}

	if meta.LegacyFormat {
		// Legacy format: different tag order, a-tag points to release
		tags = append(tags, nostr.Tag{"name", meta.Name})
		tags = append(tags, nostr.Tag{"d", meta.PackageID})
		if meta.Repository != "" {
			tags = append(tags, nostr.Tag{"repository", meta.Repository})
		}
		// Platform identifiers (f tags)
		for _, platform := range meta.Platforms {
			tags = append(tags, nostr.Tag{"f", platform})
		}
		if meta.License != "" {
			tags = append(tags, nostr.Tag{"license", meta.License})
		}
		if meta.IconURL != "" {
			tags = append(tags, nostr.Tag{"icon", meta.IconURL})
		}
		// Legacy format: a-tag points to latest release (30063)
		if meta.ReleaseVersion != "" {
			releaseRef := "30063:" + pubkey + ":" + meta.PackageID + "@" + meta.ReleaseVersion
			tags = append(tags, nostr.Tag{"a", releaseRef})
		}
	} else {
		// New format
		tags = append(tags, nostr.Tag{"d", meta.PackageID})
		tags = append(tags, nostr.Tag{"name", meta.Name})

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
		// NIP-34 repository pointer (a tag)
		if meta.NIP34Repo != "" {
			if meta.NIP34Relay != "" {
				tags = append(tags, nostr.Tag{"a", meta.NIP34Repo, meta.NIP34Relay})
			} else {
				tags = append(tags, nostr.Tag{"a", meta.NIP34Repo})
			}
		}
		// Platform identifiers (f tags) - REQUIRED per NIP-82
		for _, platform := range meta.Platforms {
			tags = append(tags, nostr.Tag{"f", platform})
		}
		if meta.License != "" {
			tags = append(tags, nostr.Tag{"license", meta.License})
		}
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
	tags := nostr.Tags{}

	if meta.LegacyFormat {
		// Legacy format: different tags, a-tag points back to app metadata
		if meta.ReleaseURL != "" {
			tags = append(tags, nostr.Tag{"url", meta.ReleaseURL})
			tags = append(tags, nostr.Tag{"r", meta.ReleaseURL})
		}
		if meta.Commit != "" {
			tags = append(tags, nostr.Tag{"commit", meta.Commit})
		}
		tags = append(tags, nostr.Tag{"d", meta.PackageID + "@" + meta.Version})

		// Asset event references (e tags)
		for _, eventID := range meta.AssetEventIDs {
			if meta.AssetRelayHint != "" {
				tags = append(tags, nostr.Tag{"e", eventID, meta.AssetRelayHint})
			} else {
				tags = append(tags, nostr.Tag{"e", eventID})
			}
		}

		// Legacy format: a-tag points back to app metadata (32267)
		appRef := "32267:" + pubkey + ":" + meta.PackageID
		tags = append(tags, nostr.Tag{"a", appRef})
	} else {
		// New format
		// Channel defaults to "main" if not specified
		channel := meta.Channel
		if channel == "" {
			channel = "main"
		}

		tags = append(tags,
			nostr.Tag{"i", meta.PackageID},
			nostr.Tag{"version", meta.Version},
			nostr.Tag{"d", meta.PackageID + "@" + meta.Version},
			nostr.Tag{"c", channel},
		)

		// Asset event references (e tags)
		for _, eventID := range meta.AssetEventIDs {
			if meta.AssetRelayHint != "" {
				tags = append(tags, nostr.Tag{"e", eventID, meta.AssetRelayHint})
			} else {
				tags = append(tags, nostr.Tag{"e", eventID})
			}
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

// BuildSoftwareAssetEvent creates a Software Asset event (kind 3063 or 1063 in legacy mode).
func BuildSoftwareAssetEvent(meta *AssetMetadata, pubkey string) *nostr.Event {
	tags := nostr.Tags{}
	kind := KindSoftwareAsset
	content := ""

	if meta.LegacyFormat {
		// Legacy format: kind 1063, different tag names, content = "packageId@version"
		kind = KindFileMetadataLegacy
		content = meta.Identifier + "@" + meta.Version

		// Platform identifiers (f tags)
		for _, platform := range meta.Platforms {
			tags = append(tags, nostr.Tag{"f", platform})
		}

		// APK certificate hash - legacy uses apk_signature_hash
		if meta.CertFingerprint != "" {
			tags = append(tags, nostr.Tag{"apk_signature_hash", meta.CertFingerprint})
		}

		tags = append(tags, nostr.Tag{"version", meta.Version})
		tags = append(tags, nostr.Tag{"version_code", strconv.FormatInt(meta.VersionCode, 10)})

		// Platform version info - legacy uses min_sdk_version/target_sdk_version
		if meta.MinSDK > 0 {
			tags = append(tags, nostr.Tag{"min_sdk_version", strconv.Itoa(int(meta.MinSDK))})
		}
		if meta.TargetSDK > 0 {
			tags = append(tags, nostr.Tag{"target_sdk_version", strconv.Itoa(int(meta.TargetSDK))})
		}

		// MIME type
		tags = append(tags, nostr.Tag{"m", "application/vnd.android.package-archive"})

		// SHA256 hash
		tags = append(tags, nostr.Tag{"x", meta.SHA256})

		// File size
		if meta.Size > 0 {
			tags = append(tags, nostr.Tag{"size", strconv.FormatInt(meta.Size, 10)})
		}

		// Download URLs
		for _, url := range meta.URLs {
			tags = append(tags, nostr.Tag{"url", url})
		}

		// Note: legacy format doesn't include filename, variant, commit, permissions, etc.
	} else {
		// New format: kind 3063
		tags = append(tags,
			nostr.Tag{"i", meta.Identifier},
			nostr.Tag{"x", meta.SHA256},
			nostr.Tag{"version", meta.Version},
		)

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

		// Filename for variant detection (fallback when no explicit variant)
		if meta.Filename != "" {
			tags = append(tags, nostr.Tag{"filename", meta.Filename})
		}

		// Explicit variant name
		if meta.Variant != "" {
			tags = append(tags, nostr.Tag{"variant", meta.Variant})
		}

		// Git commit hash for reproducible builds
		if meta.Commit != "" {
			tags = append(tags, nostr.Tag{"commit", meta.Commit})
		}

		// Android permissions
		for _, perm := range meta.Permissions {
			tags = append(tags, nostr.Tag{"permission", perm})
		}

		// Supported NIPs
		for _, nip := range meta.SupportedNIPs {
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
	}

	return &nostr.Event{
		Kind:      kind,
		PubKey:    pubkey,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
		Content:   content,
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
	APKInfo      *apk.APKInfo
	Config       *config.Config
	Pubkey       string
	OriginalURL  string // Original download URL (from release source)
	IconURL      string
	ImageURLs    []string
	Changelog    string // Release notes (from remote source or local file)
	Variant      string // Explicit variant name (from config variants map)
	Commit       string // Git commit hash for reproducible builds
	ReleaseURL   string // Release page URL (for legacy format url/r tags)
	LegacyFormat bool   // Use legacy event format (kind 1063, different tags)
}

// BuildEventSet creates all events for an APK release.
// The Release event's asset references (e tags) are populated by SignEventSet
// after the asset event is signed.
func BuildEventSet(params BuildEventSetParams) *EventSet {
	apkInfo := params.APKInfo
	cfg := params.Config
	legacyFormat := params.LegacyFormat

	// Determine app name
	name := cfg.Name
	if name == "" {
		name = apkInfo.Label
	}
	if name == "" {
		name = apkInfo.PackageID
	}

	// Build APK URLs - use original URL only (blossom URL can be calculated from x tag)
	var apkURLs []string
	if params.OriginalURL != "" {
		apkURLs = append(apkURLs, params.OriginalURL)
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

	// Build NIP-34 repository pointer if available (new format only)
	var nip34Repo, nip34Relay string
	if !legacyFormat && cfg.NIP34Repo != nil {
		// Format: "30617:pubkey:identifier"
		nip34Repo = "30617:" + cfg.NIP34Repo.Pubkey + ":" + cfg.NIP34Repo.Identifier
		if len(cfg.NIP34Repo.Relays) > 0 {
			nip34Relay = cfg.NIP34Repo.Relays[0]
		}
	}

	// Software Application event
	appMeta := &AppMetadata{
		PackageID:      apkInfo.PackageID,
		Name:           name,
		Description:    cfg.Description,
		Summary:        cfg.Summary,
		Website:        cfg.Website,
		License:        cfg.License,
		Repository:     cfg.Repository,
		NIP34Repo:      nip34Repo,
		NIP34Relay:     nip34Relay,
		Tags:           cfg.Tags,
		IconURL:        params.IconURL,
		ImageURLs:      params.ImageURLs,
		Platforms:      platforms,
		LegacyFormat:   legacyFormat,
		ReleaseVersion: apkInfo.VersionName, // For legacy a-tag pointing to release
	}

	// Determine release channel (default: main)
	channel := cfg.ReleaseChannel
	if channel == "" {
		channel = "main"
	}

	// Software Release event
	// AssetEventIDs will be populated by SignEventSet after asset is signed
	releaseMeta := &ReleaseMetadata{
		PackageID:     apkInfo.PackageID,
		Version:       apkInfo.VersionName,
		VersionCode:   apkInfo.VersionCode,
		Changelog:     params.Changelog,
		Channel:       channel,
		AssetEventIDs: []string{}, // Populated after signing
		LegacyFormat:  legacyFormat,
		ReleaseURL:    params.ReleaseURL, // For legacy url/r tags
		Commit:        params.Commit,     // In legacy, commit goes on release not asset
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
		Variant:               params.Variant,
		Commit:                params.Commit, // In new format, commit is on asset
		Permissions:           apkInfo.Permissions,
		SupportedNIPs:         cfg.SupportedNIPs,
		MinAllowedVersion:     cfg.MinAllowedVersion,
		MinAllowedVersionCode: cfg.MinAllowedVersionCode,
		LegacyFormat:          legacyFormat,
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
