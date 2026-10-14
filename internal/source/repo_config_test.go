package source

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

func TestFetchRepoConfig(t *testing.T) {
	const repoYAML = "repository: https://github.com/owner/app\nname: Repo App\n"
	unavailable := func(err error) bool { return errors.Is(err, errRepoConfigUnavailable) }
	respond := func(status int, body string) roundTripperFunc {
		return func(*http.Request) (*http.Response, error) { return testResponse(status, body), nil }
	}

	tests := []struct {
		name       string
		repository string
		transport  roundTripperFunc
		torStatus  int
		torErr     error
		cancelled  bool
		wantBody   string
		wantErr    func(error) bool
	}{
		{
			name:       "GitHub repo config",
			repository: "https://github.com/owner/app",
			transport: func(req *http.Request) (*http.Response, error) {
				if req.URL.Host+req.URL.Path == "raw.githubusercontent.com/owner/app/HEAD/zapstore.yaml" {
					return testResponse(http.StatusOK, repoYAML), nil
				}
				return testResponse(http.StatusNotFound, ""), nil
			},
			wantBody: repoYAML,
		},
		{
			name:       "GitHub 404",
			repository: "https://github.com/owner/app",
			transport:  respond(http.StatusNotFound, ""),
			wantErr:    unavailable,
		},
		{
			name:       "GitHub 403 then Tor serves repo config",
			repository: "https://github.com/owner/app",
			transport:  respond(http.StatusForbidden, "denied"),
			torStatus:  http.StatusOK,
			wantBody:   repoYAML,
		},
		{
			name:       "GitHub 403 Tor unavailable",
			repository: "https://github.com/owner/app",
			transport:  respond(http.StatusForbidden, "denied"),
			torErr:     errors.New("connection refused"),
			wantErr:    unavailable,
		},
		{
			name:       "GitHub 403 after Tor",
			repository: "https://github.com/owner/app",
			transport:  respond(http.StatusForbidden, "denied"),
			torStatus:  http.StatusForbidden,
			wantErr:    unavailable,
		},
		{
			name:       "GitHub 429",
			repository: "https://github.com/owner/app",
			transport:  respond(http.StatusTooManyRequests, "slow down"),
			wantErr:    unavailable,
		},
		{
			name:       "API 500",
			repository: "https://github.com/owner/app",
			transport:  respond(http.StatusInternalServerError, "boom"),
			wantErr:    unavailable,
		},
		{
			name:       "network error",
			repository: "https://github.com/owner/app",
			transport: func(*http.Request) (*http.Response, error) {
				return nil, errors.New("connection reset")
			},
			wantErr: unavailable,
		},
		{
			name:       "blank file",
			repository: "https://github.com/owner/app",
			transport:  respond(http.StatusOK, " \n"),
			wantErr:    unavailable,
		},
		{
			name:       "file over the size limit",
			repository: "https://github.com/owner/app",
			transport:  respond(http.StatusOK, strings.Repeat("a", MaxRemoteDownloadSize+1)),
			wantErr:    unavailable,
		},
		{
			name:       "unsupported forge",
			repository: "https://example.com/owner/app",
			transport: func(*http.Request) (*http.Response, error) {
				t.Fatal("no HTTP call expected for unsupported forge")
				return nil, nil
			},
			wantErr: unavailable,
		},
		{
			name:       "GitLab repo config on default branch",
			repository: "https://gitlab.com/owner/app",
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
			wantBody: repoYAML,
		},
		{
			name:       "Gitea/Codeberg repo config",
			repository: "https://codeberg.org/owner/app",
			transport: func(req *http.Request) (*http.Response, error) {
				if req.URL.Host+req.URL.Path != "codeberg.org/api/v1/repos/owner/app/raw/zapstore.yaml" {
					t.Errorf("unexpected URL: %s%s", req.URL.Host, req.URL.Path)
				}
				return testResponse(http.StatusOK, repoYAML), nil
			},
			wantBody: repoYAML,
		},
		{
			name:       "cancelled context",
			repository: "https://github.com/owner/app",
			transport: func(req *http.Request) (*http.Response, error) {
				return nil, req.Context().Err()
			},
			cancelled: true,
			wantErr:   func(err error) bool { return errors.Is(err, context.Canceled) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			if tt.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			torCalls := 0
			tor := &torFallback{newTransport: func() (http.RoundTripper, error) {
				if tt.torErr != nil {
					return nil, tt.torErr
				}
				return roundTripperFunc(func(*http.Request) (*http.Response, error) {
					torCalls++
					return testResponse(tt.torStatus, repoYAML), nil
				}), nil
			}}
			client := &http.Client{Transport: withTorFallback(tt.transport, tor)}

			got, err := fetchRepoConfig(ctx, client, &config.Config{Repository: tt.repository})
			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("fetchRepoConfig() error = %v, want matching error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("fetchRepoConfig() error = %v", err)
			}
			if string(got) != tt.wantBody {
				t.Errorf("fetchRepoConfig() = %q, want %q", got, tt.wantBody)
			}
			if wantTor := tt.torStatus != 0; wantTor != (torCalls > 0) {
				t.Errorf("Tor calls = %d, want used = %v", torCalls, wantTor)
			}
		})
	}
}

