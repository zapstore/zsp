package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

func TestDecodeJSONResponseRejectsOversizedBody(t *testing.T) {
	resp := &http.Response{
		Body:          io.NopCloser(bytes.NewReader([]byte(`{"value":"too long"}`))),
		ContentLength: -1,
	}
	var value struct {
		Value string `json:"value"`
	}
	if err := decodeJSONResponse(resp, 8, &value); err == nil {
		t.Fatal("decodeJSONResponse() error = nil, want size-limit rejection")
	}
}

func TestDoWithTorFallback(t *testing.T) {
	tests := []struct {
		name         string
		directStatus int
		torStatus    int
		torClientErr error
		wantErr      string
		wantTorCalls int
		wantTorAuth  string
	}{
		{name: "successful direct request does not use Tor", directStatus: http.StatusOK},
		{
			name: "forbidden request retries unauthenticated through Tor", directStatus: http.StatusForbidden,
			torStatus: http.StatusOK, wantTorCalls: 1,
		},
		{name: "non-forbidden response does not use Tor", directStatus: http.StatusInternalServerError},
		{
			name: "unavailable Tor reports actionable error", directStatus: http.StatusForbidden,
			torClientErr: errors.New("connection refused"),
			wantErr:      "start Tor with SOCKS5 on 127.0.0.1:9050",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			torCalls := 0
			var torAuthorization string
			restoreTor := SetTorHTTPClientForTest(func() (*http.Client, error) {
				if tt.torClientErr != nil {
					return nil, tt.torClientErr
				}
				return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					torCalls++
					torAuthorization = req.Header.Get("Authorization")
					return &http.Response{
						StatusCode: tt.torStatus,
						Body:       io.NopCloser(strings.NewReader("ok")),
						Header:     make(http.Header),
						Request:    req,
					}, nil
				})}, nil
			})
			t.Cleanup(restoreTor)

			directClient := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tt.directStatus,
					Body:       io.NopCloser(strings.NewReader("forbidden")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			})}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/app.apk", nil)
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer secret")

			resp, err := DoWithTorFallback(context.Background(), directClient, req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("DoWithTorFallback() error = %v, want substring %q", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("DoWithTorFallback() error = %v", err)
				}
				resp.Body.Close()
			}
			if torCalls != tt.wantTorCalls {
				t.Errorf("Tor request count = %d, want %d", torCalls, tt.wantTorCalls)
			}
			if torAuthorization != tt.wantTorAuth {
				t.Errorf("Tor Authorization = %q, want %q", torAuthorization, tt.wantTorAuth)
			}
		})
	}
}

