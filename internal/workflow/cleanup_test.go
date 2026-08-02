package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zapstore/zsp/internal/source"
)

func TestDeleteDownloadedAPKRemovesCacheAndTemp(t *testing.T) {
	cacheDir := source.DownloadCacheDir()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}

	url := "https://example.com/app-cleanup-test.apk"
	name := "app-cleanup-test.apk"

	cachedPath := filepath.Join(cacheDir, source.DownloadCacheKey(url)+"_"+name)
	if err := os.WriteFile(cachedPath, []byte("cached-apk"), 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(cachedPath) })

	// Use system temp so isManagedDownloadPath recognizes it.
	tempPath := filepath.Join(os.TempDir(), "zsp-cleanup-test-"+name)
	if err := os.WriteFile(tempPath, []byte("temp-apk"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tempPath) })

	t.Run("removes cache entry by URL", func(t *testing.T) {
		p := &Publisher{
			selectedAsset: &source.Asset{URL: url, Name: name, LocalPath: cachedPath},
			apkPath:       cachedPath,
		}
		p.deleteDownloadedAPK()
		if _, err := os.Stat(cachedPath); !os.IsNotExist(err) {
			t.Fatalf("cached APK still exists: %v", err)
		}
	})

	t.Run("removes temp staging path", func(t *testing.T) {
		if err := os.WriteFile(tempPath, []byte("temp-apk"), 0o644); err != nil {
			t.Fatalf("rewrite temp file: %v", err)
		}
		p := &Publisher{
			selectedAsset: &source.Asset{URL: url, Name: name, LocalPath: tempPath},
			apkPath:       tempPath,
		}
		p.deleteDownloadedAPK()
		if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
			t.Fatalf("temp APK still exists: %v", err)
		}
	})

	t.Run("never deletes local APK without URL", func(t *testing.T) {
		localPath := filepath.Join(t.TempDir(), "local.apk")
		if err := os.WriteFile(localPath, []byte("local"), 0o644); err != nil {
			t.Fatalf("write local file: %v", err)
		}
		p := &Publisher{
			selectedAsset: &source.Asset{Name: "local.apk", LocalPath: localPath},
			apkPath:       localPath,
		}
		p.deleteDownloadedAPK()
		if _, err := os.Stat(localPath); err != nil {
			t.Fatalf("local APK was deleted: %v", err)
		}
	})
}

func TestIsManagedDownloadPath(t *testing.T) {
	cacheFile := filepath.Join(source.DownloadCacheDir(), "abc_app.apk")
	tempFile := filepath.Join(os.TempDir(), "app.apk")
	// t.TempDir() lives under os.TempDir() on macOS; use a path outside both.
	elsewhere := filepath.Join(string(filepath.Separator)+"var", "nonexistent-zsp-local", "app.apk")

	if !isManagedDownloadPath(cacheFile) {
		t.Fatalf("cache path should be managed: %s", cacheFile)
	}
	if !isManagedDownloadPath(tempFile) {
		t.Fatalf("temp path should be managed: %s", tempFile)
	}
	if isManagedDownloadPath(elsewhere) {
		t.Fatalf("unrelated path should not be managed: %s", elsewhere)
	}
}
