// Package nostr handles Nostr event generation and signing.
package nostr

import (
	"path/filepath"
	"strconv"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/artifact"
	"github.com/zapstore/zsp/internal/config"
)

// Event kinds for Zapstore
const (
	KindAppMetadata   = 32267 // Software Application (name, description, icon, platforms)
	KindRelease       = 30063 // Software Release (version, changelog, asset links)
	KindSoftwareAsset = 3063  // Software Asset (hash, size, URLs, cert hash, platforms)
	KindBlossomAuth   = 24242 // Blossom upload authorization
	KindIdentityProof = 30509 // NIP-C1 Cryptographic Identity Proof (SPKI)

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
	MIMEType              string   // NIP-82 MIME type (e.g., "application/x-executable")
	CertFingerprint       string   // APK signing certificate SHA256 (APK only)
	MinSDK                int32    // Minimum platform version (APK only)
	TargetSDK             int32    // Target platform version (APK only)
	Platforms             []string // Full platform identifiers (e.g., "linux-x86_64")
	Filename              string   // Original filename (for variant detection)
	Variant               string   // Explicit variant name (e.g., "fdroid", "google")
	Commit                string   // Git commit hash for reproducible builds
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
		if meta.Summary != "" {
			tags = append(tags, nostr.Tag{"summary", meta.Summary})
		}
		if meta.Repository != "" {
			tags = append(tags, nostr.Tag{"repository", meta.Repository})
		}
		if meta.Website != "" {
			tags = append(tags, nostr.Tag{"url", meta.Website})
		}
		// Platform identifiers (f tags)
		for _, platform := range meta.Platforms {
			tags = append(tags, nostr.Tag{"f", platform})
		}
		// Category tags
		for _, tag := range meta.Tags {
			tags = append(tags, nostr.Tag{"t", tag})
		}
		if meta.License != "" {
			tags = append(tags, nostr.Tag{"license", meta.License})
		}
		if meta.IconURL != "" {
			tags = append(tags, nostr.Tag{"icon", meta.IconURL})
		}
		// Screenshots (image tags)
		for _, url := range meta.ImageURLs {
			tags = append(tags, nostr.Tag{"image", url})
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
		if meta.VersionCode > 0 {
			tags = append(tags, nostr.Tag{"version_code", strconv.FormatInt(meta.VersionCode, 10)})
		}

		// Platform version info - legacy uses min_sdk_version/target_sdk_version
		if meta.MinSDK > 0 {
			tags = append(tags, nostr.Tag{"min_sdk_version", strconv.Itoa(int(meta.MinSDK))})
		}
		if meta.TargetSDK > 0 {
			tags = append(tags, nostr.Tag{"target_sdk_version", strconv.Itoa(int(meta.TargetSDK))})
		}

		// MIME type — use provided MIMEType or default to APK
		legacyMIME := meta.MIMEType
		if legacyMIME == "" {
			legacyMIME = "application/vnd.android.package-archive"
		}
		tags = append(tags, nostr.Tag{"m", legacyMIME})

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

		// MIME type — use provided MIMEType or default to APK for backward compatibility
		mimeType := meta.MIMEType
		if mimeType == "" {
			mimeType = "application/vnd.android.package-archive"
		}
		tags = append(tags, nostr.Tag{"m", mimeType})

		// File size
		if meta.Size > 0 {
			tags = append(tags, nostr.Tag{"size", strconv.FormatInt(meta.Size, 10)})
		}

		// Platform identifiers (f tags) - REQUIRED per NIP-82
		for _, platform := range meta.Platforms {
			tags = append(tags, nostr.Tag{"f", platform})
		}

		// Platform version info (APK-specific: min_sdk → min_platform_version)
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

		// Supported NIPs
		for _, nip := range meta.SupportedNIPs {
			tags = append(tags, nostr.Tag{"supported_nip", nip})
		}

		// Android-specific tags (only for APKs)
		if meta.VersionCode > 0 {
			tags = append(tags, nostr.Tag{"version_code", strconv.FormatInt(meta.VersionCode, 10)})
		}

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

// AssetBuildParams holds per-asset parameters for building an event set with multiple assets.
type AssetBuildParams struct {
	Asset       *artifact.AssetInfo
	OriginalURL string // Original download URL (from release source)
	Variant     string // Explicit variant name (from config variants map)
}

// BuildEventSetParams contains parameters for building an event set.
type BuildEventSetParams struct {
	// Asset is the parsed asset metadata (preferred over APKInfo).
	// When set, APKInfo is ignored.
	// For single-asset publishing (backward compatible).
	Asset *artifact.AssetInfo

	// Assets holds multiple asset parameters for multi-asset publishing.
	// When set, Asset/OriginalURL/Variant are ignored.
	Assets []AssetBuildParams

	// APKInfo is the legacy APK metadata. Deprecated: use Asset instead.
	// Kept for backward compatibility during migration.
	APKInfo *apk.APKInfo

	Config           *config.Config
	Pubkey           string
	OriginalURL      string // Original download URL (from release source)
	BlossomServer    string // Blossom server URL (fallback when OriginalURL is empty)
	IconURL          string
	ImageURLs        []string
	Changelog        string    // Release notes (from remote source or local file)
	Variant          string    // Explicit variant name (from config variants map)
	Commit           string    // Git commit hash for reproducible builds
	Channel          string    // Release channel: main (default), beta, nightly, dev
	ReleaseURL       string    // Release page URL (for legacy format url/r tags)
	LegacyFormat     bool      // Use legacy event format (kind 1063, different tags)
	ReleaseTimestamp time.Time // Release publish date (zero means use current time)
	// UseReleaseTimestampForApp sets kind 32267 created_at to ReleaseTimestamp.
	// When false, app metadata keeps current-time created_at.
	UseReleaseTimestampForApp bool
	// MinReleaseTimestamp ensures Release.CreatedAt is strictly greater than this value.
	// Used with --overwrite-release to guarantee NIP-33 replacement when the relay
	// has an existing event with the same or newer timestamp.
	MinReleaseTimestamp time.Time
}

// BuildEventSet creates all events for a software release.
// Supports both APK and native executable assets via the Asset field.
// Supports multi-asset publishing via the Assets field.
// The Release event's asset references (e tags) are populated by SignEventSet
// after the asset event is signed.
func BuildEventSet(params BuildEventSetParams) *EventSet {
	// Normalize to multi-asset path: if Assets is empty, populate from single-asset fields
	assets := params.Assets
	if len(assets) == 0 {
		var ai *artifact.AssetInfo
		if params.Asset != nil {
			ai = params.Asset
		} else if params.APKInfo != nil {
			ai = artifact.FromAPKInfo(params.APKInfo)
		}
		if ai == nil {
			return nil // No asset info provided
		}
		assets = []AssetBuildParams{{
			Asset:       ai,
			OriginalURL: params.OriginalURL,
			Variant:     params.Variant,
		}}
	}

	// Use the first asset as the "primary" for app-level metadata
	primary := assets[0].Asset

	cfg := params.Config
	legacyFormat := params.LegacyFormat

	// Determine app name: config > identifier > filename
	// For non-APK assets, prefer identifier over filename since filename
	// often includes platform suffix (e.g., "myapp-linux-amd64").
	name := cfg.Name
	if name == "" {
		if !primary.IsAPK() && primary.Identifier != "" {
			name = primary.Identifier
		} else {
			name = primary.Name
		}
	}
	if name == "" {
		name = primary.Identifier
	}

	// Collect all platforms across all assets for app metadata
	allPlatforms := collectAllPlatforms(assets)

	// Build NIP-34 repository pointer if available (new format only)
	var nip34Repo, nip34Relay string
	if !legacyFormat && cfg.NIP34Repo != nil {
		// Format: "30617:pubkey:identifier"
		nip34Repo = "30617:" + cfg.NIP34Repo.Pubkey + ":" + cfg.NIP34Repo.Identifier
		if len(cfg.NIP34Repo.Relays) > 0 {
			nip34Relay = cfg.NIP34Repo.Relays[0]
		}
	}

	// Determine version code from primary asset (APK-only)
	var versionCode int64
	if primary.IsAPK() {
		versionCode = primary.APK.VersionCode
	}

	// Software Application event
	appMeta := &AppMetadata{
		PackageID:      primary.Identifier,
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
		Platforms:      allPlatforms,
		LegacyFormat:   legacyFormat,
		ReleaseVersion: primary.Version, // For legacy a-tag pointing to release
	}

	// Determine release channel (default: main)
	channel := params.Channel
	if channel == "" {
		channel = "main"
	}

	// Software Release event
	// AssetEventIDs will be populated by SignEventSet after assets are signed
	releaseMeta := &ReleaseMetadata{
		PackageID:     primary.Identifier,
		Version:       primary.Version,
		VersionCode:   versionCode,
		Changelog:     params.Changelog,
		Channel:       channel,
		AssetEventIDs: []string{}, // Populated after signing
		LegacyFormat:  legacyFormat,
		ReleaseURL:    params.ReleaseURL, // For legacy url/r tags
		Commit:        params.Commit,     // In legacy, commit goes on release not asset
	}

	// Build Software Asset events for each asset
	var softwareAssets []*nostr.Event
	for _, ap := range assets {
		ai := ap.Asset

		// Build asset URLs
		var assetURLs []string
		if ap.OriginalURL != "" {
			assetURLs = append(assetURLs, ap.OriginalURL)
		}
		if params.BlossomServer != "" && ai.SHA256 != "" {
			blossomURL := params.BlossomServer + "/" + ai.SHA256
			assetURLs = append(assetURLs, blossomURL)
		}

		// Determine per-asset version code
		var assetVersionCode int64
		if ai.IsAPK() {
			assetVersionCode = ai.APK.VersionCode
		}

		assetMeta := &AssetMetadata{
			Identifier:            ai.Identifier,
			Version:               ai.Version,
			VersionCode:           assetVersionCode,
			SHA256:                ai.SHA256,
			Size:                  ai.FileSize,
			URLs:                  assetURLs,
			MIMEType:              ai.MIMEType,
			Platforms:             ai.Platforms,
			Filename:              filepath.Base(ai.FilePath),
			Variant:               ap.Variant,
			Commit:                params.Commit, // In new format, commit is on asset
			SupportedNIPs:         cfg.SupportedNIPs,
			MinAllowedVersion:     cfg.MinAllowedVersion,
			MinAllowedVersionCode: cfg.MinAllowedVersionCode,
			LegacyFormat:          legacyFormat,
		}

		// APK-specific fields
		if ai.IsAPK() {
			assetMeta.CertFingerprint = ai.APK.CertFingerprint
			assetMeta.MinSDK = ai.APK.MinSDK
			assetMeta.TargetSDK = ai.APK.TargetSDK
		}

		softwareAssets = append(softwareAssets, BuildSoftwareAssetEvent(assetMeta, params.Pubkey))
	}

	eventSet := &EventSet{
		AppMetadata:    BuildAppMetadataEvent(appMeta, params.Pubkey),
		Release:        BuildReleaseEvent(releaseMeta, params.Pubkey),
		SoftwareAssets: softwareAssets,
	}

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

// collectAllPlatforms returns deduplicated platforms across all assets.
func collectAllPlatforms(assets []AssetBuildParams) []string {
	seen := make(map[string]bool)
	var platforms []string
	for _, ap := range assets {
		for _, p := range ap.Asset.Platforms {
			if !seen[p] {
				seen[p] = true
				platforms = append(platforms, p)
			}
		}
	}
	return platforms
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
