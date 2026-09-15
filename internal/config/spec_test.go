package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// specSupportedFields is the exact set of zapstore.yaml fields defined by
// product/specs/zsp.md. Any yaml-tagged Config field outside this set (and
// outside specDeprecatedFields) is undocumented and must be added to one of
// the two lists deliberately.
var specSupportedFields = map[string]bool{
	"repository":               true,
	"release_source":           true,
	"release_filter":           true,
	"match":                    true,
	"prerelease_channel":       true,
	"channel":                  true,
	"name":                     true,
	"summary":                  true,
	"description":              true,
	"tags":                     true,
	"license":                  true,
	"website":                  true,
	"icon":                     true,
	"images":                   true,
	"release_notes":            true,
	"supported_nips":           true,
	"min_allowed_version":      true,
	"min_allowed_version_code": true,
	"metadata_sources":         true,
}

// specDeprecatedFields are explicitly called out by zsp.md as unknown or
// deprecated: they must decode without error but have no effect on parsing,
// validation, or behavior within this package.
var specDeprecatedFields = map[string]bool{
	"changelog":   true,
	"pubkey":      true,
	"communities": true,
}

// TestConfigSupportedFieldsExact ensures Config exposes exactly the fields
// documented in zsp.md, plus the explicitly named deprecated fields kept for
// legacy callers. It fails if a new yaml field is added without updating the
// spec/deprecated lists, and it fails if a documented field goes missing.
func TestConfigSupportedFieldsExact(t *testing.T) {
	typ := reflect.TypeOf(Config{})
	seen := map[string]bool{}

	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		seen[name] = true
		if !specSupportedFields[name] && !specDeprecatedFields[name] {
			t.Errorf("Config has undocumented yaml field %q (add to zsp.md or specDeprecatedFields)", name)
		}
	}

	for name := range specSupportedFields {
		if !seen[name] {
			t.Errorf("Config is missing documented field %q", name)
		}
	}
}

// TestDeprecatedFieldsDecodeWithoutError verifies changelog, pubkey, and
// communities are accepted by Parse without error (so old zapstore.yaml
// files continue to load) but never influence the resulting Config beyond
// their own deprecated struct fields.
func TestDeprecatedFieldsDecodeWithoutError(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`
repository: https://github.com/user/app
changelog: CHANGELOG.md
pubkey: npub1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq
communities: [acfeaea6e51420e8068fac446ca9d17d7a9ef6a5d20d93894e50fee3d4902a84]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.ReleaseNotes != "" {
		t.Errorf("changelog leaked into ReleaseNotes: %q", cfg.ReleaseNotes)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with deprecated fields set = %v, want nil", err)
	}
}

// TestChangelogNeverPopulatesReleaseNotes covers the specific "changelog has
// no effect" rule even when release_notes is otherwise empty.
func TestChangelogNeverPopulatesReleaseNotes(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`
repository: https://github.com/user/app
changelog: CHANGELOG.md
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Changelog != "CHANGELOG.md" {
		t.Fatalf("Changelog = %q, want CHANGELOG.md (field itself should still decode)", cfg.Changelog)
	}
	if cfg.ReleaseNotes != "" {
		t.Errorf("ReleaseNotes = %q, want empty: changelog must be inert", cfg.ReleaseNotes)
	}
}

// TestLoadDoesNotDependOnSigner ensures loading never resolves SIGN_WITH or
// fails because a zapstore.yaml pubkey does not match the current signer.
// This is a hard requirement: signer-dependent behavior must not exist in
// config loading.
func TestLoadDoesNotDependOnSigner(t *testing.T) {
	const mismatchedPubkey = "npub1zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"

	dir := t.TempDir()
	path := filepath.Join(dir, "zapstore.yaml")
	contents := "repository: https://github.com/user/app\npubkey: " + mismatchedPubkey + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SIGN_WITH", "this value must not be read")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil: loading must not depend on SIGN_WITH", err)
	}
	if cfg.Pubkey != mismatchedPubkey {
		t.Errorf("Pubkey = %q, want %q (field decodes, just has no effect)", cfg.Pubkey, mismatchedPubkey)
	}
}

// TestLoadLocalPathIsNotResolvedByInternalLoad documents that internal Load
// leaves ReleaseSource.LocalPath untouched (only BaseDir is recorded); actual
// local-path resolution relative to the config file is the responsibility of
// the public zsp.LoadConfig, not this package.
func TestLoadLocalPathIsNotResolvedByInternalLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zapstore.yaml")
	if err := os.WriteFile(path, []byte("release_source: builds/app.apk\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ReleaseSource == nil {
		t.Fatal("ReleaseSource is nil")
	}
	if cfg.ReleaseSource.LocalPath != "builds/app.apk" {
		t.Errorf("LocalPath = %q, want unresolved %q", cfg.ReleaseSource.LocalPath, "builds/app.apk")
	}
	if cfg.BaseDir == "" {
		t.Error("BaseDir should still be recorded for callers that resolve paths themselves")
	}
}

