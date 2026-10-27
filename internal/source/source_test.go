package source

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/zapstore/zsp/v2/internal/config"
	"github.com/zapstore/zsp/v2/internal/netpolicy"
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

func torTransport(t *testing.T, status int, calls *int, seen *http.Request) *torFallback {
	t.Helper()
	return &torFallback{newTransport: func() (http.RoundTripper, error) {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			*calls++
			if seen != nil {
				*seen = *req
			}
			return testResponse(status, "ok"), nil
		}), nil
	}}
}

func TestTorFallbackRetriesForbiddenWithoutCredentials(t *testing.T) {
	tests := []struct {
		name         string
		directStatus int
		directHeader http.Header
		torStatus    int
		torErr       error
		wantStatus   int
		wantErr      string
		wantTorCalls int
		wantRateLim  bool
	}{
		{name: "successful direct request does not use Tor", directStatus: http.StatusOK, wantStatus: http.StatusOK},
		{name: "forbidden request retries through Tor", directStatus: http.StatusForbidden, torStatus: http.StatusOK, wantStatus: http.StatusOK, wantTorCalls: 1},
		{name: "non-forbidden response does not use Tor", directStatus: http.StatusInternalServerError, wantStatus: http.StatusInternalServerError},
		{
			name: "unavailable Tor reports actionable error", directStatus: http.StatusForbidden,
			torErr:  errors.New("connection refused"),
			wantErr: "start Tor with SOCKS5 on 127.0.0.1:9050",
		},
		{
			name: "rate-limit hints survive unavailable Tor", directStatus: http.StatusForbidden,
			directHeader: http.Header{"X-Ratelimit-Remaining": {"0"}},
			torErr:       errors.New("connection refused"),
			wantStatus:   http.StatusForbidden, wantRateLim: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			torCalls := 0
			var torReq http.Request
			tor := torTransport(t, tt.torStatus, &torCalls, &torReq)
			if tt.torErr != nil {
				tor = &torFallback{newTransport: func() (http.RoundTripper, error) { return nil, tt.torErr }}
			}
			direct := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				resp := testResponse(tt.directStatus, "forbidden")
				for name, values := range tt.directHeader {
					resp.Header[name] = values
				}
				return resp, nil
			})
			client := &http.Client{Transport: withTorFallback(direct, tor)}

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/app.apk", nil)
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer secret")
			req.Header.Set("PRIVATE-TOKEN", "secret")
			req.Header.Set("Accept", "application/json")

			resp, err := DoWithTorFallback(t.Context(), client, req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("DoWithTorFallback() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DoWithTorFallback() error = %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if statusErr := checkHTTPStatus(resp, "test"); tt.wantRateLim != errors.Is(statusErr, ErrRateLimited) {
				t.Errorf("checkHTTPStatus() = %v, rate limited want %v", statusErr, tt.wantRateLim)
			}
			if torCalls != tt.wantTorCalls {
				t.Fatalf("Tor request count = %d, want %d", torCalls, tt.wantTorCalls)
			}
			if tt.wantTorCalls > 0 {
				if got := torReq.Header.Get("Authorization") + torReq.Header.Get("PRIVATE-TOKEN"); got != "" {
					t.Errorf("Tor request carried credentials %q", got)
				}
				if got := torReq.Header.Get("Accept"); got != "application/json" {
					t.Errorf("Tor request Accept = %q, want it preserved", got)
				}
			}
			if got := req.Header.Get("Authorization"); got != "Bearer secret" {
				t.Errorf("caller's request Authorization = %q, want it untouched", got)
			}
		})
	}
}

func TestTorFallbackSkipsLoopbackTargets(t *testing.T) {
	torCalls := 0
	direct := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return testResponse(http.StatusForbidden, "no"), nil
	})
	client := &http.Client{Transport: withTorFallback(direct, torTransport(t, http.StatusOK, &torCalls, nil))}
	resp, err := client.Get("http://127.0.0.1:8080/app.apk")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || torCalls != 0 {
		t.Errorf("status = %d, Tor calls = %d; want 403 and 0", resp.StatusCode, torCalls)
	}
}

