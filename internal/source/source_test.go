package source

import (
	"strings"
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

// TestNewSourceFromConfig tests source creation from various config types.
// These tests verify URL parsing and source factory logic, not network calls.
func TestNewSourceFromConfig(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		wantType   config.SourceType
		wantErr    bool
		errContain string
	}{
		{
			name: "local source",
			cfg: &config.Config{
				Local: "../../testdata/apks/sample.apk",
			},
			wantType: config.SourceLocal,
			wantErr:  false,
		},
		{
			name: "github source - mempal",
			cfg: &config.Config{
				Repository: "https://github.com/AeonBTC/mempal",
			},
			wantType: config.SourceGitHub,
			wantErr:  false,
		},
		{
			name: "github source - citrine",
			cfg: &config.Config{
				Repository: "https://github.com/greenart7c3/Citrine",
			},
			wantType: config.SourceGitHub,
			wantErr:  false,
		},
		{
			name: "gitlab source - aurora store",
			cfg: &config.Config{
				Repository: "https://gitlab.com/AuroraOSS/AuroraStore",
			},
			wantType: config.SourceGitLab,
			wantErr:  false,
		},
		{
			name: "gitea source (codeberg)",
			cfg: &config.Config{
				Repository: "https://codeberg.org/Freeyourgadget/Gadgetbridge",
			},
			wantType: config.SourceGitea,
			wantErr:  false,
		},
		{
			name: "fdroid source - antennapod",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{
					URL: "https://f-droid.org/packages/de.danoeh.antennapod",
				},
			},
			wantType: config.SourceFDroid,
			wantErr:  false,
		},
		{
			name: "izzyondroid fdroid source",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{
					URL: "https://apt.izzysoft.de/fdroid/index/apk/de.danoeh.antennapod",
				},
			},
			wantType: config.SourceFDroid,
			wantErr:  false,
		},
		{
			name: "web source with asset_url pattern",
			cfg: &config.Config{
				Repository: "https://github.com/AntennaPod/AntennaPod",
				ReleaseSource: &config.ReleaseSource{
					URL:         "https://f-droid.org/packages/de.danoeh.antennapod/",
					IsWebSource: true,
					AssetURL:    "https://f-droid\\.org/repo/de\\.danoeh\\.antennapod_[0-9]+\\.apk",
				},
			},
			wantType: config.SourceWeb,
			wantErr:  false,
		},
		{
			name: "unknown source type fails",
			cfg: &config.Config{
				Repository: "https://unknown-forge.example.com/user/repo",
			},
			wantType:   config.SourceUnknown,
			wantErr:    true,
			errContain: "unsupported source type",
		},
		{
			name: "self-hosted gitlab with explicit type",
			cfg: &config.Config{
				Repository: "https://git.mycompany.com/team/app",
				ReleaseSource: &config.ReleaseSource{
					URL:  "https://git.mycompany.com/team/app",
					Type: "gitlab",
				},
			},
			wantType: config.SourceGitLab,
			wantErr:  false,
		},
		{
			name: "self-hosted gitea with explicit type",
			cfg: &config.Config{
				Repository: "https://forge.example.org/user/app",
				ReleaseSource: &config.ReleaseSource{
					URL:  "https://forge.example.org/user/app",
					Type: "gitea",
				},
			},
			wantType: config.SourceGitea,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := New(tt.cfg)

			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				if tt.errContain != "" && err != nil {
					if !strings.Contains(err.Error(), tt.errContain) {
						t.Errorf("New() error = %v, want error containing %q", err, tt.errContain)
					}
				}
				return
			}

			if src == nil {
				t.Error("New() returned nil source")
				return
			}

			if src.Type() != tt.wantType {
				t.Errorf("New() source type = %v, want %v", src.Type(), tt.wantType)
			}
		})
	}
}

// TestNewWithOptions tests source creation with options
func TestNewWithOptions(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		opts     Options
		wantType config.SourceType
		wantErr  bool
	}{
		{
			name: "local with base dir",
			cfg: &config.Config{
				Local: "../apks/sample.apk",
			},
			opts: Options{
				BaseDir: "../../testdata",
			},
			wantType: config.SourceLocal,
			wantErr:  false,
		},
		{
			name: "github with skip cache",
			cfg: &config.Config{
				Repository: "https://github.com/AeonBTC/mempal",
			},
			opts: Options{
				SkipCache: true,
			},
			wantType: config.SourceGitHub,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := NewWithOptions(tt.cfg, tt.opts)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewWithOptions() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			if src == nil {
				t.Error("NewWithOptions() returned nil source")
				return
			}

			if src.Type() != tt.wantType {
				t.Errorf("NewWithOptions() source type = %v, want %v", src.Type(), tt.wantType)
			}

			// For GitHub source, check that SkipCache was applied
			if tt.opts.SkipCache {
				if gh, ok := src.(*GitHub); ok {
					if !gh.SkipCache {
						t.Error("NewWithOptions() GitHub SkipCache not set")
					}
				}
			}
		})
	}
}

