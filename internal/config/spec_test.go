package config

import (
	"errors"
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
	"changelog":          true,
	"pubkey":             true,
	"communities":        true,
	"identifier":         true,
	"version":            true,
	"assets":             true,
	"executables":        true,
	"release_repository": true,
	"homepage":           true,
	"remote_metadata":    true,
	"fetch_metadata":     true,
	"blossom_server":     true,
}

// TestConfigSupportedFieldsExact ensures Config exposes exactly the fields
// documented in zsp.md, plus the explicitly named deprecated fields kept for
// legacy callers. It fails if a new yaml field is added without updating the
// spec/deprecated lists, and it fails if a documented field goes missing.
func TestConfigSupportedFieldsExact(t *testing.T) {
	supported := yamlKeys(reflect.TypeOf(Config{}))
	for name := range supported {
		if !specSupportedFields[name] {
			t.Errorf("Config has undocumented yaml field %q (add to zsp.md or specDeprecatedFields)", name)
		}
	}
	for name := range specSupportedFields {
		if !supported[name] {
			t.Errorf("Config is missing documented field %q", name)
		}
	}

	deprecated := yamlKeys(reflect.TypeOf(deprecatedKeys{}))
	for name := range deprecated {
		if !specDeprecatedFields[name] {
			t.Errorf("deprecatedKeys has unlisted yaml field %q", name)
		}
	}
	for name := range specDeprecatedFields {
		if !deprecated[name] {
			t.Errorf("deprecatedKeys is missing field %q", name)
		}
	}
}

// yamlKeys returns the yaml key of every directly tagged field of typ,
// skipping inline groups.
func yamlKeys(typ reflect.Type) map[string]bool {
	keys := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		if name == "" && strings.Contains(options, "inline") {
			continue
		}
		keys[name] = true
	}
	return keys
}

// TestDeprecatedFieldsDecodeWithoutError verifies changelog, pubkey, and
// communities are accepted by Parse without error (so old zapstore.yaml
// files continue to load) but never influence the resulting Config.
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
	if cfg.ReleaseNotes != "" {
		t.Errorf("ReleaseNotes = %q, want empty: changelog must be inert", cfg.ReleaseNotes)
	}
}

// TestParseDoesNotDependOnSigner ensures parsing never resolves SIGN_WITH or
// fails because a zapstore.yaml pubkey does not match the current signer.
func TestParseDoesNotDependOnSigner(t *testing.T) {
	t.Setenv("SIGN_WITH", "this value must not be read")

	_, err := Parse(strings.NewReader(`
repository: https://github.com/user/app
pubkey: npub1zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz
`))
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil: parsing must not depend on SIGN_WITH", err)
	}
}

func TestParseRejectsUnknownKeysAndEmptyInput(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "typo in top-level key", input: "release_sorce: https://github.com/a/b\n", wantErr: "release_sorce"},
		{name: "typo in release_source key", input: "release_source:\n  url: https://github.com/a/b\n  tpye: github\n", wantErr: "tpye"},
		{name: "typo in extractor key", input: "release_source:\n  asset_url: https://example.com/a.apk\n  version:\n    url: https://example.com/v\n    selecter: .v\n", wantErr: "selecter"},
		{name: "empty input", input: "", wantErr: "zapstore.yaml is empty"},
		{name: "comments only", input: "# nothing here\n\n", wantErr: "zapstore.yaml is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse(%q) error = %v, want it to contain %q", tt.input, err, tt.wantErr)
			}
			if tt.input == "" && !errors.Is(err, ErrEmpty) {
				t.Errorf("Parse(empty) error = %v, want ErrEmpty", err)
			}
		})
	}
}