// TestNewSourceFromConfig tests source creation from various config types.
// These tests verify URL parsing and source factory logic, not network calls.
func TestNewSourceFromConfig(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		wantType   config.SourceType
		wantErr    bool
		errContain string
	}{
		{
			name: "local source",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{LocalPath: "../../testdata/apks/sample.apk"},
			},
			wantType: config.SourceLocal,
			wantErr:  false,
		},
		{
			name: "github source - mempal",
			cfg: &config.Config{
				Repository: "https://github.com/AeonBTC/mempal",
			},
			wantType: config.SourceGitHub,
			wantErr:  false,
		},
		{
			name: "github source - citrine",
			cfg: &config.Config{
				Repository: "https://github.com/greenart7c3/Citrine",
			},
			wantType: config.SourceGitHub,
			wantErr:  false,
		},
		{
			name: "gitlab source - aurora store",
			cfg: &config.Config{
				Repository: "https://gitlab.com/AuroraOSS/AuroraStore",
			},
			wantType: config.SourceGitLab,
			wantErr:  false,
		},
		{
			name: "gitea source (codeberg)",
			cfg: &config.Config{
				Repository: "https://codeberg.org/Freeyourgadget/Gadgetbridge",
			},
			wantType: config.SourceGitea,
			wantErr:  false,
		},
		{
			name: "fdroid source - antennapod",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{
					URL: "https://f-droid.org/packages/de.danoeh.antennapod",
				},
			},
			wantType: config.SourceFDroid,
			wantErr:  false,
		},
		{
			name: "izzyondroid fdroid source",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{
					URL: "https://apt.izzysoft.de/fdroid/index/apk/de.danoeh.antennapod",
				},
			},
			wantType: config.SourceFDroid,
			wantErr:  false,
		},
		{
			name: "web source with asset_url pattern",
			cfg: &config.Config{
				Repository: "https://github.com/AntennaPod/AntennaPod",
				ReleaseSource: &config.ReleaseSource{
					URL:         "https://f-droid.org/packages/de.danoeh.antennapod/",
					IsWebSource: true,
					AssetURL:    "https://f-droid\\.org/repo/de\\.danoeh\\.antennapod_[0-9]+\\.apk",
				},
			},
			wantType: config.SourceWeb,
			wantErr:  false,
		},
		{
			name: "unknown source type fails",
			cfg: &config.Config{
				Repository: "https://unknown-forge.example.com/user/repo",
			},
			wantType:   config.SourceUnknown,
			wantErr:    true,
			errContain: "unsupported source type",
		},
		{
			name: "self-hosted gitlab with explicit type",
			cfg: &config.Config{
				Repository: "https://git.mycompany.com/team/app",
				ReleaseSource: &config.ReleaseSource{
					URL:  "https://git.mycompany.com/team/app",
					Type: "gitlab",
				},
			},
			wantType: config.SourceGitLab,
			wantErr:  false,
		},
		{
			name: "self-hosted gitea with explicit type",
			cfg: &config.Config{
				Repository: "https://forge.example.org/user/app",
				ReleaseSource: &config.ReleaseSource{
					URL:  "https://forge.example.org/user/app",
					Type: "gitea",
				},
			},
			wantType: config.SourceGitea,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := New(tt.cfg)

			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				if tt.errContain != "" && err != nil {
					if !strings.Contains(err.Error(), tt.errContain) {
						t.Errorf("New() error = %v, want error containing %q", err, tt.errContain)
					}
				}
				return
			}

			if src == nil {
				t.Error("New() returned nil source")
				return
			}

			if src.Type() != tt.wantType {
				t.Errorf("New() source type = %v, want %v", src.Type(), tt.wantType)
			}
		})
	}
}

// TestNewWithOptions tests source creation with options
func TestNewWithOptions(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		opts     Options
		wantType config.SourceType
		wantErr  bool
	}{
		{
			name: "local with base dir",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{LocalPath: "../apks/sample.apk"},
			},
			opts: Options{
				BaseDir: "../../testdata",
			},
			wantType: config.SourceLocal,
			wantErr:  false,
		},
		{
			name: "github with include pre-releases",
			cfg: &config.Config{
				Repository: "https://github.com/AeonBTC/mempal",
			},
			opts: Options{
				IncludePreReleases: true,
			},
			wantType: config.SourceGitHub,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := NewWithOptions(tt.cfg, tt.opts)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewWithOptions() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			if src == nil {
				t.Error("NewWithOptions() returned nil source")
				return
			}

			if src.Type() != tt.wantType {
				t.Errorf("NewWithOptions() source type = %v, want %v", src.Type(), tt.wantType)
			}

			// For GitHub source, check that IncludePreReleases was applied
			if tt.opts.IncludePreReleases {
				if gh, ok := src.(*GitHub); ok {
					if !gh.IncludePreReleases {
						t.Error("NewWithOptions() GitHub IncludePreReleases not set")
					}
				}
			}
		})
	}
}

