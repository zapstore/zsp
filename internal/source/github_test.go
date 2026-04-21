package source

import (
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

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
