package release

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zapstore/zsp/v2/internal/apk"
	"github.com/zapstore/zsp/v2/internal/source"
)

func TestMergeMetadata(t *testing.T) {
	first := &source.AppMetadata{
		Source: "fastlane", Name: "Fastlane Name", Summary: "Fastlane summary",
		Website: "https://fastlane.example", Tags: []string{"one"},
	}
	second := &source.AppMetadata{
		Source: "github", Name: "GitHub Name", Summary: "GitHub summary", Description: "GitHub description",
		License: "MIT", Website: "https://github.example", Tags: []string{"two"},
	}

	tests := []struct {
		name      string
		explicit  PublishConfig
		label     string
		packageID string
		results   []*source.AppMetadata
		check     func(t *testing.T, got PublishConfig)
	}{
		{
			name:      "explicit YAML wins over everything",
			explicit:  PublishConfig{Name: "Yaml", Summary: "S", Description: "D", License: "ISC", Website: "https://yaml.example", Tags: []string{"y"}},
			label:     "Label",
			packageID: "dev.example",
			results:   []*source.AppMetadata{first, second},
			check: func(t *testing.T, got PublishConfig) {
				want := PublishConfig{Name: "Yaml", Summary: "S", Description: "D", License: "ISC", Website: "https://yaml.example", Tags: []string{"y"}}
				if got.Name != want.Name || got.Summary != want.Summary || got.Description != want.Description ||
					got.License != want.License || got.Website != want.Website || !reflect.DeepEqual(got.Tags, want.Tags) {
					t.Errorf("got %+v, want explicit values %+v", got, want)
				}
			},
		},
		{
			name:      "APK label beats metadata name",
			label:     "Label",
			packageID: "dev.example",
			results:   []*source.AppMetadata{first, second},
			check:     func(t *testing.T, got PublishConfig) { expectName(t, got, "Label") },
		},
		{
			name:      "metadata name when the APK label is empty",
			label:     " ",
			packageID: "dev.example",
			results:   []*source.AppMetadata{{Source: "fastlane"}, second},
			check:     func(t *testing.T, got PublishConfig) { expectName(t, got, "GitHub Name") },
		},
		{
			name:      "package ID when label and metadata are empty",
			packageID: "dev.example",
			results:   []*source.AppMetadata{{Source: "fastlane"}},
			check:     func(t *testing.T, got PublishConfig) { expectName(t, got, "dev.example") },
		},
		{
			name:      "all sources failing still applies the APK label",
			label:     "Label",
			packageID: "dev.example",
			check:     func(t *testing.T, got PublishConfig) { expectName(t, got, "Label") },
		},
		{
			name:      "no sources and no label uses the package ID",
			packageID: "dev.example",
			check:     func(t *testing.T, got PublishConfig) { expectName(t, got, "dev.example") },
		},
		{
			name:    "first non-empty value per field wins in source order",
			results: []*source.AppMetadata{first, second},
			check: func(t *testing.T, got PublishConfig) {
				if got.Summary != "Fastlane summary" || got.Description != "GitHub description" || got.License != "MIT" {
					t.Errorf("summary %q description %q license %q", got.Summary, got.Description, got.License)
				}
				if got.Website != "https://fastlane.example" || !reflect.DeepEqual(got.Tags, []string{"one"}) {
					t.Errorf("website %q tags %v", got.Website, got.Tags)
				}
			},
		},
		{
			name:    "source order decides, not source name",
			results: []*source.AppMetadata{second, first},
			check: func(t *testing.T, got PublishConfig) {
				if got.Summary != "GitHub summary" || got.Website != "https://github.example" {
					t.Errorf("summary %q website %q", got.Summary, got.Website)
				}
			},
		},
		{
			name: "website must pass the URL policy",
			results: []*source.AppMetadata{
				{Source: "a", Website: "http://insecure.example"},
				{Source: "b", Website: "https://user:pass@example.com"},
				{Source: "c", Website: "/etc/passwd"},
				{Source: "d", Website: "https://ok.example"},
			},
			check: func(t *testing.T, got PublishConfig) {
				if got.Website != "https://ok.example" {
					t.Errorf("Website = %q, want the first valid one", got.Website)
				}
			},
		},
		{
			name:    "no valid website leaves it empty",
			results: []*source.AppMetadata{{Source: "a", Website: "javascript:alert(1)"}},
			check: func(t *testing.T, got PublishConfig) {
				if got.Website != "" {
					t.Errorf("Website = %q, want empty", got.Website)
				}
			},
		},
		{
			name: "icon candidates and image sets follow source order",
			results: []*source.AppMetadata{
				{Source: "fastlane", IconURL: "https://a.example/icon.png", ImageURLs: []string{"https://a.example/1.png", "https://a.example/2.png"}},
				{Source: "fdroid", IconURL: "https://b.example/icon.png", ImageURLs: []string{"https://b.example/1.png"}},
			},
			check: func(t *testing.T, got PublishConfig) {
				wantIcons := []string{"https://a.example/icon.png", "https://b.example/icon.png"}
				wantSets := [][]string{{"https://a.example/1.png", "https://a.example/2.png"}, {"https://b.example/1.png"}}
				if !reflect.DeepEqual(got.iconCandidates, wantIcons) || !reflect.DeepEqual(got.imageSets, wantSets) {
					t.Errorf("icons %v sets %v, want %v and %v", got.iconCandidates, got.imageSets, wantIcons, wantSets)
				}
				if got.Icon != "" || len(got.Images) != 0 {
					t.Errorf("explicit media was set from metadata: icon %q images %v", got.Icon, got.Images)
				}
			},
		},
		{
			name:     "explicit icon and images replace discovered media",
			explicit: PublishConfig{Icon: "https://yaml.example/icon.png", Images: []string{"https://yaml.example/1.png"}},
			results: []*source.AppMetadata{
				{Source: "fastlane", IconURL: "https://a.example/icon.png", ImageURLs: []string{"https://a.example/1.png"}},
			},
			check: func(t *testing.T, got PublishConfig) {
				if got.iconCandidates != nil || got.imageSets != nil {
					t.Errorf("icons %v sets %v, want none: explicit media wins", got.iconCandidates, got.imageSets)
				}
				if !reflect.DeepEqual(got.Images, []string{"https://yaml.example/1.png"}) {
					t.Errorf("Images = %v", got.Images)
				}
			},
		},
		{
			name: "remote sources cannot produce local paths or insecure URLs",
			results: []*source.AppMetadata{{
				Source:    "fastlane",
				IconURL:   "/home/x/.ssh/id_ed25519",
				ImageURLs: []string{"/etc/passwd", "file:///etc/passwd", "http://insecure.example/1.png", "relative.png", "https://ok.example/1.png"},
			}},
			check: func(t *testing.T, got PublishConfig) {
				if got.iconCandidates != nil {
					t.Errorf("iconCandidates = %v, want none", got.iconCandidates)
				}
				if !reflect.DeepEqual(got.imageSets, [][]string{{"https://ok.example/1.png"}}) {
					t.Errorf("imageSets = %v, want only the HTTPS URL", got.imageSets)
				}
			},
		},
		{
			name: "local fastlane media is kept and ordered before remote media of the same source",
			results: []*source.AppMetadata{
				{Source: "fastlane", LocalIcon: "/base/icon.png", LocalImages: []string{"/base/1.png"}},
				{Source: "github"},
				{Source: "fdroid", IconURL: "https://b.example/icon.png", ImageURLs: []string{"https://b.example/1.png"}},
			},
			check: func(t *testing.T, got PublishConfig) {
				if !reflect.DeepEqual(got.iconCandidates, []string{"/base/icon.png", "https://b.example/icon.png"}) {
					t.Errorf("iconCandidates = %v", got.iconCandidates)
				}
				if !reflect.DeepEqual(got.imageSets, [][]string{{"/base/1.png"}, {"https://b.example/1.png"}}) {
					t.Errorf("imageSets = %v", got.imageSets)
				}
			},
		},
		{
			name:    "sources without media add no empty sets",
			results: []*source.AppMetadata{{Source: "github", Name: "x"}},
			check: func(t *testing.T, got PublishConfig) {
				if got.iconCandidates != nil || got.imageSets != nil {
					t.Errorf("icons %v sets %v, want none", got.iconCandidates, got.imageSets)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, mergeMetadata(tt.explicit, tt.label, tt.packageID, tt.results))
		})
	}
}

