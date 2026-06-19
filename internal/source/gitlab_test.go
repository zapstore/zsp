package source

import (
	"testing"
)

func TestGitLabCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()

	g := &GitLab{
		projectID: "AuroraOSS%2FAuroraStore",
		cacheDir:  dir,
	}

	// No cache yet
	if got := g.GetPublishedVersion(); got != "" {
		t.Fatalf("expected empty version before any publish, got %q", got)
	}

	// Simulate FetchLatestRelease setting pendingVersion
	g.pendingVersion = "4.3.2"

	// CommitCache should write to disk and clear pendingVersion
	if err := g.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error: %v", err)
	}
	if g.pendingVersion != "" {
		t.Fatal("expected pendingVersion to be empty after CommitCache")
	}

	// GetPublishedVersion should read the written version
	if got := g.GetPublishedVersion(); got != "4.3.2" {
		t.Fatalf("GetPublishedVersion() = %q, want %q", got, "4.3.2")
	}

	// Commit with a new version
	g.pendingVersion = "4.4.0"
	if err := g.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error on second publish: %v", err)
	}
	if got := g.GetPublishedVersion(); got != "4.4.0" {
		t.Fatalf("GetPublishedVersion() after update = %q, want %q", got, "4.4.0")
	}

	// CommitCache with empty pendingVersion is a no-op
	if err := g.CommitCache(); err != nil {
		t.Fatalf("CommitCache() with empty pendingVersion should not error: %v", err)
	}
	if got := g.GetPublishedVersion(); got != "4.4.0" {
		t.Fatalf("GetPublishedVersion() after no-op CommitCache = %q, want %q", got, "4.4.0")
	}
}
