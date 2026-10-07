package config

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/zapstore/zsp/internal/sanitize"
)

// DefaultRelayHTTPURL is the REST origin used when RELAYS is unset.
const DefaultRelayHTTPURL = "https://relay.zapstore.dev"

// secretEnvNames identifies environment values that must never appear in
// output: signer material, credentials, and passwords. GetEnv registers every
// value it reads for one of these names with the sanitize package.
var secretEnvNames = map[string]bool{
	"SIGN_WITH":             true,
	"KEYSTORE_PASSWORD":     true,
	"KEYSTORE_KEY_PASSWORD": true,
	"GITHUB_TOKEN":          true,
	"GITEA_TOKEN":           true,
}

// GetEnv resolves a setting from the process environment, then from .env in
// the process's exact working directory. It never searches parent directories.
// Values read for a secret name are recorded for redaction.
func GetEnv(name string) string {
	value := lookupEnv(name)
	if secretEnvNames[name] {
		sanitize.Register(value)
	}
	return value
}

func lookupEnv(name string) string {
	if value, exists := os.LookupEnv(name); exists {
		return value
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	values, err := godotenv.Read(filepath.Join(cwd, ".env"))
	if err != nil {
		return ""
	}
	return values[name]
}

func GetSignWith() string {
	return GetEnv("SIGN_WITH")
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
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func GetKeystorePassword() string {
	return GetEnv("KEYSTORE_PASSWORD")
}

func GetKeystoreKeyPassword() string {
	return GetEnv("KEYSTORE_KEY_PASSWORD")
}
