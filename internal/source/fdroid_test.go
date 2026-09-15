package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
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
