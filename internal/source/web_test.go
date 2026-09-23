package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zapstore/zsp/internal/config"
)

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
		client: newSecureHTTPClient(5 * time.Second),
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

func TestWebDirectURLFallsBackToGETWhenHEADIsNotAllowed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		http.Redirect(w, r, "/files/app.apk", http.StatusFound)
	})
	mux.HandleFunc("/files/app.apk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	w := &Web{client: newSecureHTTPClient(5 * time.Second)}
	probed, err := w.resolveRedirects(context.Background(), srv.URL+"/download")
	if err != nil {
		t.Fatalf("resolveRedirects() error = %v", err)
	}
	if probed.FinalURL != srv.URL+"/files/app.apk" {
		t.Fatalf("resolveRedirects() = %q, want redirected URL", probed.FinalURL)
	}
}

func TestWebDirectURLKeepsAPKWhenPathIsNotAPK(t *testing.T) {
	const apkType = "application/vnd.android.package-archive"
	const apkSize int64 = 10956212
	mux := http.NewServeMux()
	// HEAD is 200 with the APK media type. A ranged GET 302s to a .bin object,
	// the same shape as a CDN such as r2a.primal.net.
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", apkType)
			w.Header().Set("Content-Length", "10956212")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/r2/6f1c0a.bin", http.StatusFound)
	})
	mux.HandleFunc("/r2/6f1c0a.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", apkType)
		w.Header().Set("Content-Length", "10956212")
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	entryURL := srv.URL + "/download"
	web := &Web{
		cfg: &config.Config{
			ReleaseSource: &config.ReleaseSource{
				IsWebSource: true,
				AssetURL:    entryURL,
			},
		},
		client:   newSecureHTTPClient(5 * time.Second),
		cacheDir: t.TempDir(),
	}

	rel, err := web.FetchLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("FetchLatestRelease() error = %v", err)
	}
	if len(rel.Assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(rel.Assets))
	}
	asset := rel.Assets[0]
	if asset.URL != entryURL {
		t.Errorf("asset.URL = %q, want original entry URL", asset.URL)
	}
	if !asset.IsAPK() {
		t.Fatal("asset dropped as non-APK despite Android package content type")
	}
	if asset.Name != "download.apk" {
		t.Errorf("asset.Name = %q, want download.apk", asset.Name)
	}
	if asset.ContentType != apkType {
		t.Errorf("ContentType = %q, want %q", asset.ContentType, apkType)
	}
	if asset.Size != apkSize {
		t.Errorf("Size = %d, want %d", asset.Size, apkSize)
	}
}

func TestWebDirectURLRenamesBinRedirectTarget(t *testing.T) {
	const apkType = "application/vnd.android.package-archive"
	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/r2/6f1c0a.bin", http.StatusFound)
	})
	mux.HandleFunc("/r2/6f1c0a.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", apkType)
		w.Header().Set("Content-Length", "10956212")
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	entryURL := srv.URL + "/download"
	web := &Web{
		cfg: &config.Config{
			ReleaseSource: &config.ReleaseSource{
				IsWebSource: true,
				AssetURL:    entryURL,
			},
		},
		client:   newSecureHTTPClient(5 * time.Second),
		cacheDir: t.TempDir(),
	}

	rel, err := web.FetchLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("FetchLatestRelease() error = %v", err)
	}
	if len(rel.Assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(rel.Assets))
	}
	asset := rel.Assets[0]
	if asset.URL != entryURL {
		t.Errorf("asset.URL = %q, want original entry URL", asset.URL)
	}
	if asset.Name != "6f1c0a.apk" {
		t.Errorf("asset.Name = %q, want 6f1c0a.apk", asset.Name)
	}
	if !asset.IsAPK() {
		t.Fatal("bin redirect target was not kept as an APK")
	}
}

// TestExtractAssetURLRejectsInsecureExtractedURL confirms a dynamically
// extracted asset URL (e.g. from a JSON API response) is held to the same
// HTTPS-outside-loopback rule as an explicit configuration URL.
func TestExtractAssetURLRejectsInsecureExtractedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"http://evil.example.com/app.apk"}`))
	}))
	t.Cleanup(srv.Close)

	w := &Web{client: newSecureHTTPClient(5 * time.Second)}
	repo := &config.ReleaseSource{
		IsWebSource: true,
		Asset:       &config.VersionExtractor{URL: srv.URL, Path: "$.url"},
	}

	if _, err := w.extractAssetURL(context.Background(), repo); err == nil {
		t.Fatal("extractAssetURL() error = nil, want rejection of insecure extracted URL")
	}
}

// TestExtractAssetURLAcceptsSafeExtractedURL confirms a safe (HTTPS) extracted
// asset URL still passes through unchanged.
func TestExtractAssetURLAcceptsSafeExtractedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://cdn.example.com/app.apk"}`))
	}))
	t.Cleanup(srv.Close)

	w := &Web{client: newSecureHTTPClient(5 * time.Second)}
	repo := &config.ReleaseSource{
		IsWebSource: true,
		Asset:       &config.VersionExtractor{URL: srv.URL, Path: "$.url"},
	}

	got, err := w.extractAssetURL(context.Background(), repo)
	if err != nil {
		t.Fatalf("extractAssetURL() error = %v", err)
	}
	if got != "https://cdn.example.com/app.apk" {
		t.Fatalf("extractAssetURL() = %q, want %q", got, "https://cdn.example.com/app.apk")
	}
}

func TestWebDirectURLReturnsErrNotModifiedForUnchangedETag(t *testing.T) {
	const etag = `"abc123"`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("If-None-Match") == etag {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		writer.Header().Set("ETag", etag)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	web := &Web{
		cfg: &config.Config{
			ReleaseSource: &config.ReleaseSource{
				IsWebSource: true,
				AssetURL:    server.URL + "/app.apk",
			},
		},
		client:   newSecureHTTPClient(5 * time.Second),
		cacheDir: t.TempDir(),
	}

	if _, err := web.FetchLatestRelease(t.Context()); err != nil {
		t.Fatalf("first FetchLatestRelease() = %v", err)
	}
	if err := web.CommitCache(); err != nil {
		t.Fatal(err)
	}

	if _, err := web.FetchLatestRelease(t.Context()); !errors.Is(err, ErrNotModified) {
		t.Fatalf("second FetchLatestRelease() = %v, want ErrNotModified", err)
	}

	if err := web.ClearHTTPCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := web.FetchLatestRelease(t.Context()); err != nil {
		t.Fatalf("FetchLatestRelease after ClearHTTPCache() = %v", err)
	}

	web.SkipHTTPCache = true
	release, err := web.FetchLatestRelease(t.Context())
	if err != nil {
		t.Fatalf("SkipHTTPCache FetchLatestRelease() = %v", err)
	}
	if len(release.Assets) != 1 || release.Assets[0].URL != server.URL+"/app.apk" {
		t.Fatalf("SkipHTTPCache assets = %+v", release.Assets)
	}
}
