package localpath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadFile(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(base, "icon.png"), "icon")
	writeFile(t, filepath.Join(base, "a", "b", "shot.png"), "shot")
	writeFile(t, filepath.Join(outside, "secret"), "secret")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(base, "link-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "link-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("icon.png", filepath.Join(base, "inside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		path         string
		want         string
		wantRejected bool
		wantMissing  bool
	}{
		{name: "relative file", path: "icon.png", want: "icon"},
		{name: "nested relative file", path: "a/b/shot.png", want: "shot"},
		{name: "dot segments inside base", path: "a/../icon.png", want: "icon"},
		{name: "absolute inside base", path: filepath.Join(base, "a", "b", "shot.png"), want: "shot"},
		{name: "symlink file", path: "link-file", wantRejected: true},
		{name: "symlink to file inside base", path: "inside-link", wantRejected: true},
		{name: "symlinked directory component", path: "link-dir/secret", wantRejected: true},
		{name: "parent escape", path: "../" + filepath.Base(outside) + "/secret", wantRejected: true},
		{name: "deep parent escape", path: "a/../../x", wantRejected: true},
		{name: "absolute outside base", path: filepath.Join(outside, "secret"), wantRejected: true},
		{name: "absolute sibling prefix", path: base + "-other/icon.png", wantRejected: true},
		{name: "directory", path: "dir", wantRejected: true},
		{name: "missing file", path: "missing.png", wantMissing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := ReadFile(base, tt.path, 1<<10)
			switch {
			case tt.wantRejected:
				if !errors.Is(err, ErrRejected) {
					t.Fatalf("ReadFile(%q) error = %v, want ErrRejected", tt.path, err)
				}
			case tt.wantMissing:
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("ReadFile(%q) error = %v, want not exist", tt.path, err)
				}
			default:
				if err != nil {
					t.Fatalf("ReadFile(%q): %v", tt.path, err)
				}
				if string(data) != tt.want {
					t.Errorf("ReadFile(%q) = %q, want %q", tt.path, data, tt.want)
				}
			}
		})
	}
}

func TestReadFileLimit(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "big"), "12345")
	if _, err := ReadFile(base, "big", 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadFile over limit error = %v, want ErrTooLarge", err)
	}
	if _, err := ReadFile(base, "big", 5); err != nil {
		t.Fatalf("ReadFile at limit: %v", err)
	}
}

func TestReadFileRequiresBaseDir(t *testing.T) {
	if _, err := ReadFile("", "icon.png", 1); !errors.Is(err, ErrRejected) {
		t.Fatalf("ReadFile without base error = %v, want ErrRejected", err)
	}
}

func TestReadFileThroughSymlinkedBase(t *testing.T) {
	real := t.TempDir()
	writeFile(t, filepath.Join(real, "icon.png"), "icon")
	link := filepath.Join(t.TempDir(), "base")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFile(link, "icon.png", 1<<10)
	if err != nil || string(data) != "icon" {
		t.Fatalf("ReadFile through symlinked base = %q, %v; the base itself may be a symlink", data, err)
	}
}

func TestReadDir(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(base, "shots", "2.png"), "2")
	writeFile(t, filepath.Join(base, "shots", "1.png"), "1")
	if err := os.Symlink(filepath.Join(outside, "x"), filepath.Join(base, "shots", "3.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "linked")); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadDir(base, "shots")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	var regular []string
	for _, entry := range entries {
		names = append(names, entry.Name())
		if entry.Type().IsRegular() {
			regular = append(regular, entry.Name())
		}
	}
	if len(names) != 3 || names[0] != "1.png" || names[2] != "3.png" {
		t.Errorf("ReadDir names = %v, want sorted [1.png 2.png 3.png]", names)
	}
	if len(regular) != 2 {
		t.Errorf("regular entries = %v, want the symlink excluded", regular)
	}
	if _, err := ReadDir(base, "linked"); !errors.Is(err, ErrRejected) {
		t.Errorf("ReadDir(symlinked dir) error = %v, want ErrRejected", err)
	}
	if _, err := ReadDir(base, "shots/1.png"); !errors.Is(err, ErrRejected) {
		t.Errorf("ReadDir(file) error = %v, want ErrRejected", err)
	}
	if _, err := ReadDir(base, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadDir(missing) error = %v, want not exist", err)
	}
}
