package config

import (
	"fmt"
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
	"HOTBOX_PIN":            true,
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
	values, err := godotenv.Read(path)
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
