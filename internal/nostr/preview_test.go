package nostr

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/config"
)

func TestBuildPreviewDataUsesFinalChannelAndPublicFilename(t *testing.T) {
	data := BuildPreviewData(
		&apk.APKInfo{FilePath: "/private/tmp/zsp-fetch-123/app.apk"},
		&config.Config{},
		&EventSet{Release: &gonostr.Event{Tags: gonostr.Tags{{"c", "nightly"}}}},
		"",
		"https://cdn.example.com",
		[]string{"wss://relay.example.com"},
	)
	if data.Channel != "nightly" {
		t.Fatalf("Channel = %q, want nightly", data.Channel)
	}
	if len(data.Assets) != 1 || data.Assets[0].Filename != "app.apk" {
		t.Fatalf("Assets = %+v, want public basename", data.Assets)
	}
}

func TestPreviewEventsIncludesReleaseWhenApplicationIsSkipped(t *testing.T) {
	release := &gonostr.Event{Kind: KindRelease, ID: "release-id"}
	server := NewPreviewServer(&PreviewData{
		ReleaseEvent:        release,
		SoftwareAssetEvents: []*gonostr.Event{{Kind: KindSoftwareAsset, ID: "asset-id"}},
	}, "", "", 0)
	recorder := httptest.NewRecorder()
	server.handleEvents(recorder, httptest.NewRequest(http.MethodGet, "/api/events", nil))

	body := recorder.Body.String()
	if !strings.Contains(body, "softwareRelease") || !strings.Contains(body, "release-id") ||
		!strings.Contains(body, "softwareAssets") || !strings.Contains(body, "asset-id") {
		t.Fatalf("skip-app event response omitted release data: %s", body)
	}
}

func TestPreviewServerServesLocalScreenshots(t *testing.T) {
	// Read the test screenshot fixture
	screenshotData, err := os.ReadFile("../../testdata/fixtures/screenshot.png")
	if err != nil {
		t.Fatalf("failed to read test screenshot: %v", err)
	}

	previewData := &PreviewData{
		AppName:   "Test App",
		PackageID: "com.example.test",
		ImageData: []PreviewImageData{
			{
				Data:     screenshotData,
				MimeType: "image/png",
			},
		},
	}

	server := NewPreviewServer(previewData, "", "", 17018)

	url, err := server.Start()
	if err != nil {
		t.Fatalf("failed to start preview server: %v", err)
	}
	defer server.Close()

	// Fetch the screenshot via the /images/0 endpoint
	resp, err := http.Get(fmt.Sprintf("%simages/0", url))
	if err != nil {
		t.Fatalf("failed to fetch screenshot: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("expected Content-Type image/png, got %s", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	if len(body) != len(screenshotData) {
		t.Errorf("expected %d bytes, got %d", len(screenshotData), len(body))
	}

	// Verify the image is referenced in the HTML
	htmlResp, err := http.Get(url)
	if err != nil {
		t.Fatalf("failed to fetch preview HTML: %v", err)
	}
	defer htmlResp.Body.Close()

	htmlBody, _ := io.ReadAll(htmlResp.Body)
	html := string(htmlBody)

	if !strings.Contains(html, `/images/0`) {
		t.Error("expected HTML to contain /images/0 reference for local screenshot")
	}

	// Verify out-of-bounds index returns 404
	resp404, err := http.Get(fmt.Sprintf("%simages/1", url))
	if err != nil {
		t.Fatalf("failed to fetch out-of-bounds image: %v", err)
	}
	defer resp404.Body.Close()

	if resp404.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404 for out-of-bounds index, got %d", resp404.StatusCode)
	}
}

func TestPreviewServerNoScreenshots(t *testing.T) {
	previewData := &PreviewData{
		AppName:   "Test App",
		PackageID: "com.example.test",
	}

	server := NewPreviewServer(previewData, "", "", 17019)
	url, err := server.Start()
	if err != nil {
		t.Fatalf("failed to start preview server: %v", err)
	}
	defer server.Close()

	// Should return 404 when no images
	resp, err := http.Get(fmt.Sprintf("%simages/0", url))
	if err != nil {
		t.Fatalf("failed to fetch image: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}
}

func TestPreviewDecisionRequiresSameOriginAndSessionNonce(t *testing.T) {
	server := NewPreviewServer(&PreviewData{AppName: "Test App"}, "", "", 17020)
	url, err := server.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, url+"api/approve", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("X-Session-Nonce", server.sessionNonce)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin approval status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}

	request, err = http.NewRequest(http.MethodPost, url+"api/approve", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "http://localhost:17020")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("nonce-less approval status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}

	request, err = http.NewRequest(http.MethodPost, url+"api/approve", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "http://localhost:17020")
	request.Header.Set("X-Session-Nonce", server.sessionNonce)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("authenticated approval status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}

	approved, err := server.WaitDecision(t.Context())
	if err != nil || !approved {
		t.Fatalf("WaitDecision() = (%v, %v), want (true, nil)", approved, err)
	}
}
