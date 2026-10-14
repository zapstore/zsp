package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestGitHubDownloadSendsBearerToken confirms GitHub's Download method routes
// through the shared download pipeline while attaching the configured token
// as a bearer Authorization header, but only to GitHub hosts.
func TestGitHubDownloadSendsBearerToken(t *testing.T) {
	payload := []byte("apk-bytes-ok")
	tests := []struct {
		name     string
		url      string
		wantAuth string
	}{
		{"github.com", "https://github.com/o/r/releases/download/v1/app.apk", "Bearer test-token"},
		{"api.github.com", "https://api.github.com/repos/o/r/releases/assets/1", "Bearer test-token"},
		{"CDN host", "https://objects.githubusercontent.com/app.apk", ""},
		{"lookalike host", "https://github.com.evil.example/app.apk", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("GITHUB_TOKEN", "test-token")
			var gotAuth string
			g := &GitHub{downloadClient: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				gotAuth = req.Header.Get("Authorization")
				return &http.Response{
					StatusCode:    http.StatusOK,
					Body:          io.NopCloser(bytes.NewReader(payload)),
					ContentLength: int64(len(payload)),
					Header:        make(http.Header),
					Request:       req,
				}, nil
			})}}
			asset := &Asset{Name: "app.apk", URL: tt.url}

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
			if asset.LocalPath != path {
				t.Fatalf("asset.LocalPath = %q, want %q", asset.LocalPath, path)
			}
		})
	}
}

func TestGitHubFetchLatestReleaseUsesETag(t *testing.T) {
	const etag = `"rel-1"`
	const body = `{"tag_name":"v1.0.0","name":"1.0.0","draft":false,"prerelease":false,"assets":[{"name":"app.apk","browser_download_url":"https://example.com/app.apk","size":1}]}`
	var ifNone []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ifNone = append(ifNone, request.Header.Get("If-None-Match"))
		if request.Header.Get("If-None-Match") == etag {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		writer.Header().Set("ETag", etag)
		_, _ = writer.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	github := &GitHub{
		owner:    "owner",
		repo:     "repo",
		client:   server.Client(),
		cacheDir: t.TempDir(),
		apiBase:  server.URL,
	}

	release, err := github.FetchLatestRelease(t.Context())
	if err != nil {
		t.Fatalf("first FetchLatestRelease() = %v", err)
	}
	if release.Version != "1.0.0" {
		t.Fatalf("version = %q, want 1.0.0", release.Version)
	}
	if github.pendingCache == nil || github.pendingCache.ETag != etag {
		t.Fatalf("pendingCache = %+v, want ETag %q", github.pendingCache, etag)
	}
	if err := github.CommitCache(); err != nil {
		t.Fatal(err)
	}

	_, err = github.FetchLatestRelease(t.Context())
	if !errors.Is(err, ErrNotModified) {
		t.Fatalf("second FetchLatestRelease() = %v, want ErrNotModified", err)
	}

	if err := github.ClearHTTPCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := github.FetchLatestRelease(t.Context()); err != nil {
		t.Fatalf("FetchLatestRelease after ClearHTTPCache() = %v", err)
	}

	github.SkipHTTPCache = true
	release, err = github.FetchLatestRelease(t.Context())
	if err != nil {
		t.Fatalf("SkipHTTPCache FetchLatestRelease() = %v", err)
	}
	if release.Version != "1.0.0" {
		t.Fatalf("SkipHTTPCache version = %q, want 1.0.0", release.Version)
	}
	if len(ifNone) != 4 || ifNone[0] != "" || ifNone[1] != etag || ifNone[2] != "" || ifNone[3] != "" {
		t.Fatalf("If-None-Match headers = %v, want [\"\", %q, \"\", \"\"]", ifNone, etag)
	}
}

// TestGitHubFetchLatestReleaseIgnoresETagOnlyChanges covers a release whose
// ETag moved because of volatile fields (asset download counts) while the
// content zsp consumes stayed the same: the fetch reports ErrNotModified and
// refreshes the stored validators.
func TestGitHubFetchLatestReleaseIgnoresETagOnlyChanges(t *testing.T) {
	body := func(downloads int) string {
		return fmt.Sprintf(`{"tag_name":"v1.0.0","name":"1.0.0","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z","assets":[{"name":"app.apk","browser_download_url":"https://example.com/app.apk","size":1,"download_count":%d}]}`, downloads)
	}
	var ifNone []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ifNone = append(ifNone, request.Header.Get("If-None-Match"))
		switch request.Header.Get("If-None-Match") {
		case `"rel-1"`:
			writer.Header().Set("ETag", `"rel-2"`)
			_, _ = writer.Write([]byte(body(8000)))
		case `"rel-2"`:
			writer.WriteHeader(http.StatusNotModified)
		default:
			writer.Header().Set("ETag", `"rel-1"`)
			_, _ = writer.Write([]byte(body(1)))
		}
	}))
	t.Cleanup(server.Close)

	github := &GitHub{
		owner:    "owner",
		repo:     "repo",
		client:   server.Client(),
		cacheDir: t.TempDir(),
		apiBase:  server.URL,
	}

	release, err := github.FetchLatestRelease(t.Context())
	if err != nil {
		t.Fatalf("first FetchLatestRelease() = %v", err)
	}
	if !HasValidAPKs(release.Assets) {
		t.Fatalf("first release assets = %+v, want a valid APK", release.Assets)
	}
	if err := github.CommitCache(); err != nil {
		t.Fatal(err)
	}

	_, err = github.FetchLatestRelease(t.Context())
	if !errors.Is(err, ErrNotModified) {
		t.Fatalf("second FetchLatestRelease() = %v, want ErrNotModified", err)
	}
	if github.pendingCache == nil || github.pendingCache.ETag != `"rel-2"` {
		t.Fatalf("pendingCache = %+v, want refreshed ETag %q", github.pendingCache, `"rel-2"`)
	}
	if err := github.CommitCache(); err != nil {
		t.Fatal(err)
	}

	_, err = github.FetchLatestRelease(t.Context())
	if !errors.Is(err, ErrNotModified) {
		t.Fatalf("third FetchLatestRelease() = %v, want ErrNotModified", err)
	}
	if len(ifNone) != 3 || ifNone[0] != "" || ifNone[1] != `"rel-1"` || ifNone[2] != `"rel-2"` {
		t.Fatalf("If-None-Match headers = %v, want [\"\" %q %q]", ifNone, `"rel-1"`, `"rel-2"`)
	}
}

