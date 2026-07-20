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

func TestResolveGitLabDescriptionURL(t *testing.T) {
	tests := []struct {
		name             string
		baseURL          string
		numericProjectID int
		rawURL           string
		want             string
		wantOK           bool
	}{
		{
			name:             "relative upload uses numeric project path",
			baseURL:          "https://gitlab.com",
			numericProjectID: 6922885,
			rawURL:           "/uploads/b9f5d827145461a2195699660545160a/AuroraStore-4.8.3.apk",
			want:             "https://gitlab.com/-/project/6922885/uploads/b9f5d827145461a2195699660545160a/AuroraStore-4.8.3.apk",
			wantOK:           true,
		},
		{
			name:             "absolute https",
			baseURL:          "https://gitlab.com",
			numericProjectID: 6922885,
			rawURL:           "https://cdn.example.com/app.apk",
			want:             "https://cdn.example.com/app.apk",
			wantOK:           true,
		},
		{
			name:             "reject javascript",
			baseURL:          "https://gitlab.com",
			numericProjectID: 6922885,
			rawURL:           "javascript:alert(1)",
			wantOK:           false,
		},
		{
			name:             "reject non-upload relative",
			baseURL:          "https://gitlab.com",
			numericProjectID: 6922885,
			rawURL:           "/-/releases/4.8.3",
			wantOK:           false,
		},
		{
			name:             "missing numeric project id",
			baseURL:          "https://gitlab.com",
			numericProjectID: 0,
			rawURL:           "/uploads/abc/app.apk",
			wantOK:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveGitLabDescriptionURL(tt.baseURL, tt.numericProjectID, tt.rawURL)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tt.wantOK, got)
			}
			if tt.wantOK && got != tt.want {
				t.Fatalf("url = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConvertReleaseDescriptionUploads(t *testing.T) {
	g := &GitLab{
		cfg:              &config.Config{},
		baseURL:          "https://gitlab.com",
		projectID:        "AuroraOSS%2FAuroraStore",
		numericProjectID: 6922885,
	}
	release := g.convertRelease(&gitlabRelease{
		TagName: "4.8.3",
		Description: `Changelog : v4.8.3 (75)

[AuroraStore-4.8.3.apk](/uploads/b9f5d827145461a2195699660545160a/AuroraStore-4.8.3.apk)
[AuroraStore-hw-4.8.3.apk](/uploads/17293566483644dfe85c15c48a2d65ac/AuroraStore-hw-4.8.3.apk)
[AuroraStore-preload-4.8.3.apk](/uploads/90fd44162078169faaf4605ff8e9792f/AuroraStore-preload-4.8.3.apk)
[README](/uploads/deadbeef/README.md)
`,
	})

	if !HasValidAPKs(release.Assets) {
		t.Fatal("expected valid APKs from description uploads")
	}
	if len(release.Assets) != 3 {
		t.Fatalf("assets = %d, want 3 (got %#v)", len(release.Assets), release.Assets)
	}

	byName := map[string]string{}
	for _, a := range release.Assets {
		byName[a.Name] = a.URL
	}
	wantURL := "https://gitlab.com/-/project/6922885/uploads/b9f5d827145461a2195699660545160a/AuroraStore-4.8.3.apk"
	if byName["AuroraStore-4.8.3.apk"] != wantURL {
		t.Fatalf("AuroraStore-4.8.3.apk URL = %q, want %q", byName["AuroraStore-4.8.3.apk"], wantURL)
	}
	if _, ok := byName["AuroraStore-hw-4.8.3.apk"]; !ok {
		t.Fatal("missing hw apk")
	}
	if _, ok := byName["AuroraStore-preload-4.8.3.apk"]; !ok {
		t.Fatal("missing preload apk")
	}
}

func TestConvertReleaseDescriptionUploadsDoNotDuplicateLinks(t *testing.T) {
	g := &GitLab{
		cfg:              &config.Config{},
		baseURL:          "https://gitlab.com",
		projectID:        "AuroraOSS%2FAuroraStore",
		numericProjectID: 6922885,
	}
	cdn := "https://auroraoss.com/downloads/AuroraStore/Release/AuroraStore-4.7.5.apk"
	release := g.convertRelease(&gitlabRelease{
		TagName: "4.7.5",
		Description: `[AuroraStore-4.7.5.apk](/uploads/f0364970b6dce58618c2aea5153fd5f4/AuroraStore-4.7.5.apk)
[AuroraStore-hw-4.7.5.apk](/uploads/12486390c307c36310f3de404d7df238/AuroraStore-hw-4.7.5.apk)
`,
		Assets: struct {
			Links []gitlabAssetLink `json:"links"`
		}{
			Links: []gitlabAssetLink{{
				Name:           "AuroraStore-4.7.5",
				URL:            cdn,
				DirectAssetURL: cdn,
				LinkType:       "other",
			}},
		},
	})

	if len(release.Assets) != 2 {
		t.Fatalf("assets = %d, want 2 (link + unique description apk)", len(release.Assets))
	}
	if release.Assets[0].URL != cdn {
		t.Fatalf("first asset should remain CDN link, got %q", release.Assets[0].URL)
	}
	if release.Assets[0].Name != "AuroraStore-4.7.5.apk" {
		t.Fatalf("first asset name = %q, want AuroraStore-4.7.5.apk", release.Assets[0].Name)
	}
	if release.Assets[1].Name != "AuroraStore-hw-4.7.5.apk" {
		t.Fatalf("second asset name = %q, want AuroraStore-hw-4.7.5.apk", release.Assets[1].Name)
	}
}

func TestFetchLatestReleaseUsesDescriptionUploads(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/AuroraOSS%2FAuroraStore", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":6922885,"path_with_namespace":"AuroraOSS/AuroraStore"}`))
	})
	mux.HandleFunc("/api/v4/projects/AuroraOSS%2FAuroraStore/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
  {
    "tag_name": "4.8.3",
    "name": "4.8.3",
    "description": "[AuroraStore-4.8.3.apk](/uploads/abc/AuroraStore-4.8.3.apk)",
    "released_at": "2026-05-13T15:48:24.372Z",
    "_links": {"self": "https://gitlab.com/AuroraOSS/AuroraStore/-/releases/4.8.3"},
    "assets": {"links": []}
  },
  {
    "tag_name": "4.7.5",
    "name": "4.7.5",
    "description": "",
    "released_at": "2025-08-19T08:18:52.063Z",
    "_links": {"self": "https://gitlab.com/AuroraOSS/AuroraStore/-/releases/4.7.5"},
    "assets": {
      "links": [{
        "name": "AuroraStore-4.7.5",
        "url": "https://auroraoss.com/downloads/AuroraStore/Release/AuroraStore-4.7.5.apk",
        "direct_asset_url": "https://auroraoss.com/downloads/AuroraStore/Release/AuroraStore-4.7.5.apk",
        "link_type": "other"
      }]
    }
  }
]`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &GitLab{
		cfg:       &config.Config{},
		baseURL:   srv.URL,
		projectID: "AuroraOSS%2FAuroraStore",
		client:    srv.Client(),
		cacheDir:  t.TempDir(),
	}

	release, err := g.FetchLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("FetchLatestRelease() error = %v", err)
	}
	if release.Version != "4.8.3" {
		t.Fatalf("version = %q, want 4.8.3", release.Version)
	}
	if len(release.Assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(release.Assets))
	}
	want := srv.URL + "/-/project/6922885/uploads/abc/AuroraStore-4.8.3.apk"
	if release.Assets[0].URL != want {
		t.Fatalf("asset URL = %q, want %q", release.Assets[0].URL, want)
	}
	if g.numericProjectID != 6922885 {
		t.Fatalf("numericProjectID = %d, want 6922885", g.numericProjectID)
	}
}