// TestValidateMatchRegex ensures the top-level match field is compiled during
// validation like every other regex field (release_filter and
// structured-extractor match).
func TestValidateMatchRegex(t *testing.T) {
	tests := []struct {
		name    string
		match   string
		wantErr bool
	}{
		{"empty is fine", "", false},
		{"valid regex", `.*-arm64\.apk$`, false},
		{"invalid regex", "(unclosed", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Repository: "https://github.com/user/app", Match: tt.match}
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestVersionExtractorExactOneMode enforces that a structured extractor
// (version or asset) requires exactly one of selector, path, or header.
func TestVersionExtractorExactOneMode(t *testing.T) {
	base := func() *ReleaseSource {
		return &ReleaseSource{
			IsWebSource: true,
			AssetURL:    "https://example.com/app.apk",
		}
	}

	tests := []struct {
		name      string
		extractor *VersionExtractor
		wantErr   bool
	}{
		{
			name:      "zero modes set",
			extractor: &VersionExtractor{URL: "https://example.com/releases"},
			wantErr:   true,
		},
		{
			name: "selector and path both set",
			extractor: &VersionExtractor{
				URL:      "https://example.com/releases",
				Selector: ".version",
				Path:     "$.tag_name",
			},
			wantErr: true,
		},
		{
			name: "selector and header both set",
			extractor: &VersionExtractor{
				URL:      "https://example.com/releases",
				Selector: ".version",
				Header:   "location",
			},
			wantErr: true,
		},
		{
			name: "path and header both set",
			extractor: &VersionExtractor{
				URL:    "https://example.com/releases",
				Path:   "$.tag_name",
				Header: "location",
			},
			wantErr: true,
		},
		{
			name: "all three set",
			extractor: &VersionExtractor{
				URL:      "https://example.com/releases",
				Selector: ".version",
				Path:     "$.tag_name",
				Header:   "location",
			},
			wantErr: true,
		},
		{
			name: "exactly selector",
			extractor: &VersionExtractor{
				URL:      "https://example.com/releases",
				Selector: ".version",
			},
			wantErr: false,
		},
		{
			name: "exactly path",
			extractor: &VersionExtractor{
				URL:  "https://example.com/releases",
				Path: "$.tag_name",
			},
			wantErr: false,
		},
		{
			name: "exactly header",
			extractor: &VersionExtractor{
				URL:    "https://example.com/releases",
				Header: "location",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := base()
			rs.Version = tt.extractor
			err := rs.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestReleaseSourceAssetURLAndAssetMutuallyExclusive covers the structured
// release_source rule that asset_url and asset cannot both be set.
func TestReleaseSourceAssetURLAndAssetMutuallyExclusive(t *testing.T) {
	rs := &ReleaseSource{
		IsWebSource: true,
		AssetURL:    "https://example.com/app.apk",
		Asset: &VersionExtractor{
			URL:       "https://example.com/download",
			Selector:  "a.download",
			Attribute: "href",
		},
	}
	if err := rs.Validate(); err == nil {
		t.Error("Validate() = nil, want error for asset_url + asset both set")
	}
}

// TestParseMinAllowedVersionFields confirms min_allowed_version and
// min_allowed_version_code decode as documented.
func TestParseMinAllowedVersionFields(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`
repository: https://github.com/user/app
min_allowed_version: "1.2.3"
min_allowed_version_code: 42
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.MinAllowedVersion != "1.2.3" {
		t.Errorf("MinAllowedVersion = %q, want 1.2.3", cfg.MinAllowedVersion)
	}
	if cfg.MinAllowedVersionCode != 42 {
		t.Errorf("MinAllowedVersionCode = %d, want 42", cfg.MinAllowedVersionCode)
	}
}

// TestUnknownFieldsIgnoredAndInert extends the base "unknown fields ignored"
// coverage with fields that could plausibly be mistaken for supported ones
// (e.g. legacy zapstore-cli names), ensuring they neither error nor leak into
// any Config field.
func TestUnknownFieldsIgnoredAndInert(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`
repository: https://github.com/user/app
identifier: com.example.app
version: "1.0.0"
assets: [app.apk]
executables: [app]
release_repository: https://github.com/user/other
homepage: https://example.com
remote_metadata: [github]
blossom_server: https://cdn.example.com
fetch_metadata: [github]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Website != "" {
		t.Errorf("Website = %q, want empty: homepage must not map to website", cfg.Website)
	}
	if len(cfg.MetadataSources) != 0 {
		t.Errorf("MetadataSources = %v, want empty: remote_metadata/fetch_metadata must not map in", cfg.MetadataSources)
	}
	if cfg.ReleaseSource != nil {
		t.Errorf("ReleaseSource = %+v, want nil: legacy assets must not become release_source", cfg.ReleaseSource)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}
