package source

import (
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

func TestWebCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()

	w := &Web{
		cfg: &config.Config{
			ReleaseSource: &config.ReleaseSource{
				URL: "https://example.com/releases/app",
			},
		},
		cacheDir: dir,
	}

	// No cache yet
	if got := w.GetPublishedVersion(); got != "" {
		t.Fatalf("expected empty version before any publish, got %q", got)
	}

	// Simulate FetchLatestRelease setting pendingCache (version extractor mode)
	w.pendingCache = &webCache{
		Version:                       "2.1.0",
		AssetURL:                      "https://example.com/releases/app-2.1.0.apk",
		LatestPublishedReleaseVersion: "2.1.0",
	}

	// CommitCache should write to disk and clear pendingCache
	if err := w.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error: %v", err)
	}
	if w.pendingCache != nil {
		t.Fatal("expected pendingCache to be nil after CommitCache")
	}

	// GetPublishedVersion should read the written version
	if got := w.GetPublishedVersion(); got != "2.1.0" {
		t.Fatalf("GetPublishedVersion() = %q, want %q", got, "2.1.0")
	}

	// Commit with a new version
	w.pendingCache = &webCache{
		Version:                       "2.2.0",
		AssetURL:                      "https://example.com/releases/app-2.2.0.apk",
		LatestPublishedReleaseVersion: "2.2.0",
	}
	if err := w.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error on second publish: %v", err)
	}
	if got := w.GetPublishedVersion(); got != "2.2.0" {
		t.Fatalf("GetPublishedVersion() after update = %q, want %q", got, "2.2.0")
	}

	// ClearCache should delete the file
	if err := w.ClearCache(); err != nil {
		t.Fatalf("ClearCache() error: %v", err)
	}
	if got := w.GetPublishedVersion(); got != "" {
		t.Fatalf("expected empty version after ClearCache, got %q", got)
	}

	// CommitCache with nil pendingCache is a no-op
	if err := w.CommitCache(); err != nil {
		t.Fatalf("CommitCache() with nil pendingCache should not error: %v", err)
	}
}
