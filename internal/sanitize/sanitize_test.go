package sanitize

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testNsec = "nsec1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpj0f0h"
	testHex  = "18e14a7b6a307f426a94f8114701e7c8e774e7f9a47e2c2035db29a206321725"
	testTok  = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
)

func TestTextRedactsNsec(t *testing.T) {
	got := Text("sign failed for " + testNsec + " now")
	if strings.Contains(got, testNsec) || !strings.Contains(got, redacted) {
		t.Fatalf("Text() = %q, want nsec redacted", got)
	}
}

func TestTextRedactsRegisteredSecrets(t *testing.T) {
	Register(testHex, testTok)
	for _, text := range []string{
		"key " + testHex + " rejected",
		"authorization failed for " + testTok,
	} {
		got := Text(text)
		if strings.Contains(got, testHex) || strings.Contains(got, testTok) {
			t.Fatalf("Text(%q) = %q, leaked a registered secret", text, got)
		}
	}
}

func TestTextRedactsURLCredentials(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{
			"cannot store wss://relay.example/?token=secret",
			"cannot store wss://relay.example/",
		},
		{
			`Get "https://user:pass@cdn.example/icon?token=secret#frag": dial failed`,
			`Get "https://cdn.example/icon": dial failed`,
		},
		{
			"bunker://pubkey?relay=wss://relay.example&secret=verysecretvalue failed",
			"bunker://pubkey failed",
		},
	}
	for _, test := range tests {
		if got := Text(test.text); got != test.want {
			t.Fatalf("Text(%q) = %q, want %q", test.text, got, test.want)
		}
	}
}

func TestTextRedactsCredentialAssignments(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"token=abc123 was rejected", "token=[redacted] was rejected"},
		{"password: hunter2 is wrong", "password: [redacted] is wrong"},
		{"Authorization: Nostr aGVsbG8gd29ybGQgdGhpcyBpcyBhIHRva2Vu", "Authorization: Nostr [redacted]"},
		{"sent Bearer abcdefghijklmnopqrstuvwxyz012345", "sent Bearer [redacted]"},
		{"line 3: cannot unmarshal !!str `ghp_abcdefghijk...` into config.ReleaseSource", "line 3: cannot unmarshal !!str [redacted] into config.ReleaseSource"},
	}
	for _, test := range tests {
		if got := Text(test.text); got != test.want {
			t.Fatalf("Text(%q) = %q, want %q", test.text, got, test.want)
		}
	}
}

func TestTextKeepsOrdinaryContext(t *testing.T) {
	for _, text := range []string{
		"relay rejected event: blocked by policy",
		"HTTP 404 Not Found: https://relay.example/path",
		"rate limited (429): retry after 30 seconds",
		"event id aaaa8b7b6a307f426a94f8114701e7c8e774e7f9a47e2c2035db29a206321725 published",
	} {
		if got := Text(text); got != text {
			t.Fatalf("Text(%q) = %q, want unchanged", text, got)
		}
	}
}

func TestTextStripsControlCharacters(t *testing.T) {
	got := Text("relay\x1b[31m rejected\r\nline two")
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, '\r') {
		t.Fatalf("Text() = %q, want control characters stripped", got)
	}
}

func TestTextRedactsHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	path := filepath.Join(home, "private", "app.apk")
	got := Text("open " + path + ": no such file")
	if strings.Contains(got, home) || !strings.Contains(got, "~") {
		t.Fatalf("Text() = %q, want home directory redacted", got)
	}
}

func TestURLKeepsPublicForm(t *testing.T) {
	got := URL("wss://user:pass@relay.example/path?token=secret#fragment")
	if got != "wss://relay.example/path" {
		t.Fatalf("URL() = %q", got)
	}
	if got := URL("://not a url"); got != "" {
		t.Fatalf("URL(invalid) = %q, want empty", got)
	}
}

func TestDetailAndErrMessage(t *testing.T) {
	if got := Detail("   "); got != "" {
		t.Fatalf("Detail(blank) = %q, want empty", got)
	}
	if got := Detail("  cause here "); got != ": cause here" {
		t.Fatalf("Detail() = %q", got)
	}
	if got := ErrMessage(nil); got != "" {
		t.Fatalf("ErrMessage(nil) = %q, want empty", got)
	}
	if got := ErrMessage(errors.New("open " + testNsec)); strings.Contains(got, testNsec) {
		t.Fatalf("ErrMessage() = %q, leaked nsec", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short", 10); got != "short" {
		t.Fatalf("Truncate() = %q", got)
	}
	got := Truncate(strings.Repeat("a", 20), 10)
	if got != strings.Repeat("a", 10)+"…" {
		t.Fatalf("Truncate() = %q", got)
	}
}
