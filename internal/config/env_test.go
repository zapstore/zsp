package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zapstore/zsp/internal/sanitize"
)

func TestGetEnvRegistersSecretValuesForRedaction(t *testing.T) {
	const token = "ghp_configtesttokenvalue1234567890"
	t.Setenv("GITHUB_TOKEN", token)
	if got := GetEnv("GITHUB_TOKEN"); got != token {
		t.Fatalf("GetEnv(GITHUB_TOKEN) = %q, want %q", got, token)
	}
	if redacted := sanitize.Text("request failed with " + token); strings.Contains(redacted, token) {
		t.Fatalf("sanitized text leaked a secret env value: %q", redacted)
	}
}

func TestGetEnvDoesNotRegisterNonSecretValues(t *testing.T) {
	const relay = "wss://relay.example/path"
	t.Setenv("RELAYS", relay)
	if got := GetEnv("RELAYS"); got != relay {
		t.Fatalf("GetEnv(RELAYS) = %q, want %q", got, relay)
	}
	// Relay URLs are public context: only credentials are stripped, the URL
	// itself must survive sanitization.
	if redacted := sanitize.Text("couldn't reach " + relay); redacted != "couldn't reach "+relay {
		t.Fatalf("sanitized text mangled a relay URL: %q", redacted)
	}
}

func TestForgeToken(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		kind SourceType
		host string
		want string
	}{
		{"github.com", nil, SourceGitHub, "github.com", "gh"},
		{"api.github.com", nil, SourceGitHub, "api.github.com", "gh"},
		{"github host is case-insensitive", nil, SourceGitHub, "API.GitHub.com", "gh"},
		{"github default https port", nil, SourceGitHub, "api.github.com:443", "gh"},
		{"github other port", nil, SourceGitHub, "api.github.com:8443", ""},
		{"github lookalike suffix", nil, SourceGitHub, "github.com.evil.example", ""},
		{"github lookalike prefix", nil, SourceGitHub, "evilgithub.com", ""},
		{"github raw host", nil, SourceGitHub, "raw.githubusercontent.com", "gh"},
		{"github raw lookalike", nil, SourceGitHub, "raw.githubusercontent.com.evil.example", ""},
		{"github user content host", nil, SourceGitHub, "objects.githubusercontent.com", ""},
		{"github codeload host", nil, SourceGitHub, "codeload.github.com", ""},
		{"github subdomain", nil, SourceGitHub, "gist.github.com", ""},
		{"github token never goes to gitea host", nil, SourceGitHub, "codeberg.org", ""},

		{"gitea default codeberg", nil, SourceGitea, "codeberg.org", "gt"},
		{"gitea default host mismatch", nil, SourceGitea, "git.example.com", ""},
		{"gitea name hint is not enough", nil, SourceGitea, "gitea.example.com", ""},
		{"forgejo name hint is not enough", nil, SourceGitea, "forgejo.example.com", ""},
		{"gitea configured host", map[string]string{"GITEA_HOST": "git.example.com"}, SourceGitea, "git.example.com", "gt"},
		{"gitea configured host excludes default", map[string]string{"GITEA_HOST": "git.example.com"}, SourceGitea, "codeberg.org", ""},
		{"gitea configured URL", map[string]string{"GITEA_HOST": "https://git.example.com/"}, SourceGitea, "git.example.com", "gt"},
		{"gitea configured host with port", map[string]string{"GITEA_HOST": "git.example.com:3000"}, SourceGitea, "git.example.com:3000", "gt"},
		{"gitea configured host port mismatch", map[string]string{"GITEA_HOST": "git.example.com:3000"}, SourceGitea, "git.example.com", ""},
		{"gitea configured host without port, URL with port", map[string]string{"GITEA_HOST": "git.example.com"}, SourceGitea, "git.example.com:8080", ""},
		{"gitea configured host suffix", map[string]string{"GITEA_HOST": "git.example.com"}, SourceGitea, "git.example.com.evil.example", ""},
		{"gitea unparsable configured host", map[string]string{"GITEA_HOST": "https://%zz"}, SourceGitea, "codeberg.org", ""},

		{"gitlab default", nil, SourceGitLab, "gitlab.com", "gl"},
		{"gitlab name hint is not enough", nil, SourceGitLab, "gitlab.example.com", ""},
		{"gitlab configured host", map[string]string{"GITLAB_HOST": "gitlab.example.com"}, SourceGitLab, "gitlab.example.com", "gl"},
		{"gitlab configured host excludes default", map[string]string{"GITLAB_HOST": "gitlab.example.com"}, SourceGitLab, "gitlab.com", ""},
		{"gitlab token never goes to github", nil, SourceGitLab, "github.com", ""},

		{"unknown kind", nil, SourceUnknown, "github.com", ""},
		{"web kind", nil, SourceWeb, "github.com", ""},
		{"empty host", nil, SourceGitHub, "", ""},
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
			if got := ForgeToken(tt.kind, tt.host); got != tt.want {
				t.Errorf("ForgeToken(%v, %q) = %q, want %q", tt.kind, tt.host, got, tt.want)
			}
		})
	}
}

func TestLookupEnvReportsDotEnvSyntaxError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	const secret = "nsec1supersecretvalue"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SIGN_WITH="+secret+"\nBROKEN LINE WITH SPACES\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	value, err := LookupEnv("ZSP_TEST_NOT_SET_ANYWHERE")
	if value != "" || !errors.Is(err, ErrDotEnvSyntax) {
		t.Fatalf("LookupEnv() = %q, %v; want empty value and ErrDotEnvSyntax", value, err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("LookupEnv() error leaks .env content: %v", err)
	}
	if err := CheckDotEnv(); !errors.Is(err, ErrDotEnvSyntax) {
		t.Errorf("CheckDotEnv() = %v, want ErrDotEnvSyntax", err)
	}

	t.Setenv("ZSP_TEST_FROM_PROCESS", "set")
	if value, err := LookupEnv("ZSP_TEST_FROM_PROCESS"); value != "set" || err != nil {
		t.Errorf("LookupEnv(process var) = %q, %v; want set, nil", value, err)
	}
}

func TestCheckDotEnvAcceptsMissingAndValidFiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := CheckDotEnv(); err != nil {
		t.Fatalf("CheckDotEnv() without .env = %v, want nil", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A=1\n# comment\nexport B=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckDotEnv(); err != nil {
		t.Fatalf("CheckDotEnv() with valid .env = %v, want nil", err)
	}
	if got := GetEnv("B"); got != "2" {
		t.Errorf("GetEnv(B) = %q, want 2", got)
	}
}