func expectName(t *testing.T, got PublishConfig, want string) {
	t.Helper()
	if got.Name != want {
		t.Errorf("Name = %q, want %q", got.Name, want)
	}
}

func TestMergeMetadataDoesNotAliasInputs(t *testing.T) {
	explicit := PublishConfig{Tags: []string{"a"}, Images: []string{"https://x.example/1.png"}}
	meta := &source.AppMetadata{Tags: []string{"b"}}
	got := mergeMetadata(PublishConfig{}, "", "id", []*source.AppMetadata{meta})
	got.Tags[0] = "changed"
	if meta.Tags[0] != "b" {
		t.Errorf("source tags mutated: %v", meta.Tags)
	}
	got = mergeMetadata(explicit, "", "id", nil)
	got.Tags[0], got.Images[0] = "changed", "changed"
	if explicit.Tags[0] != "a" || explicit.Images[0] != "https://x.example/1.png" {
		t.Errorf("explicit config mutated: %+v", explicit)
	}
}

func TestPreparePublishMetadataAppliesAPKLabelWhenEverySourceFails(t *testing.T) {
	fetch := FetchConfig{Repository: "https://codeberg.org/example/app", baseDir: t.TempDir()}
	prepared, _, warnings, err := preparePublishMetadata(t.Context(),
		PublishConfig{MetadataSources: []string{"github"}, baseDir: fetch.baseDir},
		&APK{fetchConfig: fetch}, &apk.APKInfo{PackageID: "dev.example", Label: "Zaplite"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Name != "Zaplite" {
		t.Errorf("Name = %q, want the APK label", prepared.Name)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "metadata source github failed") {
		t.Errorf("warnings = %v, want the github failure", warnings)
	}
}

func TestPreparePublishMetadataEmptySourceListDisablesMetadata(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir+"/fastlane/metadata/android/en-US/full_description.txt", "from fastlane")
	fetch := FetchConfig{Repository: "https://codeberg.org/example/app", baseDir: dir}
	prepared, _, warnings, err := preparePublishMetadata(t.Context(),
		PublishConfig{MetadataSources: []string{}, baseDir: dir},
		&APK{fetchConfig: fetch}, &apk.APKInfo{PackageID: "dev.example", Label: "Zaplite"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Description != "" || prepared.Name != "Zaplite" || len(warnings) != 0 {
		t.Errorf("prepared = %+v warnings = %v, want no metadata but the APK label", prepared, warnings)
	}
}

func TestPreparePublishMetadataTagsLocalFastlaneMedia(t *testing.T) {
	dir := t.TempDir()
	base := dir + "/fastlane/metadata/android/en-US"
	writeTestFile(t, base+"/full_description.txt", "from fastlane")
	writeTestFile(t, base+"/images/icon.png", "icon")
	writeTestFile(t, base+"/images/phoneScreenshots/1.png", "shot")
	fetch := FetchConfig{ReleaseSource: &ReleaseSource{LocalPath: dir + "/app.apk"}, baseDir: dir}
	prepared, _, _, err := preparePublishMetadata(t.Context(), PublishConfig{baseDir: dir},
		&APK{fetchConfig: fetch}, &apk.APKInfo{PackageID: "dev.example"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Description != "from fastlane" || prepared.baseDir != dir {
		t.Errorf("description %q baseDir %q", prepared.Description, prepared.baseDir)
	}
	if !reflect.DeepEqual(prepared.iconCandidates, []string{base + "/images/icon.png"}) ||
		!reflect.DeepEqual(prepared.imageSets, [][]string{{base + "/images/phoneScreenshots/1.png"}}) {
		t.Errorf("icons %v sets %v", prepared.iconCandidates, prepared.imageSets)
	}
}
