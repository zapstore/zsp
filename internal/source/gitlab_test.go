package source

import (
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

func TestNewGitLabNestedProjectPath(t *testing.T) {
	cfg := &config.Config{
		Repository: "https://gitlab.example.com/group/subgroup/project",
	}

	g, err := NewGitLab(cfg)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	if g.baseURL != "https://gitlab.example.com" {
		t.Fatalf("baseURL = %q, want %q", g.baseURL, "https://gitlab.example.com")
	}
	if g.projectID != "group%2Fsubgroup%2Fproject" {
		t.Fatalf("projectID = %q, want %q", g.projectID, "group%2Fsubgroup%2Fproject")
	}
}

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
