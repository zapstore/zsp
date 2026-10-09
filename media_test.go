package zsp

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zapstore/zsp/internal/apk"
)

func TestPrepareMediaUsesAPKIcon(t *testing.T) {
	pngBytes := tinyPNG(t, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "icon") {
			t.Errorf("downloaded icon %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(server.Close)

	plan, warnings, err := prepareMedia(t.Context(), PublishConfig{
		iconCandidates: []string{server.URL + "/icon.png"},
		imageSets:      [][]string{{server.URL + "/1.png"}},
	}, &apk.APKInfo{Icon: pngBytes}, "https://cdn.example", true)
	if err != nil {
		t.Fatalf("prepareMedia() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if plan.iconURL == "" || len(plan.imageURLs) != 1 {
		t.Fatalf("icon %q images %v", plan.iconURL, plan.imageURLs)
	}
	if !strings.Contains(plan.iconURL, "https://cdn.example/") {
		t.Fatalf("icon URL = %q", plan.iconURL)
	}
}

func TestPrepareMediaFallsBackWhenAPKIconIsMissing(t *testing.T) {
	pngBytes := tinyPNG(t, 8)
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		if strings.HasPrefix(r.URL.Path, "/bad/") {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("../../assets/icon.png"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(server.Close)

	plan, warnings, err := prepareMedia(t.Context(), PublishConfig{
		iconCandidates: []string{server.URL + "/bad/icon.png", server.URL + "/ok/icon.png"},
	}, &apk.APKInfo{}, "https://cdn.example", true)
	if err != nil {
		t.Fatalf("prepareMedia() error = %v", err)
	}
	if plan.iconURL == "" {
		t.Fatal("icon URL is empty")
	}
	if got != "/ok/icon.png" {
		t.Fatalf("last icon request = %q", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown format") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestPrepareMediaFallsBackWhenAPKIconIsNotAnImage(t *testing.T) {
	pngBytes := tinyPNG(t, 8)
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(server.Close)

	plan, warnings, err := prepareMedia(t.Context(), PublishConfig{
		iconCandidates: []string{server.URL + "/icon.png"},
	}, &apk.APKInfo{Icon: []byte("not an image")}, "https://cdn.example", true)
	if err != nil {
		t.Fatalf("prepareMedia() error = %v", err)
	}
	if !requested || plan.iconURL == "" {
		t.Fatalf("requested %v icon %q", requested, plan.iconURL)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "APK icon") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestPrepareMediaSkipsMissingScreenshot(t *testing.T) {
	pngBytes := tinyPNG(t, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/2.png") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(server.Close)

	plan, warnings, err := prepareMedia(t.Context(), PublishConfig{
		imageSets: [][]string{{server.URL + "/1.png", server.URL + "/2.png", server.URL + "/3.png"}},
	}, &apk.APKInfo{Icon: pngBytes}, "https://cdn.example", true)
	if err != nil {
		t.Fatalf("prepareMedia() error = %v", err)
	}
	if len(plan.imageURLs) != 2 {
		t.Fatalf("images = %v, want the two that exist", plan.imageURLs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "image 2") || !strings.Contains(warnings[0], "status 404") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestPrepareMediaFallsBackWhenScreenshotsAreNotImages(t *testing.T) {
	pngBytes := tinyPNG(t, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/fastlane/") {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("../../assets/icon.png"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(server.Close)

	plan, warnings, err := prepareMedia(t.Context(), PublishConfig{
		imageSets: [][]string{
			{server.URL + "/fastlane/1.png"},
			{server.URL + "/fdroid/1.png"},
		},
	}, &apk.APKInfo{Icon: pngBytes}, "https://cdn.example", true)
	if err != nil {
		t.Fatalf("prepareMedia() error = %v", err)
	}
	if len(plan.imageURLs) != 1 || !strings.Contains(plan.imageURLs[0], ".png") {
		t.Fatalf("images = %v", plan.imageURLs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown format") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestPrepareMediaSkipsSlowScreenshot(t *testing.T) {
	previous := mediaFetchTimeout
	mediaFetchTimeout = 30 * time.Millisecond
	t.Cleanup(func() { mediaFetchTimeout = previous })

	pngBytes := tinyPNG(t, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/slow.png") {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(server.Close)

	plan, warnings, err := prepareMedia(t.Context(), PublishConfig{
		imageSets: [][]string{{server.URL + "/slow.png", server.URL + "/ok.png"}},
	}, nil, "https://cdn.example", true)
	if err != nil {
		t.Fatalf("prepareMedia() error = %v", err)
	}
	if len(plan.imageURLs) != 1 {
		t.Fatalf("images = %v", plan.imageURLs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "image 1") || !strings.Contains(warnings[0], "timeout") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestPrepareMediaLocalScreenshotMustExist(t *testing.T) {
	_, _, err := prepareMedia(t.Context(), PublishConfig{
		Images: []string{"/no/such/screenshot.png"},
	}, nil, "https://cdn.example", true)
	if err == nil {
		t.Fatal("prepareMedia() error = nil, want a missing local file")
	}
}

func tinyPNG(t *testing.T, width int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, width, width))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
