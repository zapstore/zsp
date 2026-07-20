package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zapstore/zsp/internal/config"
)

func TestWebCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()

	w := &Web{
		cfg: &config.Config{
			ReleaseSource: &config.ReleaseSource{
				URL: "https://example.com/releases/app",
			},
		},
		cacheDir: dir,
	}

	// No cache yet
	if got := w.GetPublishedVersion(); got != "" {
		t.Fatalf("expected empty version before any publish, got %q", got)
	}

	// Simulate FetchLatestRelease setting pendingCache (version extractor mode)
	w.pendingCache = &webCache{
		Version:                       "2.1.0",
		AssetURL:                      "https://example.com/releases/app-2.1.0.apk",
		LatestPublishedReleaseVersion: "2.1.0",
	}

	// CommitCache should write to disk and clear pendingCache
	if err := w.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error: %v", err)
	}
	if w.pendingCache != nil {
		t.Fatal("expected pendingCache to be nil after CommitCache")
	}

	// GetPublishedVersion should read the written version
	if got := w.GetPublishedVersion(); got != "2.1.0" {
		t.Fatalf("GetPublishedVersion() = %q, want %q", got, "2.1.0")
	}

	// Commit with a new version
	w.pendingCache = &webCache{
		Version:                       "2.2.0",
		AssetURL:                      "https://example.com/releases/app-2.2.0.apk",
		LatestPublishedReleaseVersion: "2.2.0",
	}
	if err := w.CommitCache(); err != nil {
		t.Fatalf("CommitCache() error on second publish: %v", err)
	}
	if got := w.GetPublishedVersion(); got != "2.2.0" {
		t.Fatalf("GetPublishedVersion() after update = %q, want %q", got, "2.2.0")
	}

	// ClearCache should delete the file
	if err := w.ClearCache(); err != nil {
		t.Fatalf("ClearCache() error: %v", err)
	}
	if got := w.GetPublishedVersion(); got != "" {
		t.Fatalf("expected empty version after ClearCache, got %q", got)
	}

	// CommitCache with nil pendingCache is a no-op
	if err := w.CommitCache(); err != nil {
		t.Fatalf("CommitCache() with nil pendingCache should not error: %v", err)
	}
}

func TestWebDirectURLKeepsOriginalDownloadURL(t *testing.T) {
	// Simulates telegram.org-style redirect: stable entry URL → tokenized CDN URL.
	mux := http.NewServeMux()
	mux.HandleFunc("/dl/android/apk", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/cdn/Telegram.apk?token=ephemeral", http.StatusFound)
	})
	mux.HandleFunc("/cdn/Telegram.apk", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("data"))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	entryURL := srv.URL + "/dl/android/apk"
	w := &Web{
		cfg: &config.Config{
			ReleaseSource: &config.ReleaseSource{
				IsWebSource: true,
				AssetURL:    entryURL,
			},
		},
		client:    newSecureHTTPClient(5 * time.Second),
		cacheDir:  t.TempDir(),
		SkipCache: true,
	}

	rel, err := w.FetchLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("FetchLatestRelease() error = %v", err)
	}
	if len(rel.Assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(rel.Assets))
	}
	asset := rel.Assets[0]
	if asset.URL != entryURL {
		t.Errorf("asset.URL = %q, want original entry URL %q", asset.URL, entryURL)
	}
	if asset.Name != "Telegram.apk" {
		t.Errorf("asset.Name = %q, want Telegram.apk from redirect target", asset.Name)
	}
	if strings.Contains(asset.URL, "token=") {
		t.Errorf("download URL should not be the tokenized CDN URL: %s", asset.URL)
	}
}
