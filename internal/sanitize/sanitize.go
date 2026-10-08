// Package sanitize removes private material from text before it is shown to
// users, written to logs, or returned to callers. It is the single redaction
// layer shared by the CLI, the public library API, and internal packages.
//
// Private key material and credentials must never appear in output. Everything
// else is preserved so errors stay actionable: this package removes only
// credential-bearing URL components, recorded secret values, key-shaped
// tokens, authorization header values, control characters, and the user's home
// directory.
package sanitize

import (
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	redacted = "[redacted]"

	// minSecretLength is the shortest registered value that gets redacted.
	// Shorter values are ignored so ordinary text is not mangled.
	minSecretLength = 8
)

var (
	secretsMu sync.RWMutex
	secrets   []string // longest first
)

// Register records values that must never appear in sanitized text: private
// keys, signer secrets, API tokens, passwords, and similar material. Values
// shorter than 8 characters are ignored. Safe for concurrent use.
func Register(values ...string) {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for _, value := range values {
		if len(value) < minSecretLength {
			continue
		}
		known := false
		for _, existing := range secrets {
			if existing == value {
				known = true
				break
			}
		}
		if known {
			continue
		}
		secrets = append(secrets, value)
	}
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
}

// urlPattern matches absolute URLs in arbitrary text. Quotes are excluded
// because error text often quotes a URL.
var urlPattern = regexp.MustCompile(`\w+://[^\s"']+`)

// nsecPattern matches NIP-19 private keys, the one key shape that is always
// recognizable. Registered values cover the shapes that are not.
var nsecPattern = regexp.MustCompile(`(?i)nsec1[0-9a-z]+`)

// authSchemePattern matches authorization header values such as
// "Nostr <base64>" and "Bearer <token>".
var authSchemePattern = regexp.MustCompile(`(?i)\b(bearer|nostr)\s+[A-Za-z0-9+/_=.:-]{16,}`)

// assignmentPattern matches credential assignments without a URL scheme, such
// as "token=...", "password: ...", or "Authorization: Bearer ...". The value
// may carry an authorization scheme, which is kept in front of the redaction.
var assignmentPattern = regexp.MustCompile(`(?i)\b(secret|token|password|passwd|passphrase|api[ _-]?key|private[_-]?key|authorization)\b(\s*[=:]\s*)((?:bearer|nostr)\s+)?\S+`)

// yamlValuePattern matches the scalar a yaml.v3 type error quotes, which can
// be a mistyped secret ("cannot unmarshal !!str `secret...` into ...").
var yamlValuePattern = regexp.MustCompile("(cannot unmarshal \\S+) `[^`]*`")

// Text returns text with private material redacted: registered secrets, URL
// credentials/query/fragment, nsec keys, authorization values, credential
// assignments, control characters, and the user's home directory.
func Text(text string) string {
	if text == "" {
		return ""
	}
	text = redactRegistered(text)
	text = urlPattern.ReplaceAllStringFunc(text, func(candidate string) string {
		if public := URL(candidate); public != "" {
			return public
		}
		return "URL"
	})
	text = nsecPattern.ReplaceAllString(text, redacted)
	text = assignmentPattern.ReplaceAllString(text, "${1}${2}${3}"+redacted)
	text = yamlValuePattern.ReplaceAllString(text, "$1 [redacted]")
	text = authSchemePattern.ReplaceAllString(text, "${1} "+redacted)
	text = stripControl(text)
	return redactHome(text)
}

// URL returns raw with userinfo, query, and fragment removed: the form safe
// to show or publish. It returns "" when raw is not parseable.
func URL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// Detail returns ": " followed by the sanitized text, or "" when there is
// nothing left to show.
func Detail(text string) string {
	text = strings.TrimSpace(Text(text))
	if text == "" {
		return ""
	}
	return ": " + text
}

// ErrMessage returns the sanitized message of err, or "" for nil.
func ErrMessage(err error) string {
	if err == nil {
		return ""
	}
	return Text(err.Error())
}

// Truncate limits text to limit runes, appending an ellipsis when it cuts.
// It is meant for untrusted server text, not for locally built messages.
func Truncate(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimRight(string(runes[:limit]), " ") + "…"
}

func redactRegistered(text string) string {
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, redacted)
	}
	return text
}

// stripControl replaces control characters (except newline and tab) so
// untrusted text cannot inject terminal escapes or fake log lines.
func stripControl(text string) string {
	if !strings.ContainsFunc(text, isControl) {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if isControl(r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isControl(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return r < 0x20 || r == 0x7f
}

func redactHome(text string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return text
	}
	return strings.ReplaceAll(text, home, "~")
}
