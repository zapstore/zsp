package source

import (
	"testing"
)

func TestGiteaCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()

	g := &Gitea{
		owner:    "Freeyourgadget",
		repo:     "Gadgetbridge",
		cacheDir: dir,
	}

	// No cache yet
	if got := g.GetPublishedVersion(); got != "" {
		t.Fatalf("expected empty version before any publish, got %q", got)
	}

	// Simulate FetchLatestRelease setting pendingVersion
	g.pendingVersion = "0.79.0"

	// CommitCache should write to disk and clear pendingVersion
	if err := g.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error: %v", err)
	}
	if g.pendingVersion != "" {
		t.Fatal("expected pendingVersion to be empty after CommitCache")
	}

	// GetPublishedVersion should read the written version
	if got := g.GetPublishedVersion(); got != "0.79.0" {
		t.Fatalf("GetPublishedVersion() = %q, want %q", got, "0.79.0")
	}

	// Commit with a new version
	g.pendingVersion = "0.80.0"
	if err := g.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error on second publish: %v", err)
	}
	if got := g.GetPublishedVersion(); got != "0.80.0" {
		t.Fatalf("GetPublishedVersion() after update = %q, want %q", got, "0.80.0")
	}

	// CommitCache with empty pendingVersion is a no-op
	if err := g.CommitCache(); err != nil {
		t.Fatalf("CommitCache() with empty pendingVersion should not error: %v", err)
	}
	if got := g.GetPublishedVersion(); got != "0.80.0" {
		t.Fatalf("GetPublishedVersion() after no-op CommitCache = %q, want %q", got, "0.80.0")
	}
}
