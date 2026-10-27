package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zapstore/zsp/v2/internal/config"
)

// TestFDroidDownloadUsesSharedPipeline confirms F-Droid's Download method
// routes through the shared DownloadHTTP pipeline (size limit, stall
// detection, bounded retries) with no authentication, matching F-Droid
// repositories' unauthenticated access model.
func TestFDroidDownloadUsesSharedPipeline(t *testing.T) {
	var gotAuth string
	payload := []byte("apk-bytes-ok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	f := &FDroid{}
	asset := &Asset{Name: "app.apk", URL: srv.URL + "/app.apk"}

	path, err := f.Download(context.Background(), asset, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization header = %q, want none", gotAuth)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %q, want %q", got, payload)
	}
}

// TestFDroidDownloadRejectsInsecureURL confirms F-Droid asset downloads
// inherit the shared pipeline's HTTPS-outside-loopback validation.
func TestFDroidDownloadRejectsInsecureURL(t *testing.T) {
	f := &FDroid{}
	asset := &Asset{Name: "app.apk", URL: "http://evil.example.com/app.apk"}

	if _, err := f.Download(context.Background(), asset, t.TempDir(), nil); err == nil {
		t.Fatal("Download() error = nil, want rejection of insecure URL")
	}
}

func fdroidIndexJSON(versionCode int, hash string) string {
	return fmt.Sprintf(`{"packages":{
		"com.example.app":[{"versionCode":%d,"versionName":"1.%d","apkName":"app.apk","hash":%q,"hashType":"sha256","size":12,"nativecode":["arm64-v8a"]}],
		"com.example.other":[{"versionCode":1,"versionName":"1.0","apkName":"other.apk","size":1}]
	}}`, versionCode, versionCode, hash)
}

func newTestFDroid(t *testing.T, serverURL string) *FDroid {
	t.Helper()
	return &FDroid{
		repoInfo: &config.FDroidRepoInfo{
			RepoURL:   serverURL + "/repo",
			IndexURL:  serverURL + "/repo/index-v1.json",
			PackageID: "com.example.app",
		},
		client:   newDownloadHTTPClient(nil),
		cacheDir: t.TempDir(),
	}
}

func TestFDroidReleaseCarriesPublishedSHA256(t *testing.T) {
	hexSum := strings.Repeat("ab", 32)
	tests := []struct {
		name     string
		hash     string
		hashType string
		want     string
	}{
		{"sha256", hexSum, "sha256", hexSum},
		{"missing hash type defaults to sha256", hexSum, "", hexSum},
		{"other hash type is ignored", hexSum, "md5", ""},
		{"malformed hash is ignored", "not-hex", "sha256", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newTestFDroid(t, "https://example.com")
			release := f.buildRelease(&fdroidPackageVersion{VersionName: "1.0", ApkName: "app.apk", Hash: tt.hash, HashType: tt.hashType})
			if got := release.Assets[0].SHA256; got != tt.want {
				t.Errorf("Asset.SHA256 = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFDroidDownloadVerifiesIndexHash(t *testing.T) {
	payload := []byte("apk-bytes-ok")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	f := &FDroid{client: newDownloadHTTPClient(nil)}
	asset := &Asset{Name: "app.apk", URL: srv.URL + "/app.apk", SHA256: hex.EncodeToString(sum[:])}
	if _, err := f.Download(context.Background(), asset, t.TempDir(), nil); err != nil {
		t.Fatalf("Download() with matching hash error = %v", err)
	}

	asset.SHA256 = strings.Repeat("0", 64)
	if _, err := f.Download(context.Background(), asset, t.TempDir(), nil); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Download() with wrong hash error = %v, want ErrChecksumMismatch", err)
	}
}

func TestFDroidConditionalIndexFetch(t *testing.T) {
	const etag = `"index-1"`
	versionCode := 1
	var conditional int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			conditional++
		}
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte(fdroidIndexJSON(versionCode, strings.Repeat("ab", 32))))
	}))
	defer srv.Close()
	f := newTestFDroid(t, srv.URL)

	release, err := f.FetchLatestRelease(t.Context())
	if err != nil {
		t.Fatalf("first FetchLatestRelease() = %v", err)
	}
	if release.Version != "1.1" || release.Assets[0].SHA256 == "" {
		t.Fatalf("release = %+v", release)
	}
	if err := f.CommitCache(); err != nil {
		t.Fatal(err)
	}

	if _, err := f.FetchLatestRelease(t.Context()); !errors.Is(err, ErrNotModified) {
		t.Fatalf("second FetchLatestRelease() = %v, want ErrNotModified", err)
	}
	if conditional != 1 {
		t.Errorf("conditional requests = %d, want 1", conditional)
	}

	f.SkipHTTPCache = true
	if _, err := f.FetchLatestRelease(t.Context()); err != nil {
		t.Fatalf("SkipHTTPCache FetchLatestRelease() = %v", err)
	}
	f.SkipHTTPCache = false

	if err := f.ClearHTTPCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.FetchLatestRelease(t.Context()); err != nil {
		t.Fatalf("FetchLatestRelease after ClearHTTPCache() = %v", err)
	}
}

// TestFDroidIndexChangeForOtherPackageIsNotNew confirms an index that changed
// for a different package, leaving this package's build alone, is not a new
// release.
func TestFDroidIndexChangeForOtherPackageIsNotNew(t *testing.T) {
	etag := `"v1"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte(fdroidIndexJSON(1, strings.Repeat("ab", 32))))
	}))
	defer srv.Close()
	f := newTestFDroid(t, srv.URL)

	if _, err := f.FetchLatestRelease(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.CommitCache(); err != nil {
		t.Fatal(err)
	}
	etag = `"v2"` // the index changed, but not this package's entry
	if _, err := f.FetchLatestRelease(t.Context()); !errors.Is(err, ErrNotModified) {
		t.Fatalf("FetchLatestRelease() = %v, want ErrNotModified", err)
	}
}

func TestFDroidIndexSizeAndStatusErrors(t *testing.T) {
	t.Run("oversized Content-Length", func(t *testing.T) {
		f := newTestFDroid(t, "https://example.com")
		f.client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			resp := testResponse(http.StatusOK, "{}")
			resp.ContentLength = MaxFDroidIndexSize + 1
			return resp, nil
		})}
		if _, err := f.FetchLatestRelease(t.Context()); !errors.Is(err, errTooLarge) {
			t.Fatalf("FetchLatestRelease() = %v, want errTooLarge", err)
		}
	})
	t.Run("server error is typed", func(t *testing.T) {
		f := newTestFDroid(t, "https://example.com")
		f.client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return testResponse(http.StatusBadGateway, ""), nil
		})}
		_, err := f.FetchLatestRelease(t.Context())
		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadGateway {
			t.Fatalf("FetchLatestRelease() = %v, want 502 *HTTPStatusError", err)
		}
	})
}
