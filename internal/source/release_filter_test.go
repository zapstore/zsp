package source

import (
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

// TestReleaseFilterImplementations verifies that all Git-based sources
// implement the matchesReleaseFilter method correctly.
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

	cfg := &config.Config{}

	t.Run("GitHub", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg.ReleaseFilter = tt.releaseFilter
				gh := &GitHub{cfg: cfg}
				got := gh.matchesReleaseFilter(tt.tagName)
				if got != tt.want {
					t.Errorf("GitHub.matchesReleaseFilter(%q) = %v, want %v", tt.tagName, got, tt.want)
				}
			})
		}
	})

	t.Run("GitLab", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg.ReleaseFilter = tt.releaseFilter
				gl := &GitLab{cfg: cfg}
				got := gl.matchesReleaseFilter(tt.tagName)
				if got != tt.want {
					t.Errorf("GitLab.matchesReleaseFilter(%q) = %v, want %v", tt.tagName, got, tt.want)
				}
			})
		}
	})

	t.Run("Gitea", func(t *testing.T) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg.ReleaseFilter = tt.releaseFilter
				gt := &Gitea{cfg: cfg}
				got := gt.matchesReleaseFilter(tt.tagName)
				if got != tt.want {
					t.Errorf("Gitea.matchesReleaseFilter(%q) = %v, want %v", tt.tagName, got, tt.want)
				}
			})
		}
	})
}

// TestReleaseFilterInvalidRegex verifies that invalid regex patterns
// return false (safe default behavior).
func TestReleaseFilterInvalidRegex(t *testing.T) {
	cfg := &config.Config{
		ReleaseFilter: "[invalid(regex",
	}

	gh := &GitHub{cfg: cfg}
	if gh.matchesReleaseFilter("v1.0.0") {
		t.Error("Expected invalid regex to return false")
	}

	gl := &GitLab{cfg: cfg}
	if gl.matchesReleaseFilter("v1.0.0") {
		t.Error("Expected invalid regex to return false")
	}

	gt := &Gitea{cfg: cfg}
	if gt.matchesReleaseFilter("v1.0.0") {
		t.Error("Expected invalid regex to return false")
	}
}