// TestProgressReader tests the progress reader wrapper
func TestProgressReader(t *testing.T) {
	data := []byte("hello world")
	total := int64(len(data))

	var lastDownloaded, lastTotal int64
	callCount := 0

	progress := func(downloaded, totalSize int64) {
		lastDownloaded = downloaded
		lastTotal = totalSize
		callCount++
	}

	reader := &ProgressReader{
		Reader:     &bytesReaderImpl{data: data},
		Total:      total,
		OnProgress: progress,
	}

	buf := make([]byte, 5)
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if n != 5 {
		t.Errorf("Read() n = %d, want 5", n)
	}

	if reader.Downloaded != 5 {
		t.Errorf("Downloaded = %d, want 5", reader.Downloaded)
	}

	if lastDownloaded != 5 {
		t.Errorf("progress callback downloaded = %d, want 5", lastDownloaded)
	}

	if lastTotal != total {
		t.Errorf("progress callback total = %d, want %d", lastTotal, total)
	}

	if callCount != 1 {
		t.Errorf("progress callback count = %d, want 1", callCount)
	}
}

// TestAssetFields tests Asset struct fields
func TestAssetFields(t *testing.T) {
	asset := &Asset{
		Name:        "app-v1.0.0-arm64.apk",
		URL:         "https://example.com/releases/app-v1.0.0-arm64.apk",
		Size:        1024000,
		LocalPath:   "/tmp/app-v1.0.0-arm64.apk",
		ContentType: "application/vnd.android.package-archive",
	}

	if asset.Name != "app-v1.0.0-arm64.apk" {
		t.Errorf("Asset.Name = %q, want %q", asset.Name, "app-v1.0.0-arm64.apk")
	}
	if asset.Size != 1024000 {
		t.Errorf("Asset.Size = %d, want %d", asset.Size, 1024000)
	}
}

// TestReleaseFields tests Release struct fields
func TestReleaseFields(t *testing.T) {
	release := &Release{
		Version:    "1.0.0",
		TagName:    "v1.0.0",
		Changelog:  "Bug fixes",
		PreRelease: false,
		Assets: []*Asset{
			{Name: "app.apk"},
		},
	}

	if release.Version != "1.0.0" {
		t.Errorf("Release.Version = %q, want %q", release.Version, "1.0.0")
	}
	if len(release.Assets) != 1 {
		t.Errorf("Release.Assets len = %d, want 1", len(release.Assets))
	}
}

func TestForgeConvertersPreserveReleaseName(t *testing.T) {
	tests := []struct {
		forge string
		got   *Release
		want  string
	}{
		{"github", (&GitHub{}).convertRelease(&githubRelease{TagName: "v1.0.0", Name: "GitHub release"}), "GitHub release"},
		{"gitlab", (&GitLab{}).convertRelease(&gitlabRelease{TagName: "v1.0.0", Name: "GitLab release"}), "GitLab release"},
		{"gitea", (&Gitea{}).convertRelease(&giteaRelease{TagName: "v1.0.0", Name: "Gitea release"}), "Gitea release"},
	}
	for _, test := range tests {
		if test.got.Name != test.want {
			t.Errorf("%s converter release name = %q, want %q", test.forge, test.got.Name, test.want)
		}
	}
}

// bytesReaderImpl implements io.Reader for testing
type bytesReaderImpl struct {
	data []byte
	pos  int
}

func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, nil
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func TestIsAPKContentTypeIgnoresParameters(t *testing.T) {
	if !IsAPKContentType("application/vnd.android.package-archive; charset=binary") {
		t.Fatal("expected Android package media type to match with parameters")
	}
	asset := &Asset{
		Name:        "6f1c0a.bin",
		URL:         "https://r2a.primal.net/blob/6f1c0a.bin",
		ContentType: "application/vnd.android.package-archive",
	}
	if !asset.IsAPK() {
		t.Fatal("content type should identify the asset as an APK")
	}
	if (&Asset{Name: "6f1c0a.bin", URL: "https://r2a.primal.net/blob/6f1c0a.bin"}).IsAPK() {
		t.Fatal("bin path without an APK content type is not an APK")
	}
}

