package source

import (
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

func TestGitHubCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()

	g := &GitHub{
		owner:    "owner",
		repo:     "repo",
		cacheDir: dir,
	}

	// No cache yet
	if got := g.GetPublishedVersion(); got != "" {
		t.Fatalf("expected empty version before any publish, got %q", got)
	}

	// Simulate FetchLatestRelease setting pending
	g.pending = &pendingCache{
		ETag:                          `"etag-v1"`,
		Release:                       &githubRelease{TagName: "v1.2.3"},
		LatestPublishedReleaseVersion: "1.2.3",
	}

	// CommitCache should write to disk and clear pending
	if err := g.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error: %v", err)
	}
	if g.pending != nil {
		t.Fatal("expected pending to be nil after CommitCache")
	}

	// GetPublishedVersion should read the written version
	if got := g.GetPublishedVersion(); got != "1.2.3" {
		t.Fatalf("GetPublishedVersion() = %q, want %q", got, "1.2.3")
	}

	// Commit with a new version
	g.pending = &pendingCache{
		ETag:                          `"etag-v2"`,
		Release:                       &githubRelease{TagName: "v2.0.0"},
		LatestPublishedReleaseVersion: "2.0.0",
	}
	if err := g.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error on second publish: %v", err)
	}
	if got := g.GetPublishedVersion(); got != "2.0.0" {
		t.Fatalf("GetPublishedVersion() after update = %q, want %q", got, "2.0.0")
	}

	// ClearCache should delete the file
	if err := g.ClearCache(); err != nil {
		t.Fatalf("ClearCache() error: %v", err)
	}
	if got := g.GetPublishedVersion(); got != "" {
		t.Fatalf("expected empty version after ClearCache, got %q", got)
	}

	// CommitCache with no pending is a no-op
	if err := g.CommitCache(); err != nil {
		t.Fatalf("CommitCache() with nil pending should not error: %v", err)
	}
}

func TestGitHub_matchesReleaseFilter(t *testing.T) {
	tests := []struct {
		name          string
		releaseFilter string
		tagName       string
		want          bool
	}{
		{
			name:          "no filter matches everything",
			releaseFilter: "",
			tagName:       "v1.0.0",
			want:          true,
		},
		{
			name:          "K9MAIL filter matches K9MAIL tag",
			releaseFilter: "^K9MAIL_.*",
			tagName:       "K9MAIL_17_0",
			want:          true,
		},
		{
			name:          "K9MAIL filter does not match THUNDERBIRD tag",
			releaseFilter: "^K9MAIL_.*",
			tagName:       "THUNDERBIRD_18_0b4",
			want:          false,
		},
		{
			name:          "THUNDERBIRD filter matches THUNDERBIRD tag",
			releaseFilter: "^THUNDERBIRD_.*",
			tagName:       "THUNDERBIRD_18_0b4",
			want:          true,
		},
		{
			name:          "THUNDERBIRD filter does not match K9MAIL tag",
			releaseFilter: "^THUNDERBIRD_.*",
			tagName:       "K9MAIL_17_0",
			want:          false,
		},
		{
			name:          "version pattern matches v-prefixed tags",
			releaseFilter: "^v[0-9]+\\.[0-9]+\\.[0-9]+$",
			tagName:       "v1.2.3",
			want:          true,
		},
		{
			name:          "version pattern does not match beta tags",
			releaseFilter: "^v[0-9]+\\.[0-9]+\\.[0-9]+$",
			tagName:       "v1.2.3-beta",
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GitHub{
				cfg: &config.Config{
					ReleaseFilter: tt.releaseFilter,
				},
			}
			got := g.matchesReleaseFilter(tt.tagName)
			if got != tt.want {
				t.Errorf("matchesReleaseFilter(%q) = %v, want %v", tt.tagName, got, tt.want)
			}
		})
	}
}
