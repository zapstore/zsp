package release

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeepAPKCopiesOnce(t *testing.T) {
	src := filepath.Join(t.TempDir(), "app.apk")
	if err := os.WriteFile(src, []byte("apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "apk-cache")
	if err := keepAPK(src, dir, "abc"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "abc.apk")
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "apk" {
		t.Fatalf("copy = %q, %v", got, err)
	}
	if info, _ := os.Stat(dest); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v, want readable by the relay", info.Mode().Perm())
	}
	if err := os.WriteFile(src, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := keepAPK(src, dir, "abc"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "apk" {
		t.Fatal("an existing copy was replaced")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("%d files, want no temporary left", len(entries))
	}
}