func TestNormalizeReleaseSource(t *testing.T) {
	tests := []struct {
		name    string
		raw     ReleaseSource
		want    ReleaseSource
		wantErr string
	}{
		{name: "forge url", raw: ReleaseSource{URL: "https://github.com/a/b"}, want: ReleaseSource{URL: "https://github.com/a/b"}},
		{name: "direct apk url", raw: ReleaseSource{URL: "https://example.com/app.apk"}, want: ReleaseSource{AssetURL: "https://example.com/app.apk", IsWebSource: true}},
		{name: "type is lowered", raw: ReleaseSource{URL: "https://git.example.com/a/b", Type: " GitLab "}, want: ReleaseSource{URL: "https://git.example.com/a/b", Type: "gitlab"}},
		{name: "typed unknown host is not a direct download", raw: ReleaseSource{URL: "https://example.com/app.apk", Type: "fdroid"}, want: ReleaseSource{URL: "https://example.com/app.apk", Type: "fdroid"}},
		{name: "asset url", raw: ReleaseSource{AssetURL: "https://example.com/app.apk"}, want: ReleaseSource{AssetURL: "https://example.com/app.apk", IsWebSource: true}},
		{name: "asset url keeps url", raw: ReleaseSource{URL: "https://example.com/page", AssetURL: "https://example.com/app.apk"}, want: ReleaseSource{URL: "https://example.com/page", AssetURL: "https://example.com/app.apk", IsWebSource: true}},
		{name: "IsWebSource is honored", raw: ReleaseSource{URL: "https://github.com/a/b", IsWebSource: true}, want: ReleaseSource{URL: "https://github.com/a/b", IsWebSource: true}},
		{name: "local path", raw: ReleaseSource{LocalPath: "/x/app.apk"}, want: ReleaseSource{LocalPath: "/x/app.apk"}},
		{name: "type local moves url", raw: ReleaseSource{URL: "/x/*.apk", Type: "local"}, want: ReleaseSource{LocalPath: "/x/*.apk", Type: "local"}},
		{name: "type local two paths", raw: ReleaseSource{URL: "/x", LocalPath: "/y", Type: "local"}, wantErr: "one path"},
		{name: "local with remote type", raw: ReleaseSource{LocalPath: "/x", Type: "github"}, wantErr: "remote type"},
		{name: "local with asset url", raw: ReleaseSource{LocalPath: "/x", AssetURL: "https://example.com/a.apk"}, wantErr: "remote settings"},
		{name: "empty", raw: ReleaseSource{}, wantErr: "must specify"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeReleaseSource(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NormalizeReleaseSource(%+v) error = %v, want it to contain %q", tt.raw, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeReleaseSource(%+v): %v", tt.raw, err)
			}
			if *got != tt.want {
				t.Errorf("NormalizeReleaseSource(%+v) = %+v, want %+v", tt.raw, *got, tt.want)
			}
		})
	}
}

func TestNormalizeReleaseSourceIsIdempotent(t *testing.T) {
	for _, raw := range []ReleaseSource{
		{URL: "https://example.com/app.apk"},
		{URL: "https://example.com/downloads"},
		{URL: "https://github.com/a/b"},
		{URL: "/x/*.apk", Type: "local"},
		{AssetURL: "https://example.com/app.apk"},
	} {
		once, err := NormalizeReleaseSource(raw)
		if err != nil {
			t.Fatalf("NormalizeReleaseSource(%+v): %v", raw, err)
		}
		twice, err := NormalizeReleaseSource(*once)
		if err != nil || *twice != *once {
			t.Errorf("NormalizeReleaseSource(%+v) twice = %+v, %v, want %+v", raw, twice, err, *once)
		}
	}
}

func TestParseKeepsDirectURLAsWritten(t *testing.T) {
	for name, input := range map[string]string{
		"scalar":  "release_source: https://example.com/app.apk\n",
		"mapping": "release_source:\n  url: https://example.com/app.apk\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse(strings.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate(): %v", err)
			}
			if got := *cfg.ReleaseSource; got != (ReleaseSource{URL: "https://example.com/app.apk"}) {
				t.Errorf("ReleaseSource = %+v, want the URL as written", got)
			}
			normalized, err := NormalizeReleaseSource(*cfg.ReleaseSource)
			if err != nil {
				t.Fatal(err)
			}
			if want := (ReleaseSource{AssetURL: "https://example.com/app.apk", IsWebSource: true}); *normalized != want {
				t.Errorf("normalized = %+v, want %+v", *normalized, want)
			}
		})
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

// TestLegacyKeysAreInert covers deprecated keys that could plausibly be
// mistaken for supported ones (e.g. legacy zapstore-cli names), ensuring they
// neither error nor leak into any Config field.
func TestLegacyKeysAreInert(t *testing.T) {
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
