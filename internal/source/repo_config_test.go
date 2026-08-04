package source

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

func TestResolveIndexerConfig(t *testing.T) {
	repoYAML := `repository: https://github.com/owner/app
release_source: https://github.com/owner/app
name: Repo App
match: ".*-arm64.*\\.apk$"
`
	indexerCfg := &config.Config{
		Repository: "https://github.com/owner/app",
		ReleaseSource: &config.ReleaseSource{
			URL: "https://f-droid.org/packages/com.example.app",
		},
		Name: "Indexer Name",
	}

	tests := []struct {
		name      string
		cfg       *config.Config
		transport roundTripperFunc
		torStatus int   // if set, Tor fallback returns this status with repoYAML
		torErr    error // if set, Tor client factory fails
		wantName  string
		wantMatch string
		wantSame  bool // expect returned pointer == indexer cfg
		wantErr   bool
	}{
		{
			name: "GitHub repo config replaces indexer config",
			cfg:  indexerCfg,
			transport: func(req *http.Request) (*http.Response, error) {
				if req.URL.Host+req.URL.Path == "raw.githubusercontent.com/owner/app/HEAD/zapstore.yaml" {
					return testResponse(http.StatusOK, repoYAML), nil
				}
				return testResponse(http.StatusNotFound, ""), nil
			},
			wantName:  "Repo App",
			wantMatch: `.*-arm64.*\.apk$`,
		},
		{
			name: "GitHub 404 keeps indexer config",
			cfg:  indexerCfg,
			transport: func(req *http.Request) (*http.Response, error) {
				return testResponse(http.StatusNotFound, ""), nil
			},
			wantSame: true,
			wantName: "Indexer Name",
		},
		{
			name: "GitHub 403 then Tor serves repo config",
			cfg:  indexerCfg,
			transport: func(req *http.Request) (*http.Response, error) {
				return testResponse(http.StatusForbidden, "rate limit"), nil
			},
			torStatus: http.StatusOK,
			wantName:  "Repo App",
			wantMatch: `.*-arm64.*\.apk$`,
		},
		{
			name: "GitHub 403 Tor unavailable keeps indexer config",
			cfg:  indexerCfg,
			transport: func(req *http.Request) (*http.Response, error) {
				return testResponse(http.StatusForbidden, "rate limit"), nil
			},
			torErr:   errors.New("connection refused"),
			wantSame: true,
			wantName: "Indexer Name",
		},
		{
			name: "GitHub 403 after Tor keeps indexer config",
			cfg:  indexerCfg,
			transport: func(req *http.Request) (*http.Response, error) {
				return testResponse(http.StatusForbidden, "rate limit"), nil
			},
			torStatus: http.StatusForbidden,
			wantSame:  true,
			wantName:  "Indexer Name",
		},
		{
			name: "GitHub 429 keeps indexer config",
			cfg:  indexerCfg,
			transport: func(req *http.Request) (*http.Response, error) {
				return testResponse(http.StatusTooManyRequests, "slow down"), nil
			},
			wantSame: true,
			wantName: "Indexer Name",
		},
		{
			name: "unsupported forge keeps indexer config",
			cfg: &config.Config{
				Repository: "https://example.com/owner/app",
				Name:       "Indexer Name",
				ReleaseSource: &config.ReleaseSource{
					URL: "https://example.com/app.apk",
				},
			},
			transport: func(req *http.Request) (*http.Response, error) {
				t.Fatal("no HTTP call expected for unsupported forge")
				return nil, nil
			},
			wantSame: true,
			wantName: "Indexer Name",
		},
		{
			name: "GitLab repo config on default branch",
			cfg: &config.Config{
				Repository: "https://gitlab.com/owner/app",
				Name:       "Indexer Name",
				ReleaseSource: &config.ReleaseSource{
					URL: "https://gitlab.com/owner/app",
				},
			},
			transport: func(req *http.Request) (*http.Response, error) {
				wantPath := "/api/v4/projects/owner%2Fapp/repository/files/zapstore.yaml/raw"
				if got := req.URL.EscapedPath(); got != wantPath {
					t.Errorf("EscapedPath = %q, want %q", got, wantPath)
				}
				if req.URL.Query().Get("ref") != "HEAD" {
					t.Errorf("ref = %q, want HEAD", req.URL.Query().Get("ref"))
				}
				return testResponse(http.StatusOK, repoYAML), nil
			},
			wantName:  "Repo App",
			wantMatch: `.*-arm64.*\.apk$`,
		},
		{
			name: "Gitea/Codeberg repo config",
			cfg: &config.Config{
				Repository: "https://codeberg.org/owner/app",
				Name:       "Indexer Name",
				ReleaseSource: &config.ReleaseSource{
					URL: "https://codeberg.org/owner/app",
				},
			},
			transport: func(req *http.Request) (*http.Response, error) {
				if req.URL.Host+req.URL.Path != "codeberg.org/api/v1/repos/owner/app/raw/zapstore.yaml" {
					t.Errorf("unexpected URL: %s%s", req.URL.Host, req.URL.Path)
				}
				return testResponse(http.StatusOK, repoYAML), nil
			},
			wantName:  "Repo App",
			wantMatch: `.*-arm64.*\.apk$`,
		},
		{
			name: "malformed repo YAML keeps indexer config",
			cfg:  indexerCfg,
			transport: func(req *http.Request) (*http.Response, error) {
				return testResponse(http.StatusOK, "name: [unterminated"), nil
			},
			wantSame: true,
			wantName: "Indexer Name",
		},
		{
			name: "API 500 keeps indexer config",
			cfg:  indexerCfg,
			transport: func(req *http.Request) (*http.Response, error) {
				return testResponse(http.StatusInternalServerError, "boom"), nil
			},
			wantSame: true,
			wantName: "Indexer Name",
		},
		{
			name: "network error keeps indexer config",
			cfg:  indexerCfg,
			transport: func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("connection reset")
			},
			wantSame: true,
			wantName: "Indexer Name",
		},
		{
			name: "cancelled context",
			cfg:  indexerCfg,
			transport: func(req *http.Request) (*http.Response, error) {
				return nil, req.Context().Err()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.name == "cancelled context" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			}

			if tt.torStatus != 0 || tt.torErr != nil {
				body := repoYAML
				restoreTor := SetTorHTTPClientForTest(func() (*http.Client, error) {
					if tt.torErr != nil {
						return nil, tt.torErr
					}
					return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						return testResponse(tt.torStatus, body), nil
					})}, nil
				})
				t.Cleanup(restoreTor)
			}

			client := &http.Client{Transport: tt.transport}
			got, err := resolveIndexerConfig(ctx, tt.cfg, client)
			if tt.wantErr {
				if err == nil {
					t.Fatal("resolveIndexerConfig() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveIndexerConfig() error = %v", err)
			}
			if tt.wantSame {
				if got != tt.cfg {
					t.Fatal("expected indexer config pointer unchanged")
				}
			} else if got == tt.cfg {
				t.Fatal("expected repo config to replace indexer config")
			}
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
			if tt.wantMatch != "" && got.Match != tt.wantMatch {
				t.Errorf("Match = %q, want %q", got.Match, tt.wantMatch)
			}
		})
	}
}

func TestResolveIndexerConfigNil(t *testing.T) {
	_, err := resolveIndexerConfig(context.Background(), nil, &http.Client{})
	if err == nil {
		t.Fatal("expected error for nil indexer config")
	}
}
