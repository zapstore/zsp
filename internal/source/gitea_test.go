package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestGiteaDownloadSendsTokenHeader confirms Gitea's Download method routes
// through the shared download pipeline while attaching the configured token
// as a Gitea-style Authorization header, but only to GITEA_HOST.
func TestGiteaDownloadSendsTokenHeader(t *testing.T) {
	payload := []byte("apk-bytes-ok")
	tests := []struct {
		name      string
		giteaHost func(srvHost string) string
		wantAuth  string
	}{
		{"configured host gets the token", func(host string) string { return host }, "token test-token"},
		{"other host gets no token", func(string) string { return "codeberg.org" }, ""},
		{"host with a matching name hint gets no token", func(string) string { return "gitea.example.com" }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				_, _ = w.Write(payload)
			}))
			defer srv.Close()
			t.Setenv("GITEA_TOKEN", "test-token")
			t.Setenv("GITEA_HOST", tt.giteaHost(strings.TrimPrefix(srv.URL, "http://")))

			g := &Gitea{}
			asset := &Asset{Name: "app.apk", URL: srv.URL + "/app.apk"}
			path, err := g.Download(context.Background(), asset, t.TempDir(), nil)
			if err != nil {
				t.Fatalf("Download() error = %v", err)
			}
			if gotAuth != tt.wantAuth {
				t.Fatalf("Authorization header = %q, want %q", gotAuth, tt.wantAuth)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != string(payload) {
				t.Fatalf("downloaded %q, want %q", got, payload)
			}
		})
	}
}

func TestGiteaFetchUsesMaxReleases(t *testing.T) {
	var gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag_name":"v1.0.0","name":"1.0.0","draft":false,"prerelease":false,"assets":[{"name":"app.apk","browser_download_url":"https://example.com/app.apk","size":1}]}]`))
	}))
	t.Cleanup(srv.Close)

	gitea := &Gitea{
		baseURL:     srv.URL,
		owner:       "owner",
		repo:        "repo",
		client:      srv.Client(),
		maxReleases: 25,
	}
	if _, err := gitea.FetchLatestRelease(t.Context()); err != nil {
		t.Fatalf("FetchLatestRelease() = %v", err)
	}
	if gotLimit != "25" {
		t.Fatalf("limit = %q, want 25", gotLimit)
	}
}

// TestGiteaDownloadRejectsInsecureURL confirms Gitea asset downloads inherit
// the shared pipeline's HTTPS-outside-loopback validation.
func TestGiteaDownloadRejectsInsecureURL(t *testing.T) {
	g := &Gitea{}
	asset := &Asset{Name: "app.apk", URL: "http://evil.example.com/app.apk"}

	if _, err := g.Download(context.Background(), asset, t.TempDir(), nil); err == nil {
		t.Fatal("Download() error = nil, want rejection of insecure URL")
	}
}