// TestFetchRepoConfigSendsForgeToken confirms the repository-file request
// carries the token of the forge it targets, and nothing for other hosts.
func TestFetchRepoConfigSendsForgeToken(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		env        map[string]string
		header     string
		want       string
	}{
		{"github", "https://github.com/owner/app", nil, "Authorization", "Bearer gh"},
		{"gitea default host", "https://codeberg.org/owner/app", nil, "Authorization", "token gt"},
		{"gitea unconfigured self-hosted", "https://forgejo.example.com/owner/app", map[string]string{"GITEA_HOST": "other.example.com"}, "Authorization", ""},
		{"gitea configured self-hosted", "https://git.example.com/owner/app", map[string]string{"GITEA_HOST": "git.example.com"}, "Authorization", "token gt"},
		{"gitea self-hosted without GITEA_HOST", "https://git.example.com/owner/app", nil, "Authorization", ""},
		{"gitlab default host", "https://gitlab.com/owner/app", nil, "PRIVATE-TOKEN", "gl"},
		{"gitlab name hint only", "https://gitlab.example.com/owner/app", nil, "PRIVATE-TOKEN", ""},
		{"gitlab configured host", "https://gitlab.example.com/owner/app", map[string]string{"GITLAB_HOST": "gitlab.example.com"}, "PRIVATE-TOKEN", "gl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("GITHUB_TOKEN", "gh")
			t.Setenv("GITEA_TOKEN", "gt")
			t.Setenv("GITLAB_TOKEN", "gl")
			t.Setenv("GITEA_HOST", "")
			t.Setenv("GITLAB_HOST", "")
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			var got string
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				got = req.Header.Get(tt.header)
				return testResponse(http.StatusOK, "name: app\n"), nil
			})}
			// An explicit gitea type lets a self-hosted forge without a
			// recognizable hostname use the Gitea API.
			cfg := &config.Config{Repository: tt.repository, ReleaseSource: &config.ReleaseSource{Type: "gitea"}}
			if _, err := fetchRepoConfig(t.Context(), client, cfg); err != nil {
				t.Fatalf("fetchRepoConfig() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("%s header = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestRepoFileURL(t *testing.T) {
	tests := []struct {
		repository, path, want string
	}{
		{"https://github.com/owner/app", "./assets/icon one.png", "https://raw.githubusercontent.com/owner/app/HEAD/assets/icon%20one.png"},
		{"https://gitlab.com/group/app", "docs/CHANGELOG.md", "https://gitlab.com/api/v4/projects/group%2Fapp/repository/files/docs%2FCHANGELOG.md/raw?ref=HEAD"},
		{"https://codeberg.org/owner/app", "../../etc/icon.png", "https://codeberg.org/api/v1/repos/owner/app/raw/etc/icon.png"},
	}
	for _, tt := range tests {
		got, ok := RepoFileURL(tt.repository, tt.path)
		if !ok || got != tt.want {
			t.Errorf("RepoFileURL(%q, %q) = (%q, %v), want %q", tt.repository, tt.path, got, ok, tt.want)
		}
	}
	for _, unsupported := range [][2]string{{"https://example.com/app", "icon.png"}, {"https://github.com/owner/app", "."}} {
		if got, ok := RepoFileURL(unsupported[0], unsupported[1]); ok {
			t.Errorf("RepoFileURL(%q, %q) = %q, want unsupported", unsupported[0], unsupported[1], got)
		}
	}
}
