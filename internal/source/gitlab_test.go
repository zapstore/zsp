package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestPickGitLabAssetURL(t *testing.T) {
	cdn := "https://releases.example.org/app.apk"
	direct := "https://gitlab.com/group/proj/-/releases/v1/downloads/app.apk"

	tests := []struct {
		name string
		link gitlabAssetLink
		want string
	}{
		{
			name: "prefer external url over gitlab direct interstitial",
			link: gitlabAssetLink{URL: cdn, DirectAssetURL: direct},
			want: cdn,
		},
		{
			name: "fall back to direct when url empty",
			link: gitlabAssetLink{DirectAssetURL: direct},
			want: direct,
		},
		{
			name: "url only",
			link: gitlabAssetLink{URL: cdn},
			want: cdn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickGitLabAssetURL(tt.link); got != tt.want {
				t.Fatalf("pickGitLabAssetURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseGitLabExternalRedirect(t *testing.T) {
	external := "https://releases.ironfoxoss.org/ironfox/releases/152.0.6/arm64-v8a/ironfox-152.0.6-arm64-v8a.apk"
	body := []byte(`<div class="tree-holder">
<h2>You are being redirected away from GitLab</h2>
<p>Redirect url is an external url, it may contain user-generated content and malicious code. Do not continue unless you trust the author and source.</p>
</div>
<div>
<a href="` + external + `" target="_blank" rel="noopener noreferrer"> Click here to redirect to ` + external + ` </a>
</div>
`)

	got, ok := parseGitLabExternalRedirect(body)
	if !ok {
		t.Fatal("expected to parse external redirect")
	}
	if got != external {
		t.Fatalf("parseGitLabExternalRedirect() = %q, want %q", got, external)
	}

	if _, ok := parseGitLabExternalRedirect([]byte(`<html><a href="https://evil.example/a.apk">x</a></html>`)); ok {
		t.Fatal("should not treat arbitrary HTML as GitLab redirect")
	}
	if _, ok := parseGitLabExternalRedirect([]byte(gitlabExternalRedirectMarker + `<a href="javascript:alert(1)">x</a>`)); ok {
		t.Fatal("should reject non-http(s) href")
	}
}

func TestGitLabDownloadFollowsExternalInterstitial(t *testing.T) {
	apkBytes := []byte("PK\x03\x04fake-apk-content")
	var cdnHits, gitlabHits int

	mux := http.NewServeMux()
	mux.HandleFunc("/cdn/app.apk", func(w http.ResponseWriter, r *http.Request) {
		cdnHits++
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		_, _ = w.Write(apkBytes)
	})
	mux.HandleFunc("/group/proj/-/releases/v1/downloads/app.apk", func(w http.ResponseWriter, r *http.Request) {
		gitlabHits++
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<div class="tree-holder">
<h2>You are being redirected away from GitLab</h2>
</div>
<a href="http://` + r.Host + `/cdn/app.apk">Click here to redirect</a>
`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &GitLab{
		cfg:               &config.Config{},
		SkipDownloadCache: true,
	}
	destDir := t.TempDir()
	asset := &Asset{
		Name: "app.apk",
		URL:  srv.URL + "/group/proj/-/releases/v1/downloads/app.apk",
	}

	path, err := g.Download(context.Background(), asset, destDir, nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(apkBytes) {
		t.Fatalf("downloaded content = %q, want %q", got, apkBytes)
	}
	if gitlabHits != 1 {
		t.Fatalf("gitlabHits = %d, want 1", gitlabHits)
	}
	if cdnHits != 1 {
		t.Fatalf("cdnHits = %d, want 1", cdnHits)
	}
	if !strings.HasSuffix(filepath.Base(path), "app.apk") {
		t.Fatalf("unexpected path %q", path)
	}
}

func TestConvertReleasePrefersExternalURL(t *testing.T) {
	g := &GitLab{cfg: &config.Config{}}
	release := g.convertRelease(&gitlabRelease{
		TagName: "v1.2.3",
		Assets: struct {
			Links []gitlabAssetLink `json:"links"`
		}{
			Links: []gitlabAssetLink{{
				Name:           "ironfox-1.2.3-arm64-v8a.apk",
				URL:            "https://releases.example.org/ironfox-1.2.3-arm64-v8a.apk",
				DirectAssetURL: "https://gitlab.com/ironfox-oss/IronFox/-/releases/v1.2.3/downloads/ironfox-1.2.3-arm64-v8a.apk",
				LinkType:       "package",
			}},
		},
	})
	if len(release.Assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(release.Assets))
	}
	if got := release.Assets[0].URL; got != "https://releases.example.org/ironfox-1.2.3-arm64-v8a.apk" {
		t.Fatalf("asset URL = %q, want CDN url", got)
	}
	if got := release.Assets[0].Name; got != "ironfox-1.2.3-arm64-v8a.apk" {
		t.Fatalf("asset Name = %q, want ironfox-1.2.3-arm64-v8a.apk", got)
	}
}
