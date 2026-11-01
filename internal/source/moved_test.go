package source

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zapstore/zsp/v2/internal/config"
)

// renamedForge starts a local fake forge that renamed owner/old to a new path: /api/old redirects
// with apiStatus, and the web page /owner/old redirects with webStatus to
// webTo(server URL), or answers 200 when webStatus is 200.
func renamedForge(t *testing.T, apiStatus, webStatus int, webTo func(base string) string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/by-id", apiStatus)
	})
	mux.HandleFunc("/api/by-id", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	mux.HandleFunc("/api/plain", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	mux.HandleFunc("/owner/old", func(w http.ResponseWriter, r *http.Request) {
		if webStatus == http.StatusOK {
			_, _ = w.Write([]byte("page"))
			return
		}
		http.Redirect(w, r, webTo(server.URL), webStatus)
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func giteaPath(rawURL string) string {
	_, path := config.GetGiteaRepo(rawURL)
	return path
}

func observeAPI(t *testing.T, server *httptest.Server, probe *movedProbe, path string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	probe.observe(t.Context(), client, resp)
}

func TestMovedProbeReports(t *testing.T) {
	toNew := func(base string) string { return base + "/owner/new" }
	tests := []struct {
		name      string
		apiStatus int
		webStatus int
		webTo     func(base string) string
		apiPath   string
		want      bool
	}{
		{"permanent redirect to a new path", http.StatusMovedPermanently, http.StatusMovedPermanently, toNew, "/api/old", true},
		{"308 counts as permanent and a relative target resolves", http.StatusPermanentRedirect, http.StatusFound, func(string) string { return "/owner/new" }, "/api/old", true},
		{"temporary API redirect is ignored", http.StatusFound, http.StatusMovedPermanently, toNew, "/api/old", false},
		{"no redirect", http.StatusMovedPermanently, http.StatusMovedPermanently, toNew, "/api/plain", false},
		{"web page does not move", http.StatusMovedPermanently, http.StatusOK, nil, "/api/old", false},
		{"same path in another case", http.StatusMovedPermanently, http.StatusMovedPermanently, func(base string) string { return base + "/Owner/Old" }, "/api/old", false},
		{"another host is ignored", http.StatusMovedPermanently, http.StatusMovedPermanently, func(string) string { return "https://example.org/owner/new" }, "/api/old", false},
		{"a path that is not a repository", http.StatusMovedPermanently, http.StatusMovedPermanently, func(base string) string { return base + "/login" }, "/api/old", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := renamedForge(t, tt.apiStatus, tt.webStatus, tt.webTo)
			var got []string
			probe := newMovedProbe(func(repository string) { got = append(got, repository) }, server.URL+"/owner/old", "owner/old", giteaPath)
			observeAPI(t, server, probe, tt.apiPath)

			if !tt.want {
				if len(got) != 0 {
					t.Fatalf("OnMoved called with %v, want no call", got)
				}
				return
			}
			if len(got) != 1 || got[0] != server.URL+"/owner/new" {
				t.Fatalf("OnMoved = %v, want [%s/owner/new]", got, server.URL)
			}
		})
	}
}

func TestMovedProbeReportsOnce(t *testing.T) {
	server := renamedForge(t, http.StatusMovedPermanently, http.StatusMovedPermanently, func(base string) string { return base + "/owner/new" })
	calls := 0
	probe := newMovedProbe(func(string) { calls++ }, server.URL+"/owner/old", "owner/old", giteaPath)
	observeAPI(t, server, probe, "/api/old")
	observeAPI(t, server, probe, "/api/old")
	if calls != 1 {
		t.Fatalf("OnMoved called %d times, want 1", calls)
	}
}

func TestMovedProbeNilIsSafe(t *testing.T) {
	server := renamedForge(t, http.StatusMovedPermanently, http.StatusMovedPermanently, func(base string) string { return base + "/owner/new" })
	probe := newMovedProbe(nil, server.URL+"/owner/old", "owner/old", giteaPath)
	if probe != nil {
		t.Fatalf("newMovedProbe(nil) = %v, want nil", probe)
	}
	observeAPI(t, server, probe, "/api/old")
}

func TestGitHubReportsMovedRepository(t *testing.T) {
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/old/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/repositories/7/releases/latest", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/repositories/7/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","name":"v1.0.0","assets":[{"name":"app.apk","size":1,"browser_download_url":"https://example.com/app.apk"}]}`))
	})
	mux.HandleFunc("/owner/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+"/owner/new", http.StatusMovedPermanently)
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	var got []string
	github := &GitHub{
		owner:    "owner",
		repo:     "old",
		client:   server.Client(),
		apiBase:  server.URL,
		cacheDir: t.TempDir(),
		moved:    newMovedProbe(func(repository string) { got = append(got, repository) }, server.URL+"/owner/old", "owner/old", giteaPath),
	}
	release, err := github.FetchLatestRelease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "v1.0.0" {
		t.Fatalf("TagName = %q, want v1.0.0", release.TagName)
	}
	if len(got) != 1 || got[0] != server.URL+"/owner/new" {
		t.Fatalf("OnMoved = %v, want [%s/owner/new]", got, server.URL)
	}
}
