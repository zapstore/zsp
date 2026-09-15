package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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

// TestGitHubDownloadSendsBearerToken confirms GitHub's Download method routes
// through the shared DownloadHTTP pipeline while still attaching the
// configured token as a bearer Authorization header.
func TestGitHubDownloadSendsBearerToken(t *testing.T) {
	var gotAuth string
	payload := []byte("apk-bytes-ok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	g := &GitHub{token: "test-token"}
	asset := &Asset{Name: "app.apk", URL: srv.URL + "/app.apk"}

	path, err := g.Download(context.Background(), asset, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer test-token")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %q, want %q", got, payload)
	}
	if asset.LocalPath != path {
		t.Fatalf("asset.LocalPath = %q, want %q", asset.LocalPath, path)
	}
}

// TestGitHubDownloadRejectsInsecureURL confirms GitHub asset downloads inherit
// the shared pipeline's HTTPS-outside-loopback validation.
func TestGitHubDownloadRejectsInsecureURL(t *testing.T) {
	g := &GitHub{}
	asset := &Asset{Name: "app.apk", URL: "http://evil.example.com/app.apk"}

	if _, err := g.Download(context.Background(), asset, t.TempDir(), nil); err == nil {
		t.Fatal("Download() error = nil, want rejection of insecure URL")
	}
}