// TestProgressReader tests the progress reader wrapper
func TestProgressReader(t *testing.T) {
	data := []byte("hello world")
	total := int64(len(data))

	var lastDownloaded, lastTotal int64
	callCount := 0

	progress := func(downloaded, totalSize int64) {
		lastDownloaded = downloaded
		lastTotal = totalSize
		callCount++
	}

	reader := &ProgressReader{
		Reader:     &bytesReaderImpl{data: data},
		Total:      total,
		OnProgress: progress,
	}

	buf := make([]byte, 5)
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if n != 5 {
		t.Errorf("Read() n = %d, want 5", n)
	}

	if reader.Downloaded != 5 {
		t.Errorf("Downloaded = %d, want 5", reader.Downloaded)
	}

	if lastDownloaded != 5 {
		t.Errorf("progress callback downloaded = %d, want 5", lastDownloaded)
	}

	if lastTotal != total {
		t.Errorf("progress callback total = %d, want %d", lastTotal, total)
	}

	if callCount != 1 {
		t.Errorf("progress callback count = %d, want 1", callCount)
	}
}

// TestAssetFields tests Asset struct fields
func TestAssetFields(t *testing.T) {
	asset := &Asset{
		Name:        "app-v1.0.0-arm64.apk",
		URL:         "https://example.com/releases/app-v1.0.0-arm64.apk",
		Size:        1024000,
		LocalPath:   "/tmp/app-v1.0.0-arm64.apk",
		ContentType: "application/vnd.android.package-archive",
	}

	if asset.Name != "app-v1.0.0-arm64.apk" {
		t.Errorf("Asset.Name = %q, want %q", asset.Name, "app-v1.0.0-arm64.apk")
	}
	if asset.Size != 1024000 {
		t.Errorf("Asset.Size = %d, want %d", asset.Size, 1024000)
	}
}

// TestReleaseFields tests Release struct fields
func TestReleaseFields(t *testing.T) {
	release := &Release{
		Version:    "1.0.0",
		TagName:    "v1.0.0",
		Changelog:  "Bug fixes",
		PreRelease: false,
		Assets: []*Asset{
			{Name: "app.apk"},
		},
	}

	if release.Version != "1.0.0" {
		t.Errorf("Release.Version = %q, want %q", release.Version, "1.0.0")
	}
	if len(release.Assets) != 1 {
		t.Errorf("Release.Assets len = %d, want 1", len(release.Assets))
	}
}

// bytesReaderImpl implements io.Reader for testing
type bytesReaderImpl struct {
	data []byte
	pos  int
}

func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, nil
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func TestHasUnsupportedArchitecture(t *testing.T) {
	tests := []struct {
		filename    string
		unsupported bool
	}{
		// Unsupported architectures - should be filtered
		{"app-x86_64.apk", true},
		{"app-x86.apk", true},
		{"bunny-6.0-804-x86_64.apk", true},
		{"bunny-6.0-803-x86.apk", true},
		{"app_x86_64_release.apk", true},
		{"app.x86.release.apk", true},
		{"app-i686.apk", true},
		{"app-i386.apk", true},
		{"app-amd64.apk", true},

		// Unsupported 32-bit ARM - should be filtered
		{"app-armeabi-v7a.apk", true},
		{"app-armeabi.apk", true},
		{"app-armeabi-v7a-release.apk", true},

		// Supported architectures - should NOT be filtered (only arm64-v8a)
		{"app-arm64-v8a.apk", false},
		{"bunny-6.0-802-arm64-v8a.apk", false},
		{"bunny-6.0-801-arm.apk", false},    // "arm" alone is ambiguous, don't filter
		{"app-release.apk", false},          // no arch indicator
		{"app.apk", false},                  // no arch indicator
		{"app-universal.apk", false},        // universal
		{"app-v1.0.0.apk", false},           // version, not arch
		{"x86_64-app.apk", false},           // arch at start, not in middle
		{"app-arm64-v8a-fdroid.apk", false}, // arm64 with fdroid suffix

		// Non-APK files - should NOT be filtered
		{"app-x86_64.zip", false},
		{"app-x86.tar.gz", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := HasUnsupportedArchitecture(tt.filename)
			if got != tt.unsupported {
				t.Errorf("HasUnsupportedArchitecture(%q) = %v, want %v", tt.filename, got, tt.unsupported)
			}
		})
	}
}

func TestFilterUnsupportedArchitectures(t *testing.T) {
	assets := []*Asset{
		{Name: "app-arm64-v8a.apk"},
		{Name: "app-x86_64.apk"},
		{Name: "app-armeabi-v7a.apk"},
		{Name: "app-x86.apk"},
		{Name: "app-universal.apk"},
	}

	filtered := FilterUnsupportedArchitectures(assets)

	// Should keep only arm64-v8a and universal (armeabi-v7a is now filtered)
	if len(filtered) != 2 {
		t.Errorf("FilterUnsupportedArchitectures returned %d assets, want 2", len(filtered))
	}

	// Verify the right ones were kept
	names := make(map[string]bool)
	for _, a := range filtered {
		names[a.Name] = true
	}

	if !names["app-arm64-v8a.apk"] {
		t.Error("Expected app-arm64-v8a.apk to be kept")
	}
	if !names["app-universal.apk"] {
		t.Error("Expected app-universal.apk to be kept")
	}
	if names["app-armeabi-v7a.apk"] {
		t.Error("Expected app-armeabi-v7a.apk to be filtered out")
	}
	if names["app-x86_64.apk"] {
		t.Error("Expected app-x86_64.apk to be filtered out")
	}
	if names["app-x86.apk"] {
		t.Error("Expected app-x86.apk to be filtered out")
	}
}
