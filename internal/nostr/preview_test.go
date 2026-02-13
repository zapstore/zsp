package nostr

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

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
