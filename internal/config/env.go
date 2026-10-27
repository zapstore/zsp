package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/zapstore/zsp/v2/internal/netpolicy"
	"github.com/zapstore/zsp/v2/internal/sanitize"
)

// DefaultRelayHTTPURL is the REST origin used when RELAYS is unset.
const DefaultRelayHTTPURL = "https://relay.zapstore.dev"

// secretEnvNames identifies environment values that must never appear in
// output: signer material, credentials, and passwords. GetEnv registers every
// value it reads for one of these names with the sanitize package.
var secretEnvNames = map[string]bool{
	"SIGN_WITH":             true,
	"HOTBOX_PIN":            true,
	"KEYSTORE_PASSWORD":     true,
	"KEYSTORE_KEY_PASSWORD": true,
	"GITHUB_TOKEN":          true,
	"GITEA_TOKEN":           true,
	"GITLAB_TOKEN":          true,
}

// Forge hosts that receive a token when GITEA_HOST or GITLAB_HOST is unset.
const (
	defaultGiteaHost  = "codeberg.org"
	defaultGitLabHost = "gitlab.com"
)

// ErrDotEnvSyntax reports a .env file in the working directory that cannot be
// parsed. The parser's own message is withheld: it quotes the offending text,
// which may be a secret.
var ErrDotEnvSyntax = errors.New(".env has invalid syntax")

// GetEnv resolves a setting from the process environment, then from .env in
// the process's exact working directory. It never searches parent directories.
// Values read for a secret name are recorded for redaction. A .env that cannot
// be read or parsed yields "" here; use LookupEnv or CheckDotEnv to see why.
func GetEnv(name string) string {
	value, _ := LookupEnv(name)
	return value
}

// LookupEnv is GetEnv that also reports an unreadable or unparsable .env. The
// error is returned only when the process environment does not set name, so a
// broken .env never hides a value that was provided directly.
func LookupEnv(name string) (string, error) {
	value, err := lookupEnv(name)
	if secretEnvNames[name] {
		sanitize.Register(value)
	}
	return value, err
}

func lookupEnv(name string) (string, error) {
	if value, exists := os.LookupEnv(name); exists {
		return value, nil
	}
	values, err := readDotEnv()
	return values[name], err
}

// readDotEnv parses .env in the working directory. A missing file is not an
// error.
func readDotEnv() (map[string]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(cwd, ".env"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read .env: %w", err)
	}
	values, err := godotenv.Unmarshal(string(data))
	if err != nil {
		return nil, ErrDotEnvSyntax
	}
	return values, nil
}

// CheckDotEnv reports a .env in the working directory that exists but cannot
// be read or parsed. Without it, settings such as SIGN_WITH silently read as unset.
func CheckDotEnv() error {
	_, err := readDotEnv()
	return err
}

// ForgeToken returns the API token for a forge request, or "" when host is not
// the forge the token belongs to. host is a URL host, with a port when the URL
// has one. Tokens go only to an exactly matching host:
//
//   - GitHub: GITHUB_TOKEN to github.com, api.github.com, and
//     raw.githubusercontent.com (repository files such as zapstore.yaml).
//   - Gitea: GITEA_TOKEN to GITEA_HOST (default codeberg.org).
//   - GitLab: GITLAB_TOKEN to GITLAB_HOST (default gitlab.com).
//
// A hostname that merely looks like a forge, such as gitea.example.com, gets
// no token unless it is configured as the forge host.
func ForgeToken(kind SourceType, host string) string {
	host = normalizeForgeHost(host)
	if host == "" {
		return ""
	}
	switch kind {
	case SourceGitHub:
		if host == "github.com" || host == "api.github.com" || host == "raw.githubusercontent.com" {
			return GetEnv("GITHUB_TOKEN")
		}
	case SourceGitea:
		if host == configuredForgeHost("GITEA_HOST", defaultGiteaHost) {
			return GetEnv("GITEA_TOKEN")
		}
	case SourceGitLab:
		if host == configuredForgeHost("GITLAB_HOST", defaultGitLabHost) {
			return GetEnv("GITLAB_TOKEN")
		}
	}
	return ""
}

// configuredForgeHost reads a forge host setting, which may be a bare host,
// host:port, or a URL.
func configuredForgeHost(name, fallback string) string {
	value := strings.TrimSpace(GetEnv(name))
	if value == "" {
		return fallback
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return ""
		}
		value = parsed.Host
	}
	value, _, _ = strings.Cut(value, "/")
	return normalizeForgeHost(value)
}

func normalizeForgeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ":443")
	return strings.TrimSuffix(host, ".")
}

// GetSignWith returns SIGN_WITH. An unreadable or malformed .env is an error
// rather than an empty value, so the caller does not report a missing key.
func GetSignWith() (string, error) {
	return LookupEnv("SIGN_WITH")
}

// CheckDotEnvPermissions refuses a .env in the working directory that other
// users can read when it holds SIGN_WITH or HOTBOX_PIN: either one lets the
// reader sign as the user.
func CheckDotEnvPermissions() error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	path := filepath.Join(cwd, ".env")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	values, err := readDotEnv()
	if err != nil {
		return nil
	}
	for _, name := range []string{"SIGN_WITH", "HOTBOX_PIN"} {
		if values[name] != "" {
			return fmt.Errorf(".env holds %s and is readable by other users; run chmod 600 .env", name)
		}
	}
	return nil
}

// GetHotbox returns the hotbox socket and PIN, or empty strings when either is unset.
func GetHotbox() (socket, pin string) {
	socket, pin = GetEnv("HOTBOX_SOCKET"), GetEnv("HOTBOX_PIN")
	if socket == "" || pin == "" {
		return "", ""
	}
	return socket, pin
}

// RelaySocketPath reports whether raw is a Unix socket relay target.
// Absolute paths and unix:// URLs qualify. The returned path has no scheme.
func RelaySocketPath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	var path string
	switch {
	case strings.HasPrefix(raw, "/"):
		path = raw
	case strings.HasPrefix(raw, "unix://"):
		path = strings.TrimPrefix(raw, "unix://")
	default:
		return "", false
	}
	if path == "" || strings.ContainsRune(path, 0) {
		return "", false
	}
	return path, true
}

// GetRelayHTTPURL returns the REST origin corresponding to the first WebSocket
// relay in RELAYS. Unix socket targets are skipped. WebSocket schemes are
// converted to their HTTP equivalents.
func GetRelayHTTPURL() string {
	for _, relay := range strings.Split(GetEnv("RELAYS"), ",") {
		if _, ok := RelaySocketPath(relay); ok {
			continue
		}
		if value := normalizeRelayHTTPURL(relay); value != "" {
			return value
		}
	}
	return DefaultRelayHTTPURL
}

func normalizeRelayHTTPURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		if loopbackHost(value) {
			value = "http://" + value
		} else {
			value = "https://" + value
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return value
	}
	switch parsed.Scheme {
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	}
	parsed.User = nil
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func loopbackHost(value string) bool {
	host := value
	if h, _, err := net.SplitHostPort(value); err == nil {
		host = h
	}
	return netpolicy.IsLoopbackHost(host)
}

func GetKeystorePassword() string {
	return GetEnv("KEYSTORE_PASSWORD")
}

func GetKeystoreKeyPassword() string {
	return GetEnv("KEYSTORE_KEY_PASSWORD")
}
