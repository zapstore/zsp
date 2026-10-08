package config

import (
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