func TestTorFallbackBuildsTransportOnce(t *testing.T) {
	builds, torCalls := 0, 0
	tor := &torFallback{newTransport: func() (http.RoundTripper, error) {
		builds++
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			torCalls++
			return testResponse(http.StatusOK, "ok"), nil
		}), nil
	}}
	direct := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return testResponse(http.StatusForbidden, "no"), nil
	})
	client := &http.Client{Transport: withTorFallback(direct, tor)}
	for range 3 {
		resp, err := client.Get("https://example.com/a")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		resp.Body.Close()
	}
	if builds != 1 || torCalls != 3 {
		t.Errorf("Tor transport builds = %d, calls = %d; want 1 and 3", builds, torCalls)
	}
}

// TestTorFallbackKeepsRedirectPolicy confirms the client's CheckRedirect still
// governs hops after a Tor replay.
func TestTorFallbackKeepsRedirectPolicy(t *testing.T) {
	direct := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return testResponse(http.StatusForbidden, "no"), nil
	})
	tor := &torFallback{newTransport: func() (http.RoundTripper, error) {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			resp := testResponse(http.StatusFound, "")
			resp.Header.Set("Location", "http://evil.example.com/app.apk")
			resp.Request = req
			return resp, nil
		}), nil
	}}
	client := &http.Client{Transport: withTorFallback(direct, tor), CheckRedirect: validateRedirect}
	resp, err := client.Get("https://example.com/app.apk")
	if err == nil {
		resp.Body.Close()
		t.Fatal("Get() error = nil, want the insecure redirect refused")
	}
	if !errors.Is(err, netpolicy.ErrUnsafeURL) {
		t.Errorf("Get() error = %v, want ErrUnsafeURL", err)
	}
}

func TestValidateRedirectDropsCredentialsOnHostChange(t *testing.T) {
	newReq := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("PRIVATE-TOKEN", "secret")
		return req
	}
	via := []*http.Request{newReq("https://gitlab.example.com/a")}

	same := newReq("https://gitlab.example.com/b")
	if err := validateRedirect(same, via); err != nil {
		t.Fatalf("validateRedirect(same host) = %v", err)
	}
	if same.Header.Get("PRIVATE-TOKEN") == "" || same.Header.Get("Authorization") == "" {
		t.Error("credentials dropped on a same-host redirect")
	}

	other := newReq("https://cdn.example.com/b")
	if err := validateRedirect(other, via); err != nil {
		t.Fatalf("validateRedirect(other host) = %v", err)
	}
	if other.Header.Get("PRIVATE-TOKEN") != "" || other.Header.Get("Authorization") != "" {
		t.Error("credentials kept on a cross-host redirect")
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
			src, err := NewWithOptions(tt.cfg, Options{})

			if (err != nil) != tt.wantErr {
				t.Errorf("NewWithOptions() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				if tt.errContain != "" && err != nil {
					if !strings.Contains(err.Error(), tt.errContain) {
						t.Errorf("NewWithOptions() error = %v, want error containing %q", err, tt.errContain)
					}
				}
				return
			}

			if src == nil {
				t.Error("NewWithOptions() returned nil source")
				return
			}

			if src.Type() != tt.wantType {
				t.Errorf("NewWithOptions() source type = %v, want %v", src.Type(), tt.wantType)
			}
		})
	}
}

func TestReleasesToCheck(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		want    int
		wantErr bool
	}{
		{name: "default", want: defaultMaxReleases},
		{name: "one", n: 1, want: 1},
		{name: "page", n: maxReleasesPage, want: maxReleasesPage},
		{name: "negative", n: -1, wantErr: true},
		{name: "above page", n: maxReleasesPage + 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := releasesToCheck(tt.n)
			if tt.wantErr {
				if err == nil {
					t.Fatal("releasesToCheck() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("releasesToCheck(%d) = %v", tt.n, err)
			}
			if got != tt.want {
				t.Errorf("releasesToCheck(%d) = %d, want %d", tt.n, got, tt.want)
			}
		})
	}
}

