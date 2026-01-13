package nostr

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/config"
)

// filterExactTag returns tags where tag[0] exactly matches the key.
// This is needed because Tags.GetAll does prefix matching.
func filterExactTag(tags nostr.Tags, key string) nostr.Tags {
	var result nostr.Tags
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == key {
			result = append(result, tag)
		}
	}
	return result
}

func TestBuildAppMetadataEvent(t *testing.T) {
	meta := &AppMetadata{
		PackageID:   "com.example.app",
		Name:        "Example App",
		Description: "A test application",
		Summary:     "Test app",
		Website:     "https://example.com",
		License:     "MIT",
		Repository:  "https://github.com/example/app",
		Tags:        []string{"test", "example"},
		IconURL:     "https://cdn.example.com/icon.png",
		ImageURLs:   []string{"https://cdn.example.com/screenshot1.png"},
		Platforms:   []string{"android-arm64-v8a", "android-armeabi-v7a"},
	}

	pubkey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	event := BuildAppMetadataEvent(meta, pubkey)

	if event.Kind != KindAppMetadata {
		t.Errorf("expected kind %d, got %d", KindAppMetadata, event.Kind)
	}

	if event.PubKey != pubkey {
		t.Errorf("expected pubkey %s, got %s", pubkey, event.PubKey)
	}

	// Check required tags
	dTag := event.Tags.GetFirst([]string{"d"})
	if dTag == nil || (*dTag)[1] != "com.example.app" {
		t.Error("missing or incorrect d tag")
	}

	nameTag := event.Tags.GetFirst([]string{"name"})
	if nameTag == nil || (*nameTag)[1] != "Example App" {
		t.Error("missing or incorrect name tag")
	}

	// Check platform tags (f tags per NIP-82)
	fTags := filterExactTag(event.Tags, "f")
	if len(fTags) != 2 {
		t.Errorf("expected 2 f tags, got %d", len(fTags))
	}

	// Check content contains description per NIP-82
	if event.Content != "A test application" {
		t.Errorf("expected description in content, got %q", event.Content)
	}

	// Check url tag (website)
	urlTag := event.Tags.GetFirst([]string{"url"})
	if urlTag == nil || (*urlTag)[1] != "https://example.com" {
		t.Error("missing or incorrect url tag")
	}
}

func TestBuildReleaseEvent(t *testing.T) {
	meta := &ReleaseMetadata{
		PackageID:      "com.example.app",
		Version:        "1.2.3",
		VersionCode:    123,
		Changelog:      "Bug fixes and improvements",
		Channel:        "beta",
		AssetEventIDs:  []string{"abc123eventid", "def456eventid"},
		AssetRelayHint: "wss://relay.example.com",
	}

	pubkey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	event := BuildReleaseEvent(meta, pubkey)

	if event.Kind != KindRelease {
		t.Errorf("expected kind %d, got %d", KindRelease, event.Kind)
	}

	// Check i tag (app identifier) per NIP-82
	iTag := event.Tags.GetFirst([]string{"i"})
	if iTag == nil || (*iTag)[1] != "com.example.app" {
		t.Errorf("missing or incorrect i tag: %v", iTag)
	}

	// Check d tag format
	dTag := event.Tags.GetFirst([]string{"d"})
	if dTag == nil || (*dTag)[1] != "com.example.app@1.2.3" {
		t.Errorf("incorrect d tag: %v", dTag)
	}

	// Check version tag
	versionTag := event.Tags.GetFirst([]string{"version"})
	if versionTag == nil || (*versionTag)[1] != "1.2.3" {
		t.Error("missing or incorrect version tag")
	}

	// Check channel tag (c tag) per NIP-82
	cTag := event.Tags.GetFirst([]string{"c"})
	if cTag == nil || (*cTag)[1] != "beta" {
		t.Errorf("missing or incorrect c tag: %v", cTag)
	}

	// Check content is changelog (release notes)
	if event.Content != "Bug fixes and improvements" {
		t.Errorf("expected changelog in content, got %q", event.Content)
	}

	// Check asset event IDs (e tags per NIP-82)
	eTags := event.Tags.GetAll([]string{"e"})
	if len(eTags) != 2 {
		t.Errorf("expected 2 e tags, got %d", len(eTags))
	}
	// Check relay hint is included
	if len(eTags) > 0 && len(eTags[0]) < 3 {
		t.Error("expected relay hint in e tag")
	}
}

