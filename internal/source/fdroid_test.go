package source

import (
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

func TestFDroidCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()

	f := &FDroid{
		repoInfo: &config.FDroidRepoInfo{
			IndexURL:  "https://f-droid.org/repo/index-v1.json",
			PackageID: "de.danoeh.antennapod",
		},
		cacheDir: dir,
	}

	// No cache yet
	if got := f.GetPublishedVersion(); got != "" {
		t.Fatalf("expected empty version before any publish, got %q", got)
	}

	// Simulate FetchLatestRelease setting pending
	f.pending = &fdroidIndexCache{
		ETag:                          `"abc123"`,
		LatestPublishedReleaseVersion: "3.4.2",
	}

	// CommitCache should write to disk and clear pending
	if err := f.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error: %v", err)
	}
	if f.pending != nil {
		t.Fatal("expected pending to be nil after CommitCache")
	}

	// GetPublishedVersion should read the written version
	if got := f.GetPublishedVersion(); got != "3.4.2" {
		t.Fatalf("GetPublishedVersion() = %q, want %q", got, "3.4.2")
	}

	// Commit with a new version (simulating a second publish)
	f.pending = &fdroidIndexCache{
		ETag:                          `"def456"`,
		LatestPublishedReleaseVersion: "3.5.0",
	}
	if err := f.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error on second publish: %v", err)
	}
	if got := f.GetPublishedVersion(); got != "3.5.0" {
		t.Fatalf("GetPublishedVersion() after update = %q, want %q", got, "3.5.0")
	}

	// CommitCache with no pending is a no-op
	if err := f.CommitCache(); err != nil {
		t.Fatalf("CommitCache() with nil pending should not error: %v", err)
	}
}
