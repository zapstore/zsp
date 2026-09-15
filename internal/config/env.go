package config

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

// DefaultRelayHTTPURL is the REST origin used when RELAYS is unset.
const DefaultRelayHTTPURL = "https://relay.zapstore.dev"

// GetEnv resolves a setting from the process environment, then from .env in
// the process's exact working directory. It never searches parent directories.
func GetEnv(name string) string {
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

// GetRelayHTTPURL returns the REST origin corresponding to the first relay in
// RELAYS. WebSocket schemes are converted to their HTTP equivalents.
func GetRelayHTTPURL() string {
	for _, relay := range strings.Split(GetEnv("RELAYS"), ",") {
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