func TestBuildReleaseEventDefaultChannel(t *testing.T) {
	meta := &ReleaseMetadata{
		PackageID:     "com.example.app",
		Version:       "1.0.0",
		AssetEventIDs: []string{},
	}

	pubkey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	event := BuildReleaseEvent(meta, pubkey)

	// Check channel defaults to "main"
	cTag := event.Tags.GetFirst([]string{"c"})
	if cTag == nil || (*cTag)[1] != "main" {
		t.Errorf("expected default channel 'main', got %v", cTag)
	}
}

func TestBuildSoftwareAssetEvent(t *testing.T) {
	meta := &AssetMetadata{
		Identifier:      "com.example.app",
		Version:         "1.2.3",
		VersionCode:     123,
		SHA256:          "abc123def456",
		Size:            1024000,
		URLs:            []string{"https://cdn.example.com/abc123def456"},
		CertFingerprint: "cert123",
		MinSDK:          21,
		TargetSDK:       34,
		Platforms:       []string{"android-arm64-v8a", "android-armeabi-v7a"},
		Filename:        "example-v1.2.3-arm64.apk",
	}

	pubkey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	event := BuildSoftwareAssetEvent(meta, pubkey)

	if event.Kind != KindSoftwareAsset {
		t.Errorf("expected kind %d, got %d", KindSoftwareAsset, event.Kind)
	}

	// Check i tag (asset identifier) per NIP-82
	iTag := event.Tags.GetFirst([]string{"i"})
	if iTag == nil || (*iTag)[1] != "com.example.app" {
		t.Error("missing or incorrect i tag")
	}

	// Check x tag (SHA256)
	xTag := event.Tags.GetFirst([]string{"x"})
	if xTag == nil || (*xTag)[1] != "abc123def456" {
		t.Error("missing or incorrect x tag")
	}

	// Check size tag
	sizeTag := event.Tags.GetFirst([]string{"size"})
	if sizeTag == nil || (*sizeTag)[1] != "1024000" {
		t.Error("missing or incorrect size tag")
	}

	// Check MIME type
	mTag := event.Tags.GetFirst([]string{"m"})
	if mTag == nil || (*mTag)[1] != "application/vnd.android.package-archive" {
		t.Error("missing or incorrect m tag")
	}

	// Check cert fingerprint (apk_certificate_hash per NIP-82)
	certTag := event.Tags.GetFirst([]string{"apk_certificate_hash"})
	if certTag == nil || (*certTag)[1] != "cert123" {
		t.Error("missing or incorrect apk_certificate_hash tag")
	}

	// Check platform tags (f tags per NIP-82)
	// Note: GetAll does prefix matching, so we filter for exact "f" matches
	fTags := filterExactTag(event.Tags, "f")
	if len(fTags) != 2 {
		t.Errorf("expected 2 f tags, got %d", len(fTags))
	}
	if len(fTags) > 0 && fTags[0][1] != "android-arm64-v8a" {
		t.Errorf("expected f tag android-arm64-v8a, got %s", fTags[0][1])
	}

	// Check min_platform_version tag per NIP-82
	minTag := event.Tags.GetFirst([]string{"min_platform_version"})
	if minTag == nil || (*minTag)[1] != "21" {
		t.Error("missing or incorrect min_platform_version tag")
	}

	// Check target_platform_version tag per NIP-82
	targetTag := event.Tags.GetFirst([]string{"target_platform_version"})
	if targetTag == nil || (*targetTag)[1] != "34" {
		t.Error("missing or incorrect target_platform_version tag")
	}

	// Check version_code tag (Android-specific)
	vcTag := event.Tags.GetFirst([]string{"version_code"})
	if vcTag == nil || (*vcTag)[1] != "123" {
		t.Error("missing or incorrect version_code tag")
	}

	// Check filename tag
	fnTag := event.Tags.GetFirst([]string{"filename"})
	if fnTag == nil || (*fnTag)[1] != "example-v1.2.3-arm64.apk" {
		t.Error("missing or incorrect filename tag")
	}
}