func TestHasUnsupportedArchitecture(t *testing.T) {
	tests := []struct {
		filename    string
		unsupported bool
	}{
		// Unsupported architectures - should be filtered
		{"app-x86_64.apk", true},
		{"app-x86.apk", true},
		{"bunny-6.0-804-x86_64.apk", true},
		{"bunny-6.0-803-x86.apk", true},
		{"app_x86_64_release.apk", true},
		{"app.x86.release.apk", true},
		{"app-i686.apk", false},
		{"app-i386.apk", false},
		{"app-amd64.apk", false},

		// Unsupported 32-bit ARM - should be filtered
		{"app-armeabi-v7a.apk", true},
		{"app-armeabi.apk", true},
		{"app-armeabi-v7a-release.apk", true},

		// Supported architectures - should NOT be filtered (only arm64-v8a)
		{"app-arm64-v8a.apk", false},
		{"bunny-6.0-802-arm64-v8a.apk", false},
		{"bunny-6.0-801-arm.apk", false},    // "arm" alone is ambiguous, don't filter
		{"app-release.apk", false},          // no arch indicator
		{"app.apk", false},                  // no arch indicator
		{"app-universal.apk", false},        // universal
		{"app-v1.0.0.apk", false},           // version, not arch
		{"x86_64-app.apk", true},            // unsupported arch token at start
		{"app-arm64-v8a-fdroid.apk", false}, // arm64 with fdroid suffix

		// Non-APK files - should NOT be filtered
		{"app-x86_64.zip", false},
		{"app-x86.tar.gz", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := HasUnsupportedArchitecture(tt.filename)
			if got != tt.unsupported {
				t.Errorf("HasUnsupportedArchitecture(%q) = %v, want %v", tt.filename, got, tt.unsupported)
			}
		})
	}
}

func TestFilterUnsupportedArchitectures(t *testing.T) {
	assets := []*Asset{
		{Name: "app-arm64-v8a.apk"},
		{Name: "app-x86_64.apk"},
		{Name: "app-armeabi-v7a.apk"},
		{Name: "app-x86.apk"},
		{Name: "app-universal.apk"},
	}

	filtered := FilterUnsupportedArchitectures(assets)

	// Should keep only arm64-v8a and universal (armeabi-v7a is now filtered)
	if len(filtered) != 2 {
		t.Errorf("FilterUnsupportedArchitectures returned %d assets, want 2", len(filtered))
	}

	// Verify the right ones were kept
	names := make(map[string]bool)
	for _, a := range filtered {
		names[a.Name] = true
	}

	if !names["app-arm64-v8a.apk"] {
		t.Error("Expected app-arm64-v8a.apk to be kept")
	}
	if !names["app-universal.apk"] {
		t.Error("Expected app-universal.apk to be kept")
	}
	if names["app-armeabi-v7a.apk"] {
		t.Error("Expected app-armeabi-v7a.apk to be filtered out")
	}
	if names["app-x86_64.apk"] {
		t.Error("Expected app-x86_64.apk to be filtered out")
	}
	if names["app-x86.apk"] {
		t.Error("Expected app-x86.apk to be filtered out")
	}
}

func TestIsTransientDownloadError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, want: true},
		{name: "wrapped unexpected EOF", err: fmt.Errorf("failed to write file: %w", io.ErrUnexpectedEOF), want: true},
		{name: "connection reset string", err: errors.New("read: connection reset by peer"), want: true},
		{name: "download stalled", err: errors.New("download stalled: no data received for 30s"), want: true},
		{name: "permanent 404", err: errors.New("download failed with status 404: https://example.com/a.apk"), want: false},
		{name: "net timeout", err: &net.DNSError{Err: "i/o timeout", IsTimeout: true}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientDownloadError(tt.err); got != tt.want {
				t.Errorf("isTransientDownloadError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestDownloadHTTPRetriesTransientEOF(t *testing.T) {
	var hits atomic.Int32
	payload := []byte("apk-bytes-ok")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			// Advertise full length but close early — triggers unexpected EOF.
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("truncated"))
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "app.apk")
	err := DownloadHTTP(context.Background(), srv.URL, dest, 0, nil, nil)
	if err != nil {
		t.Fatalf("DownloadHTTP() error = %v", err)
	}
	if hits.Load() < 2 {
		t.Fatalf("expected retry after truncated response, hits=%d", hits.Load())
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %q, want %q", got, payload)
	}
}

func TestValidateDownloadURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{"https://github.com/user/app.apk", false},
		{"http://github.com/user/app.apk", true}, // HTTP disallowed for remote hosts
		{"http://localhost/app.apk", false},
		{"http://127.0.0.1:8080/app.apk", false},
		{"javascript:alert(1)", true},
		{"", true},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if err := validateDownloadURL(tt.url); (err != nil) != tt.wantErr {
				t.Errorf("validateDownloadURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

// TestDownloadHTTPRejectsInsecureURL confirms the shared pipeline refuses an
// insecure initial URL before making any request, so callers of DownloadHTTP
// (all remote APK sources) inherit this check for free.
func TestDownloadHTTPRejectsInsecureURL(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "app.apk")
	err := DownloadHTTP(context.Background(), "http://example.com/app.apk", dest, 0, nil, nil)
	if err == nil {
		t.Fatal("DownloadHTTP() error = nil, want rejection of insecure URL")
	}
	if !strings.Contains(err.Error(), "refusing unsafe download URL") {
		t.Fatalf("DownloadHTTP() error = %v, want unsafe URL rejection", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("expected no file written, stat err = %v", statErr)
	}
}

// TestDownloadHTTPClientRejectsUnsafeRedirect exercises the CheckRedirect hook
// directly (rather than through a real multi-hop request, which would also
// exercise the retry loop) to confirm every redirect hop is held to the same
// HTTPS-outside-loopback rule as an explicit URL, and that redirect depth is
// bounded.
func TestDownloadHTTPClientRejectsUnsafeRedirect(t *testing.T) {
	client := newDownloadHTTPClient()

	insecure, err := http.NewRequest(http.MethodGet, "http://evil.example.com/app.apk", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := client.CheckRedirect(insecure, nil); err == nil {
		t.Fatal("CheckRedirect() = nil, want rejection of insecure redirect target")
	}

	safe, err := http.NewRequest(http.MethodGet, "https://cdn.example.com/app.apk", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := client.CheckRedirect(safe, nil); err != nil {
		t.Fatalf("CheckRedirect() = %v, want nil for safe https target", err)
	}

	loopback, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/app.apk", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := client.CheckRedirect(loopback, nil); err != nil {
		t.Fatalf("CheckRedirect() = %v, want nil for loopback http target", err)
	}

	tenHops := make([]*http.Request, 10)
	if err := client.CheckRedirect(safe, tenHops); err == nil {
		t.Fatal("CheckRedirect() = nil, want too-many-redirects rejection at depth 10")
	}
}

// TestDownloadHTTPAttachesHeaders confirms per-source headers (e.g. a GitHub
// or Gitea Authorization token) reach the actual download request.
func TestDownloadHTTPAttachesHeaders(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Length", "4")
		_, _ = w.Write([]byte("data"))
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "app.apk")
	err := DownloadHTTP(context.Background(), srv.URL, dest, 0, map[string]string{"Authorization": "Bearer secret"}, nil)
	if err != nil {
		t.Fatalf("DownloadHTTP() error = %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer secret")
	}
}

func TestCheckHTTPStatusDoesNotExposeResponseBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://example.com/releases", nil)
	response := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Request:    request,
		Body:       io.NopCloser(strings.NewReader("Authorization: Bearer secret-token")),
	}
	err := checkHTTPStatus(response, "test service")
	if err == nil {
		t.Fatal("checkHTTPStatus() error = nil")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaked response body: %v", err)
	}
}
