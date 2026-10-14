package source

import (
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

// TestReleaseFilterImplementations verifies the shared release filter.
func TestReleaseFilterImplementations(t *testing.T) {
	tests := []struct {
		name          string
		releaseFilter string
		tagName       string
		want          bool
	}{
		{
			name:          "no filter matches all",
			releaseFilter: "",
			tagName:       "v1.0.0",
			want:          true,
		},
		{
			name:          "K9MAIL prefix matches",
			releaseFilter: "^K9MAIL_.*",
			tagName:       "K9MAIL_17_0",
			want:          true,
		},
		{
			name:          "K9MAIL prefix does not match THUNDERBIRD",
			releaseFilter: "^K9MAIL_.*",
			tagName:       "THUNDERBIRD_18_0",
			want:          false,
		},
		{
			name:          "THUNDERBIRD prefix matches",
			releaseFilter: "^THUNDERBIRD_.*",
			tagName:       "THUNDERBIRD_18_0b4",
			want:          true,
		},
		{
			name:          "stable version pattern",
			releaseFilter: "^v[0-9]+\\.[0-9]+\\.[0-9]+$",
			tagName:       "v1.2.3",
			want:          true,
		},
		{
			name:          "stable version pattern excludes beta",
			releaseFilter: "^v[0-9]+\\.[0-9]+\\.[0-9]+$",
			tagName:       "v1.2.3-beta",
			want:          false,
		},
		{
			name:          "mainnet suffix matches",
			releaseFilter: ".*-mainnet$",
			tagName:       "phoenix-104-2.6.0-mainnet",
			want:          true,
		},
		{
			name:          "mainnet suffix does not match testnet",
			releaseFilter: ".*-mainnet$",
			tagName:       "phoenix-104-2.6.0-testnet",
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, err := newReleaseFilter(tt.releaseFilter)
			if err != nil {
				t.Fatalf("newReleaseFilter(%q) error = %v", tt.releaseFilter, err)
			}
			if got := filter.matches(tt.tagName); got != tt.want {
				t.Errorf("filter %q matches(%q) = %v, want %v", tt.releaseFilter, tt.tagName, got, tt.want)
			}
		})
	}
}

func TestReleaseFilterMatchesReleaseName(t *testing.T) {
	filter, err := newReleaseFilter("^Mobile")
	if err != nil {
		t.Fatalf("newReleaseFilter() error = %v", err)
	}
	if !filter.matches("v1.0.0", "Mobile 1.0.0") {
		t.Error("filter did not match the release name")
	}
	if filter.matches("v1.0.0", "Desktop 1.0.0") {
		t.Error("filter matched an unrelated name")
	}
}

// TestReleaseFilterInvalidRegex verifies that an invalid pattern is a
// construction error for every forge source, not a filter that hides every
// release.
func TestReleaseFilterInvalidRegex(t *testing.T) {
	const pattern = "[invalid(regex"
	if _, err := newReleaseFilter(pattern); err == nil {
		t.Fatal("newReleaseFilter() error = nil, want invalid pattern error")
	}

	constructors := map[string]func(*config.Config) error{
		"GitHub": func(cfg *config.Config) error { _, err := NewGitHub(cfg); return err },
		"GitLab": func(cfg *config.Config) error { _, err := NewGitLab(cfg); return err },
		"Gitea":  func(cfg *config.Config) error { _, err := NewGitea(cfg); return err },
	}
	repositories := map[string]string{
		"GitHub": "https://github.com/owner/app",
		"GitLab": "https://gitlab.com/owner/app",
		"Gitea":  "https://codeberg.org/owner/app",
	}
	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{Repository: repositories[name], ReleaseFilter: pattern}
			if err := construct(cfg); err == nil {
				t.Errorf("New%s() error = nil, want invalid release filter error", name)
			}
		})
	}
}

// TestReleaseFilterAppliesToEveryForge confirms a configured filter reaches
// each forge's release selection.
func TestReleaseFilterAppliesToEveryForge(t *testing.T) {
	cfg := func(repository string) *config.Config {
		return &config.Config{Repository: repository, ReleaseFilter: "^K9MAIL_"}
	}
	gh, err := NewGitHub(cfg("https://github.com/owner/app"))
	if err != nil {
		t.Fatal(err)
	}
	gl, err := NewGitLab(cfg("https://gitlab.com/owner/app"))
	if err != nil {
		t.Fatal(err)
	}
	gt, err := NewGitea(cfg("https://codeberg.org/owner/app"))
	if err != nil {
		t.Fatal(err)
	}
	for name, filter := range map[string]*releaseFilter{"GitHub": gh.filter, "GitLab": gl.filter, "Gitea": gt.filter} {
		if !filter.matches("K9MAIL_17_0") || filter.matches("THUNDERBIRD_18_0") {
			t.Errorf("%s filter did not apply the configured pattern", name)
		}
	}
}