func TestBuildEventSet(t *testing.T) {
	apkInfo := &apk.APKInfo{
		PackageID:       "com.example.app",
		VersionName:     "1.0.0",
		VersionCode:     1,
		Label:           "Example App",
		SHA256:          "abc123",
		FileSize:        1024,
		FilePath:        "/path/to/example-v1.0.0.apk",
		CertFingerprint: "cert123",
		MinSDK:          21,
		TargetSDK:       34,
		Architectures:   []string{"arm64-v8a"},
	}

	cfg := &config.Config{
		Name:        "My App",
		Description: "A great app",
		Repository:  "https://github.com/example/app",
		Tags:        []string{"test"},
	}

	pubkey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	blossomURL := "https://cdn.zapstore.dev"

	events := BuildEventSet(BuildEventSetParams{
		APKInfo:    apkInfo,
		Config:     cfg,
		Pubkey:     pubkey,
		BlossomURL: blossomURL,
	})

	if events.AppMetadata == nil {
		t.Error("AppMetadata event is nil")
	}
	if events.Release == nil {
		t.Error("Release event is nil")
	}
	if events.SoftwareAsset == nil {
		t.Error("SoftwareAsset event is nil")
	}

	// Check that config name overrides APK label
	nameTag := events.AppMetadata.Tags.GetFirst([]string{"name"})
	if nameTag == nil || (*nameTag)[1] != "My App" {
		t.Errorf("expected name 'My App', got %v", nameTag)
	}

	// Check platform identifiers are converted correctly
	fTags := filterExactTag(events.AppMetadata.Tags, "f")
	if len(fTags) != 1 {
		t.Errorf("expected 1 f tag, got %d", len(fTags))
	}
	if len(fTags) > 0 && fTags[0][1] != "android-arm64-v8a" {
		t.Errorf("expected f tag android-arm64-v8a, got %s", fTags[0][1])
	}

	// Check release has channel tag
	cTag := events.Release.Tags.GetFirst([]string{"c"})
	if cTag == nil || (*cTag)[1] != "main" {
		t.Errorf("expected channel 'main', got %v", cTag)
	}

	// Check asset has i tag
	iTag := events.SoftwareAsset.Tags.GetFirst([]string{"i"})
	if iTag == nil || (*iTag)[1] != "com.example.app" {
		t.Errorf("expected i tag 'com.example.app', got %v", iTag)
	}
}

func TestBuildEventSetFallbackToLabel(t *testing.T) {
	apkInfo := &apk.APKInfo{
		PackageID:   "com.example.app",
		VersionName: "1.0.0",
		VersionCode: 1,
		Label:       "APK Label",
		SHA256:      "abc123",
		FilePath:    "/path/to/app.apk",
	}

	cfg := &config.Config{} // No name set

	pubkey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	events := BuildEventSet(BuildEventSetParams{
		APKInfo: apkInfo,
		Config:  cfg,
		Pubkey:  pubkey,
	})

	// Should fall back to APK label
	nameTag := events.AppMetadata.Tags.GetFirst([]string{"name"})
	if nameTag == nil || (*nameTag)[1] != "APK Label" {
		t.Errorf("expected name 'APK Label', got %v", nameTag)
	}
}

func TestBuildEventSetArchitectureIndependent(t *testing.T) {
	// APK with no native libraries should support all Android platforms
	apkInfo := &apk.APKInfo{
		PackageID:     "com.example.app",
		VersionName:   "1.0.0",
		VersionCode:   1,
		Label:         "Pure Java App",
		SHA256:        "abc123",
		FilePath:      "/path/to/app.apk",
		Architectures: []string{}, // No native libs
	}

	cfg := &config.Config{}
	pubkey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	events := BuildEventSet(BuildEventSetParams{
		APKInfo: apkInfo,
		Config:  cfg,
		Pubkey:  pubkey,
	})

	// Should have all 4 Android platform tags
	fTags := filterExactTag(events.AppMetadata.Tags, "f")
	if len(fTags) != 4 {
		t.Errorf("expected 4 f tags for arch-independent APK, got %d", len(fTags))
	}
}

