package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestGiteaDownloadSendsTokenHeader confirms Gitea's Download method routes
// through the shared DownloadHTTP pipeline while still attaching the
// configured token as a Gitea-style Authorization header.
func TestGiteaDownloadSendsTokenHeader(t *testing.T) {
	var gotAuth string
	payload := []byte("apk-bytes-ok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	g := &Gitea{token: "test-token"}
	asset := &Asset{Name: "app.apk", URL: srv.URL + "/app.apk"}

	path, err := g.Download(context.Background(), asset, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if gotAuth != "token test-token" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "token test-token")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %q, want %q", got, payload)
	}
}

// TestGiteaDownloadRejectsInsecureURL confirms Gitea asset downloads inherit
// the shared pipeline's HTTPS-outside-loopback validation.
func TestGiteaDownloadRejectsInsecureURL(t *testing.T) {
	g := &Gitea{}
	asset := &Asset{Name: "app.apk", URL: "http://evil.example.com/app.apk"}

	if _, err := g.Download(context.Background(), asset, t.TempDir(), nil); err == nil {
		t.Fatal("Download() error = nil, want rejection of insecure URL")
	}
}