func TestNewWithOptionsSetsMaxReleases(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{name: "github", cfg: &config.Config{Repository: "https://github.com/example/app"}},
		{name: "gitlab", cfg: &config.Config{Repository: "https://gitlab.com/example/app"}},
		{name: "gitea", cfg: &config.Config{Repository: "https://codeberg.org/example/app"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created, err := NewWithOptions(tt.cfg, Options{MaxReleases: 25})
			if err != nil {
				t.Fatal(err)
			}
			if got := forgeMaxReleases(t, created); got != 25 {
				t.Fatalf("maxReleases = %d, want 25", got)
			}
			created, err = NewWithOptions(tt.cfg, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if got := forgeMaxReleases(t, created); got != defaultMaxReleases {
				t.Fatalf("default maxReleases = %d, want %d", got, defaultMaxReleases)
			}
		})
	}
	if _, err := NewWithOptions(tests[0].cfg, Options{MaxReleases: 101}); err == nil {
		t.Fatal("NewWithOptions() error = nil, want max releases rejected")
	}
}

func forgeMaxReleases(t *testing.T, created Source) int {
	t.Helper()
	switch source := created.(type) {
	case *GitHub:
		return source.maxReleases
	case *GitLab:
		return source.maxReleases
	case *Gitea:
		return source.maxReleases
	default:
		t.Fatalf("source = %T, want a forge client", created)
		return 0
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

func TestIsTransient(t *testing.T) {
	tlsErr := &url.Error{Op: "Get", URL: "https://example.com", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, want: true},
		{name: "wrapped unexpected EOF", err: fmt.Errorf("write file: %w", io.ErrUnexpectedEOF), want: true},
		{name: "connection reset", err: &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, want: true},
		{name: "connection refused", err: &url.Error{Op: "Get", URL: "https://example.com", Err: syscall.ECONNREFUSED}, want: true},
		{name: "broken pipe", err: fmt.Errorf("write: %w", syscall.EPIPE), want: true},
		{name: "download stalled", err: fmt.Errorf("%w: no data received for 30s", errDownloadStalled), want: true},
		{name: "dial timeout", err: &url.Error{Op: "Get", URL: "https://example.com", Err: &net.DNSError{Err: "i/o timeout", IsTimeout: true}}, want: true},
		{name: "503", err: &HTTPStatusError{StatusCode: http.StatusServiceUnavailable}, want: true},
		{name: "500", err: fmt.Errorf("download: %w", &HTTPStatusError{StatusCode: http.StatusInternalServerError}), want: true},
		{name: "408", err: &HTTPStatusError{StatusCode: http.StatusRequestTimeout}, want: true},
		{name: "429", err: &HTTPStatusError{StatusCode: http.StatusTooManyRequests}, want: true},
		{name: "404", err: &HTTPStatusError{StatusCode: http.StatusNotFound}, want: false},
		{name: "403", err: &HTTPStatusError{StatusCode: http.StatusForbidden}, want: false},
		{name: "x509 verification failure", err: tlsErr, want: false},
		{name: "NXDOMAIN", err: &url.Error{Op: "Get", URL: "https://nope.example", Err: &net.DNSError{Err: "no such host", IsNotFound: true}}, want: false},
		{name: "unsafe URL", err: fmt.Errorf("download: %w", netpolicy.ErrUnsafeURL), want: false},
		{name: "blocked address", err: &url.Error{Op: "Get", URL: "https://x", Err: netpolicy.ErrBlockedAddress}, want: false},
		{name: "checksum mismatch", err: ErrChecksumMismatch, want: false},
		{name: "message text is not matched", err: errors.New("connection reset by peer"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTransient(tt.err); got != tt.want {
				t.Errorf("IsTransient(%v) = %v, want %v", tt.err, got, tt.want)
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
	if !errors.Is(err, netpolicy.ErrUnsafeURL) {
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
	client := newDownloadHTTPClient(nil)

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
