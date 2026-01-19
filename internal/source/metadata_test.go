package source

import (
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

// TestDefaultMetadataSources tests the metadata source detection logic
func TestDefaultMetadataSources(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		wantLen    int
		wantFirst  string
		wantNil    bool
	}{
		{
			name: "github source includes github metadata",
			cfg: &config.Config{
				Repository: "https://github.com/AeonBTC/mempal",
			},
			wantLen:   1,
			wantFirst: "github",
		},
		{
			name: "gitlab source includes gitlab metadata",
			cfg: &config.Config{
				Repository: "https://gitlab.com/AuroraOSS/AuroraStore",
			},
			wantLen:   1,
			wantFirst: "gitlab",
		},
		{
			name: "fdroid source includes fdroid metadata",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{
					URL: "https://f-droid.org/packages/de.danoeh.antennapod",
				},
			},
			wantLen:   1,
			wantFirst: "fdroid",
		},
		{
			name: "local source with github repo gets github metadata",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{LocalPath: "./app.apk"},
				Repository:    "https://github.com/AeonBTC/mempal",
			},
			wantLen:   1,
			wantFirst: "github",
		},
		{
			name: "explicit metadata_sources appended",
			cfg: &config.Config{
				Repository:      "https://github.com/AeonBTC/mempal",
				MetadataSources: []string{"playstore"},
			},
			wantLen: 2, // github + playstore
		},
		{
			name: "explicit metadata_sources without duplicates",
			cfg: &config.Config{
				Repository:      "https://github.com/AeonBTC/mempal",
				MetadataSources: []string{"github", "playstore"},
			},
			wantLen: 2, // github (from repo) + playstore (github deduplicated)
		},
		{
			name: "unknown source type with no repo",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{LocalPath: "./app.apk"},
			},
			wantNil: true,
		},
		{
			name: "web source with github repo gets github metadata",
			cfg: &config.Config{
				Repository: "https://github.com/AntennaPod/AntennaPod",
				ReleaseSource: &config.ReleaseSource{
					URL:         "https://example.com/releases",
					IsWebSource: true,
					AssetURL:    "https://example.com/app.apk",
				},
			},
			wantLen:   1,
			wantFirst: "github",
		},
		{
			name: "multiple explicit sources",
			cfg: &config.Config{
				Repository:      "https://github.com/AntennaPod/AntennaPod",
				ReleaseSource:   &config.ReleaseSource{URL: "https://f-droid.org/packages/de.danoeh.antennapod"},
				MetadataSources: []string{"playstore"},
			},
			wantLen: 3, // fdroid (from release_source) + github (from repository) + playstore
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sources := DefaultMetadataSources(tt.cfg)

			if tt.wantNil {
				if sources != nil {
					t.Errorf("DefaultMetadataSources() = %v, want nil", sources)
				}
				return
			}

			if sources == nil {
				t.Error("DefaultMetadataSources() returned nil, want non-nil")
				return
			}

			if len(sources) != tt.wantLen {
				t.Errorf("DefaultMetadataSources() len = %d, want %d, got %v", len(sources), tt.wantLen, sources)
			}

			if tt.wantFirst != "" && len(sources) > 0 && sources[0] != tt.wantFirst {
				t.Errorf("DefaultMetadataSources()[0] = %q, want %q", sources[0], tt.wantFirst)
			}
		})
	}
}

// TestMetadataFetcherCreation tests MetadataFetcher creation
func TestMetadataFetcherCreation(t *testing.T) {
	cfg := &config.Config{
		Repository: "https://github.com/AeonBTC/mempal",
	}

	fetcher := NewMetadataFetcher(cfg)
	if fetcher == nil {
		t.Error("NewMetadataFetcher() returned nil")
	}

	fetcherWithPkg := NewMetadataFetcherWithPackageID(cfg, "com.aeonbtc.mempal")
	if fetcherWithPkg == nil {
		t.Error("NewMetadataFetcherWithPackageID() returned nil")
	}
	if fetcherWithPkg.PackageID != "com.aeonbtc.mempal" {
		t.Errorf("PackageID = %q, want %q", fetcherWithPkg.PackageID, "com.aeonbtc.mempal")
	}
}

// TestExtractFirstParagraph tests the markdown paragraph extraction
func TestExtractFirstParagraph(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
		want     string
	}{
		{
			name:     "simple paragraph",
			markdown: "This is the first paragraph.",
			want:     "This is the first paragraph.",
		},
		{
			name: "skip header",
			markdown: `# Header
This is the content.`,
			want: "This is the content.",
		},
		{
			name: "skip badges",
			markdown: `![badge](url)
[![another](url)](link)
Real content here.`,
			want: "Real content here.",
		},
		{
			name: "skip html",
			markdown: `<div>ignored</div>
Content after html.`,
			want: "Content after html.",
		},
		{
			name: "multiline paragraph",
			markdown: `# Title
First line of paragraph.
Second line of paragraph.

Next paragraph ignored.`,
			want: "First line of paragraph. Second line of paragraph.",
		},
		{
			name:     "empty string",
			markdown: "",
			want:     "",
		},
		{
			name: "only headers and badges",
			markdown: `# Title
![badge](url)`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFirstParagraph(tt.markdown)
			if got != tt.want {
				t.Errorf("extractFirstParagraph() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGetPlayStorePackageID tests Play Store URL parsing
func TestGetPlayStorePackageID(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{
			"https://play.google.com/store/apps/details?id=com.example.app",
			"com.example.app",
		},
		{
			"https://play.google.com/store/apps/details?id=de.danoeh.antennapod&hl=en",
			"de.danoeh.antennapod",
		},
		{
			"https://play.google.com/store/apps/details?hl=en&id=com.example.app",
			"com.example.app",
		},
		{
			"https://github.com/user/repo",
			"",
		},
		{
			"not a url",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := GetPlayStorePackageID(tt.url)
			if got != tt.want {
				t.Errorf("GetPlayStorePackageID(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

