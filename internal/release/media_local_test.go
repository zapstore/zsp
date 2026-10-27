package release

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zapstore/zsp/v2/internal/apk"
	"github.com/zapstore/zsp/v2/internal/source"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// localMediaTree builds a base directory with one good screenshot and the
// ways a path can escape it.
func localMediaTree(t *testing.T) (base string, pngBytes []byte) {
	t.Helper()
	pngBytes = tinyPNG(t, 4)
	base = t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(base, "media", "ok.png"), string(pngBytes))
	writeTestFile(t, filepath.Join(outside, "secret.png"), string(pngBytes))
	if err := os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(base, "media", "link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("ok.png"), filepath.Join(base, "media", "inside-link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	return base, pngBytes
}

func TestPrepareMediaExplicitLocalPathsAreConfined(t *testing.T) {
	base, pngBytes := localMediaTree(t)
	outside := filepath.Join(filepath.Dir(base), "outside.png")
	writeTestFile(t, outside, string(pngBytes))
	t.Cleanup(func() { _ = os.Remove(outside) })

	tests := []struct {
		name     string
		location string
		wantErr  bool
	}{
		{name: "nested file inside base", location: filepath.Join(base, "media", "ok.png")},
		{name: "symlink file", location: filepath.Join(base, "media", "link.png"), wantErr: true},
		{name: "symlink to a file inside base", location: filepath.Join(base, "media", "inside-link.png"), wantErr: true},
		{name: "symlinked directory component", location: filepath.Join(base, "linked-dir", "secret.png"), wantErr: true},
		{name: "dot dot escape", location: filepath.Join(base, "media", "..", "..", "outside.png"), wantErr: true},
		{name: "absolute outside base", location: outside, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for label, config := range map[string]PublishConfig{
				"icon":   {Icon: tt.location, baseDir: base},
				"images": {Images: []string{tt.location}, baseDir: base},
			} {
				plan, _, err := prepareMedia(t.Context(), config, &apk.APKInfo{Icon: pngBytes}, "https://cdn.example", true)
				if tt.wantErr {
					if !errors.Is(err, ErrInvalidConfig) {
						t.Errorf("%s %q: error = %v, want ErrInvalidConfig", label, tt.location, err)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s %q: %v", label, tt.location, err)
				} else if label == "images" && len(plan.imageURLs) != 1 {
					t.Errorf("images %q: imageURLs = %v, want one", tt.location, plan.imageURLs)
				}
			}
		})
	}
}

func TestPrepareMediaDiscoveredLocalPathsAreSkipped(t *testing.T) {
	base, _ := localMediaTree(t)
	plan, warnings, err := prepareMedia(t.Context(), PublishConfig{
		baseDir:        base,
		iconCandidates: []string{filepath.Join(base, "media", "link.png"), filepath.Join(base, "media", "ok.png")},
		imageSets: [][]string{
			{filepath.Join(base, "media", "link.png"), filepath.Join(base, "linked-dir", "secret.png")},
			{filepath.Join(base, "media", "ok.png")},
		},
	}, &apk.APKInfo{}, "https://cdn.example", true)
	if err != nil {
		t.Fatalf("prepareMedia(): %v", err)
	}
	if plan.iconURL == "" || len(plan.imageURLs) != 1 {
		t.Errorf("icon %q images %v, want the regular files to be used", plan.iconURL, plan.imageURLs)
	}
	if len(warnings) != 3 {
		t.Fatalf("warnings = %v, want three skipped symlinks", warnings)
	}
	for _, warning := range warnings {
		if !strings.Contains(warning, "skipped") || !strings.Contains(warning, "symlink") {
			t.Errorf("warning %q does not explain the skipped symlink", warning)
		}
	}
}

func TestLoadReleaseNotesLocalPathsAreConfined(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(base, "docs", "notes.md"), "notes")
	writeTestFile(t, filepath.Join(outside, "notes.md"), "secret")
	if err := os.Symlink(filepath.Join(outside, "notes.md"), filepath.Join(base, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "linked-dir")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		location string
		want     string
		wantErr  bool
	}{
		{name: "nested file", location: filepath.Join(base, "docs", "notes.md"), want: "notes"},
		{name: "symlink file", location: filepath.Join(base, "link.md"), wantErr: true},
		{name: "symlinked directory", location: filepath.Join(base, "linked-dir", "notes.md"), wantErr: true},
		{name: "dot dot escape", location: filepath.Join(base, "docs", "..", "..", filepath.Base(outside), "notes.md"), wantErr: true},
		{name: "absolute outside", location: filepath.Join(outside, "notes.md"), wantErr: true},
		{name: "relative path", location: "docs/notes.md", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := loadReleaseNotes(t.Context(), base, tt.location, "", "")
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("loadReleaseNotes(%q) error = %v, want ErrInvalidConfig", tt.location, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("loadReleaseNotes(%q) = %q, %v, want %q", tt.location, got, err, tt.want)
			}
		})
	}
}

func TestHostileFastlaneDownloadURLIsNeverUploaded(t *testing.T) {
	pngBytes := tinyPNG(t, 4)
	base := t.TempDir()
	secret := filepath.Join(base, "id_ed25519.png")
	writeTestFile(t, secret, string(pngBytes))

	resolved := mergeMetadata(PublishConfig{baseDir: base}, "", "dev.example", []*source.AppMetadata{
		{Source: "fastlane", IconURL: secret, ImageURLs: []string{secret}},
	})
	plan, _, err := prepareMedia(t.Context(), resolved, &apk.APKInfo{}, "https://cdn.example", true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.iconURL != "" || len(plan.imageURLs) != 0 || len(plan.blobs) != 0 {
		t.Errorf("a local path from a remote source was uploaded: %+v", plan)
	}
}
