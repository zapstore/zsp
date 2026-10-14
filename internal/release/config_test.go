package release

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	internalconfig "github.com/zapstore/zsp/internal/config"
	"gopkg.in/yaml.v3"
)

func TestFetchInternalConfigNormalizesLikeYAML(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		source *ReleaseSource
	}{
		{
			name:   "direct apk url",
			yaml:   "release_source: https://example.com/app.apk\n",
			source: &ReleaseSource{URL: "https://example.com/app.apk"},
		},
		{
			name:   "direct apk url as a map",
			yaml:   "release_source:\n  url: https://example.com/app.apk\n",
			source: &ReleaseSource{URL: "https://example.com/app.apk"},
		},
		{
			name:   "forge",
			yaml:   "release_source: https://github.com/example/app\n",
			source: &ReleaseSource{URL: "https://github.com/example/app"},
		},
		{
			name:   "typed self-hosted forge",
			yaml:   "release_source:\n  url: https://git.example.com/team/app\n  type: gitlab\n",
			source: &ReleaseSource{URL: "https://git.example.com/team/app", Type: "gitlab"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := internalconfig.Parse(strings.NewReader(tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			fromYAML, err := internalconfig.NormalizeReleaseSource(*parsed.ReleaseSource)
			if err != nil {
				t.Fatal(err)
			}

			internal, err := fetchInternalConfig(FetchConfig{ReleaseSource: tt.source})
			if err != nil {
				t.Fatalf("fetchInternalConfig(%+v): %v", tt.source, err)
			}
			if *internal.ReleaseSource != *fromYAML {
				t.Errorf("library source = %+v, YAML source = %+v, want the same", *internal.ReleaseSource, *fromYAML)
			}
		})
	}
}

func TestFetchInternalConfigRejectsInvalidLocalSources(t *testing.T) {
	tests := []struct {
		name   string
		source ReleaseSource
	}{
		{name: "relative local path", source: ReleaseSource{LocalPath: "app.apk"}},
		{name: "local with remote type", source: ReleaseSource{LocalPath: "/x/app.apk", Type: "github"}},
		{name: "local with asset url", source: ReleaseSource{LocalPath: "/x/app.apk", AssetURL: "https://example.com/a.apk"}},
		{name: "local type with two paths", source: ReleaseSource{URL: "/x", LocalPath: "/y", Type: "local"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fetchInternalConfig(FetchConfig{ReleaseSource: &tt.source})
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("fetchInternalConfig(%+v) error = %v, want ErrInvalidConfig", tt.source, err)
			}
		})
	}
}

func TestParseConfigRejectsTyposAndEmptyFiles(t *testing.T) {
	for name, input := range map[string]string{
		"typo":  "release_sorce: https://github.com/example/app\n",
		"empty": "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseConfig(strings.NewReader(input)); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("ParseConfig(%q) error = %v, want ErrInvalidConfig", input, err)
			}
		})
	}
}

func TestParseFetchConfigKeepsDirectURLAsWritten(t *testing.T) {
	for name, input := range map[string]string{
		"scalar":  "release_source: https://example.com/app.apk\n",
		"mapping": "release_source:\n  url: https://example.com/app.apk\n",
	} {
		t.Run(name, func(t *testing.T) {
			config, err := ParseFetchConfig(strings.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			got := config.ReleaseSource
			if got == nil || got.URL != "https://example.com/app.apk" || got.AssetURL != "" {
				t.Fatalf("ReleaseSource = %+v, want URL as written and no AssetURL", got)
			}
			marshaled, err := yaml.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if want := "release_source: https://example.com/app.apk\n"; string(marshaled) != want {
				t.Errorf("MarshalYAML = %q, want %q", marshaled, want)
			}
			again, err := ParseFetchConfig(bytes.NewReader(marshaled))
			if err != nil || !reflect.DeepEqual(again, config) {
				t.Errorf("round trip = %+v, %v, want %+v", again, err, config)
			}
		})
	}
}

func TestLoadConfigKeepsDirectURLAsWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zapstore.yaml")
	if err := os.WriteFile(path, []byte("release_source: https://example.com/app.apk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Fetch.ReleaseSource; got == nil || got.URL != "https://example.com/app.apk" || got.AssetURL != "" || got.LocalPath != "" {
		t.Fatalf("ReleaseSource = %+v, want URL as written", got)
	}
}

func TestFetchDirectURLSource(t *testing.T) {
	apkData, err := os.ReadFile("../../testdata/apks/sample.apk")
	if err != nil {
		t.Fatal(err)
	}
	serve := func(rejectHead bool, contentType string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rejectHead && r.Method == http.MethodHead {
				http.Error(w, "no", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write(apkData)
		}))
		t.Cleanup(server.Close)
		return server
	}
	const apkType = "application/vnd.android.package-archive"

	tests := []struct {
		name        string
		path        string
		rejectHead  bool
		contentType string
		wantErr     error
	}{
		{name: "apk path", path: "/app.apk", contentType: apkType},
		{name: "apk path with HEAD rejected", path: "/app.apk", rejectHead: true, contentType: "application/octet-stream"},
		{name: "extensionless path served as an APK", path: "/downloads", contentType: apkType},
		{name: "extensionless path with HEAD rejected", path: "/downloads", rejectHead: true, contentType: apkType, wantErr: ErrNoAPK},
		{name: "extensionless path serving a page", path: "/downloads", contentType: "text/html", wantErr: ErrNoAPK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := serve(tt.rejectHead, tt.contentType)
			apks, err := Fetch(t.Context(), FetchConfig{ReleaseSource: &ReleaseSource{URL: server.URL + tt.path}}, FetchOptions{SkipHTTPCache: true})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Fetch() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Fetch(): %v", err)
			}
			defer closeAPKs(apks)
			if len(apks) != 1 || apks[0].AppID == "" {
				t.Fatalf("Fetch() = %+v, want the one downloaded APK", apks)
			}
		})
	}
}
