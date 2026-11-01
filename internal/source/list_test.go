package source

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubListReleases(t *testing.T) {
	const body = `[
		{"tag_name":"v3.0.0","name":"3.0.0","body":"notes three","draft":true,"assets":[{"name":"app.apk","browser_download_url":"https://example.com/draft.apk"}]},
		{"tag_name":"v2.0.0-beta.1","name":"beta","body":"try it","prerelease":true,"published_at":"2024-05-01T00:00:00Z","assets":[
			{"name":"app-arm64-v8a.apk","size":10,"browser_download_url":"https://example.com/a.apk","digest":"sha256:` + sha64 + `"},
			{"name":"app.dmg","size":20,"browser_download_url":"https://example.com/app.dmg"}]},
		{"tag_name":"v1.0.0","name":"1.0.0","assets":[{"name":"desktop.zip","browser_download_url":"https://example.com/d.zip"}]}
	]`
	var gotPage string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPage = r.URL.Query().Get("per_page")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	github := &GitHub{owner: "o", repo: "r", client: server.Client(), apiBase: server.URL, cacheDir: t.TempDir(),
		// a release filter and the pre-release choice must not hide anything
		filter: mustFilter(t, `^v1\.`)}
	releases, err := github.ListReleases(t.Context(), 25)
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if gotPage != "25" {
		t.Errorf("per_page = %q, want 25", gotPage)
	}
	if len(releases) != 2 {
		t.Fatalf("releases = %d, want 2: the draft is left out, the filter and pre-release choice are not applied", len(releases))
	}

	beta := releases[0]
	if beta.TagName != "v2.0.0-beta.1" || !beta.PreRelease || beta.Changelog != "try it" || beta.CreatedAt.IsZero() {
		t.Errorf("beta = %+v", beta)
	}
	if len(beta.Assets) != 1 || beta.Assets[0].Name != "app-arm64-v8a.apk" || beta.Assets[0].Size != 10 || beta.Assets[0].SHA256 != sha64 {
		t.Errorf("beta assets = %+v, want only the APK with its size and digest", beta.Assets)
	}
	if len(releases[1].Assets) != 0 {
		t.Errorf("a release with no APK keeps no assets, got %+v", releases[1].Assets)
	}
}

func TestListReleasesRejectsABadLimit(t *testing.T) {
	github := &GitHub{owner: "o", repo: "r", client: http.DefaultClient, apiBase: "http://127.0.0.1:1"}
	if _, err := github.ListReleases(t.Context(), 1000); err == nil || !strings.Contains(err.Error(), "max releases") {
		t.Fatalf("ListReleases(1000) = %v, want the max releases error", err)
	}
}

func TestGitLabListReleases(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/app/releases", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v2.0.0","upcoming_release":true,"assets":{"links":[{"name":"app.apk","url":"https://example.com/up.apk"}]}},
			{"tag_name":"v1.1.0-rc.1","description":"candidate","assets":{"links":[{"name":"app.apk","url":"https://example.com/rc.apk"},{"name":"notes","url":"https://example.com/notes.txt"}]}},
			{"tag_name":"v1.0.0","assets":{"links":[{"name":"app.apk","url":"https://example.com/app.apk"}]}}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	gitlab := &GitLab{baseURL: srv.URL, projectID: "app", numericProjectID: 1, client: srv.Client(), filter: mustFilter(t, `^nothing$`)}
	releases, err := gitlab.ListReleases(t.Context(), 0)
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(releases) != 2 || releases[0].TagName != "v1.1.0-rc.1" || !releases[0].PreRelease || releases[1].PreRelease {
		t.Fatalf("releases = %+v, want the rc (a pre-release) then v1.0.0, without the upcoming one", releases)
	}
	if len(releases[0].Assets) != 1 || releases[0].Assets[0].URL != "https://example.com/rc.apk" {
		t.Errorf("rc assets = %+v, want only the APK", releases[0].Assets)
	}
}

func TestGiteaListReleases(t *testing.T) {
	var gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		_, _ = w.Write([]byte(`[
			{"tag_name":"v2.0.0","draft":true,"assets":[{"name":"app.apk","browser_download_url":"https://example.com/d.apk"}]},
			{"tag_name":"nightly","name":"Nightly","prerelease":true,"created_at":"2024-05-01T00:00:00Z","assets":[{"name":"app.apk","size":5,"browser_download_url":"https://example.com/n.apk"}]}]`))
	}))
	t.Cleanup(srv.Close)

	gitea := &Gitea{baseURL: srv.URL, owner: "o", repo: "r", client: srv.Client()}
	releases, err := gitea.ListReleases(t.Context(), 0)
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if gotLimit != "10" {
		t.Errorf("limit = %q, want the default 10", gotLimit)
	}
	if len(releases) != 1 || releases[0].TagName != "nightly" || !releases[0].PreRelease || releases[0].Assets[0].Size != 5 {
		t.Fatalf("releases = %+v, want the nightly only", releases)
	}
}

const sha64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func mustFilter(t *testing.T, pattern string) *releaseFilter {
	t.Helper()
	filter, err := newReleaseFilter(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return filter
}