func TestGitHubListHonorsMaxReleases(t *testing.T) {
	const apkRelease = `[{"tag_name":"v1.0.0","name":"1.0.0","draft":false,"prerelease":false,"assets":[{"name":"app.apk","browser_download_url":"https://example.com/app.apk","size":1}]}]`
	const desktopRelease = `[{"tag_name":"v9.0.0","name":"desktop","draft":false,"prerelease":false,"assets":[{"name":"app.dmg","browser_download_url":"https://example.com/app.dmg"}]}]`

	tests := []struct {
		name      string
		limit     int
		body      string
		wantPage  string
		wantError string
	}{
		{name: "default", wantPage: "10", body: apkRelease},
		{name: "override", limit: 25, wantPage: "25", body: apkRelease},
		{name: "error names the scan size", limit: 25, wantPage: "25", body: desktopRelease, wantError: "last 25 releases"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPage string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/releases/latest") {
					_, _ = writer.Write([]byte(`{"tag_name":"v9.0.0","draft":false,"assets":[{"name":"app.dmg","browser_download_url":"https://example.com/app.dmg"}]}`))
					return
				}
				gotPage = request.URL.Query().Get("per_page")
				_, _ = writer.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)

			github := &GitHub{
				owner:       "owner",
				repo:        "repo",
				client:      server.Client(),
				cacheDir:    t.TempDir(),
				apiBase:     server.URL,
				maxReleases: tt.limit,
			}
			release, err := github.FetchLatestRelease(t.Context())
			if gotPage != tt.wantPage {
				t.Fatalf("per_page = %q, want %q", gotPage, tt.wantPage)
			}
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("FetchLatestRelease() = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("FetchLatestRelease() = %v", err)
			}
			if release.Version != "1.0.0" {
				t.Fatalf("version = %q, want 1.0.0", release.Version)
			}
		})
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

func TestGitHubReleaseAssetDigest(t *testing.T) {
	hexSum := strings.Repeat("cd", 32)
	g := &GitHub{}
	release := g.convertRelease(&githubRelease{
		TagName: "v1.0.0",
		Assets: []githubAsset{
			{Name: "a.apk", BrowserDownloadURL: "https://github.com/o/r/a.apk", Digest: "sha256:" + hexSum},
			{Name: "b.apk", BrowserDownloadURL: "https://github.com/o/r/b.apk"},
			{Name: "c.apk", BrowserDownloadURL: "https://github.com/o/r/c.apk", Digest: "sha512:abcd"},
		},
	})
	want := []string{hexSum, "", ""}
	for i, asset := range release.Assets {
		if asset.SHA256 != want[i] {
			t.Errorf("asset %s SHA256 = %q, want %q", asset.Name, asset.SHA256, want[i])
		}
	}
}

func TestGitHubDownloadRejectsDigestMismatch(t *testing.T) {
	g := &GitHub{downloadClient: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, "tampered"), nil
	})}}
	asset := &Asset{Name: "app.apk", URL: "https://github.com/o/r/app.apk", SHA256: strings.Repeat("0", 64)}
	if _, err := g.Download(context.Background(), asset, t.TempDir(), nil); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Download() error = %v, want ErrChecksumMismatch", err)
	}
}

func TestGitHubRateLimitHintWithoutToken(t *testing.T) {
	t.Chdir(t.TempDir())
	tests := []struct {
		name      string
		token     string
		wantHint  bool
		wantLimit bool
	}{
		{"no token", "", true, true},
		{"token set", "ghp_test", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", tt.token)
			// The test client has no Tor fallback, so the direct 403 surfaces.
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				resp := testResponse(http.StatusForbidden, "")
				resp.Header.Set("X-RateLimit-Remaining", "0")
				resp.Request = req
				return resp, nil
			})}
			g := &GitHub{owner: "o", repo: "r", client: client, apiBase: "https://api.github.com", cacheDir: t.TempDir()}
			_, err := g.FetchLatestRelease(t.Context())
			if errors.Is(err, ErrRateLimited) != tt.wantLimit {
				t.Fatalf("FetchLatestRelease() = %v, rate limited want %v", err, tt.wantLimit)
			}
			if got := strings.Contains(err.Error(), "GITHUB_TOKEN"); got != tt.wantHint {
				t.Errorf("error %q mentions GITHUB_TOKEN = %v, want %v", err, got, tt.wantHint)
			}
		})
	}
}
