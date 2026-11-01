package release

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zapstore/zsp/v2/internal/apk"
)

// forgeWithAPKs starts a fake Gitea forge for owner/repo whose newest release is
// a beta and whose older one is stable, both carrying the corpus APK sample.apk.
// APK downloads honour byte ranges. It returns the forge's FetchConfig.
func forgeWithAPKs(t *testing.T) (FetchConfig, string) {
	t.Helper()
	apkPath := filepath.Join("..", "..", "testdata", "apks", "sample.apk")
	if _, err := os.Stat(apkPath); err != nil {
		t.Skipf("no test APK: %v", err)
	}

	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/owner/repo/releases", func(w http.ResponseWriter, r *http.Request) {
		size := mustSize(t, apkPath)
		_, _ = fmt.Fprintf(w, `[
			{"tag_name":"v2.0.0-beta.1","name":"Beta","body":"%s","prerelease":true,"published_at":"2024-05-02T00:00:00Z","assets":[
				{"name":"app-arm64-v8a.apk","size":%d,"browser_download_url":"%s/dl/sample.apk"},
				{"name":"app.dmg","size":9,"browser_download_url":"%s/dl/app.dmg"}]},
			{"tag_name":"v1.0.0","name":"Stable","body":"first","published_at":"2024-05-01T00:00:00Z","assets":[
				{"name":"app.apk","size":%d,"browser_download_url":"%s/dl/sample.apk"}]},
			{"tag_name":"v0.9.0","draft":true,"assets":[]}]`,
			strings.Repeat("n", 20_000), size, server.URL, server.URL, size, server.URL)
	})
	mux.HandleFunc("/dl/sample.apk", func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(apkPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		http.ServeContent(w, r, "sample.apk", time.Time{}, f)
	})
	mux.HandleFunc("/dl/not-an-apk.apk", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x.apk", time.Time{}, strings.NewReader(strings.Repeat("not a zip ", 1000)))
	})
	mux.HandleFunc("/dl/no-ranges.apk", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100_000)))
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return FetchConfig{
		ReleaseSource: &ReleaseSource{URL: server.URL + "/owner/repo", Type: "gitea"},
		// a filter that matches no release must not hide any from the list
		ReleaseFilter: `^never-matches$`,
	}, server.URL
}

func mustSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func TestListReleases(t *testing.T) {
	config, base := forgeWithAPKs(t)

	releases, err := ListReleases(t.Context(), config, ListOptions{})
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("releases = %d, want 2: the draft is left out and the filter does not apply", len(releases))
	}

	beta, stable := releases[0], releases[1]
	if beta.Tag != "v2.0.0-beta.1" || beta.Title != "Beta" || !beta.Prerelease || stable.Prerelease {
		t.Errorf("releases = %+v, %+v", beta, stable)
	}
	if beta.Date.IsZero() || !beta.Date.After(stable.Date) {
		t.Errorf("dates = %v, %v, want the beta newer", beta.Date, stable.Date)
	}
	if len(beta.Assets) != 1 || beta.Assets[0].Name != "app-arm64-v8a.apk" || beta.Assets[0].URL != base+"/dl/sample.apk" || beta.Assets[0].Size == 0 {
		t.Errorf("beta assets = %+v, want only the APK", beta.Assets)
	}
	if len(beta.Notes) != maxReleaseNotesBytes {
		t.Errorf("notes are %d bytes, want them cut to %d", len(beta.Notes), maxReleaseNotesBytes)
	}
}

func TestListReleasesNeedsAForge(t *testing.T) {
	config := FetchConfig{ReleaseSource: &ReleaseSource{URL: "https://f-droid.org/packages/org.acme.app", Type: "fdroid"}}
	if _, err := ListReleases(t.Context(), config, ListOptions{}); !errors.Is(err, ErrListUnsupported) {
		t.Fatalf("ListReleases of an F-Droid source = %v, want ErrListUnsupported", err)
	}
}

func TestListReleasesRejectsABadConfig(t *testing.T) {
	if _, err := ListReleases(t.Context(), FetchConfig{}, ListOptions{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("ListReleases of an empty config = %v, want ErrInvalidConfig", err)
	}
	config, _ := forgeWithAPKs(t)
	if _, err := ListReleases(t.Context(), config, ListOptions{MaxReleases: 1000}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("ListReleases(1000) = %v, want ErrInvalidConfig", err)
	}
}

// TestProbeAPKMatchesTheDownloadedAPK lists a forge, probes the APK it names and
// checks the claim against what a full download parses to.
func TestProbeAPKMatchesTheDownloadedAPK(t *testing.T) {
	config, _ := forgeWithAPKs(t)
	releases, err := ListReleases(t.Context(), config, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}

	identity, err := ProbeAPK(t.Context(), releases[0].Assets[0], ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeAPK: %v", err)
	}
	parsed, err := apk.Parse(t.Context(), filepath.Join("..", "..", "testdata", "apks", "sample.apk"))
	if err != nil {
		t.Fatal(err)
	}
	if identity.AppID != parsed.PackageID || identity.VersionCode != parsed.VersionCode || identity.CertificateHash != parsed.CertFingerprint {
		t.Fatalf("identity = %+v, Parse = %s %d %s", identity, parsed.PackageID, parsed.VersionCode, parsed.CertFingerprint)
	}
	if identity.BytesRead <= 0 || identity.BytesRead*10 > parsed.FileSize {
		t.Errorf("read %d of %d bytes, want a small part of the file", identity.BytesRead, parsed.FileSize)
	}
}

func TestProbeAPKFailures(t *testing.T) {
	_, base := forgeWithAPKs(t)

	tests := []struct {
		name  string
		asset ListedAsset
		want  error
	}{
		{"no ranges", ListedAsset{Name: "a.apk", URL: base + "/dl/no-ranges.apk", Size: 100_000}, ErrRangeUnsupported},
		{"no ranges, size unknown", ListedAsset{Name: "a.apk", URL: base + "/dl/no-ranges.apk"}, ErrRangeUnsupported},
		{"not an APK", ListedAsset{Name: "a.apk", URL: base + "/dl/not-an-apk.apk", Size: 10_000}, ErrInvalidAPK},
		{"no URL", ListedAsset{Name: "a.apk"}, ErrInvalidConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ProbeAPK(t.Context(), tt.asset, ProbeOptions{})
			if !errors.Is(err, tt.want) {
				t.Fatalf("ProbeAPK = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestProbeAPKStopsAtItsBudget(t *testing.T) {
	_, base := forgeWithAPKs(t)
	asset := ListedAsset{Name: "a.apk", URL: base + "/dl/sample.apk", Size: mustSize(t, filepath.Join("..", "..", "testdata", "apks", "sample.apk"))}

	_, err := ProbeAPK(t.Context(), asset, ProbeOptions{MaxBytes: 1024})
	if err == nil {
		t.Fatal("ProbeAPK read an APK within a 1 KiB budget")
	}
	if errors.Is(err, ErrRangeUnsupported) {
		t.Errorf("a spent budget is not a missing range feature: %v", err)
	}
}

func TestProbeAPKHonoursCancellation(t *testing.T) {
	_, base := forgeWithAPKs(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	asset := ListedAsset{Name: "a.apk", URL: base + "/dl/sample.apk", Size: 6_760_308}
	if _, err := ProbeAPK(ctx, asset, ProbeOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProbeAPK on a cancelled context = %v, want context.Canceled", err)
	}
}
