package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalSource(t *testing.T) {
	testdataDir := filepath.Join("..", "..", "testdata", "apks")

	t.Run("single file", func(t *testing.T) {
		path := filepath.Join(testdataDir, "sample.apk")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Skipf("test APK not found: %s", path)
		}

		src, err := NewLocal(path)
		if err != nil {
			t.Fatalf("NewLocal() error: %v", err)
		}

		release, err := src.FetchLatestRelease(context.Background())
		if err != nil {
			t.Fatalf("FetchLatestRelease() error: %v", err)
		}

		if len(release.Assets) != 1 {
			t.Errorf("expected 1 asset, got %d", len(release.Assets))
		}

		if release.Assets[0].Name != "sample.apk" {
			t.Errorf("expected asset name 'sample.apk', got %q", release.Assets[0].Name)
		}

		// Test download (should just return the path)
		localPath, err := src.Download(context.Background(), release.Assets[0], "", nil)
		if err != nil {
			t.Fatalf("Download() error: %v", err)
		}

		if localPath != release.Assets[0].LocalPath {
			t.Errorf("Download() returned different path: %q vs %q", localPath, release.Assets[0].LocalPath)
		}
	})

	t.Run("glob pattern", func(t *testing.T) {
		pattern := filepath.Join(testdataDir, "*.apk")

		src, err := NewLocal(pattern)
		if err != nil {
			t.Fatalf("NewLocal() error: %v", err)
		}

		release, err := src.FetchLatestRelease(context.Background())
		if err != nil {
			t.Fatalf("FetchLatestRelease() error: %v", err)
		}

		if len(release.Assets) == 0 {
			t.Error("expected at least one asset")
		}

		t.Logf("Found %d APK files", len(release.Assets))
		for _, asset := range release.Assets {
			t.Logf("  - %s (%d bytes)", asset.Name, asset.Size)
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		src, err := NewLocal("/nonexistent/path/app.apk")
		if err != nil {
			t.Fatalf("NewLocal() error: %v", err)
		}

		_, err = src.FetchLatestRelease(context.Background())
		if err == nil {
			t.Error("expected error for non-existent file")
		}
	})

	t.Run("empty pattern", func(t *testing.T) {
		_, err := NewLocal("")
		if err == nil {
			t.Error("expected error for empty pattern")
		}
	})
}

