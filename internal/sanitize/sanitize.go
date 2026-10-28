// Package sanitize removes private material from text before it is shown to
// users, written to logs, or returned to callers. It is the single redaction
// layer shared by the CLI, the public library API, and internal packages.
//
// Private key material and credentials must never appear in output. Everything
// else is preserved so errors stay actionable: this package removes only
// credential-bearing URL components, recorded secret values, key-shaped
// tokens, authorization header values, and control characters. Absolute
// filesystem paths are rewritten relative to the working directory. A path
// that cannot be made relative still hides the home directory as ~.
package sanitize

import (
	"net/url"
	"os"
	"path/filepath"
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
// shorter than 8 characters are silently ignored, so a short password or PIN
// is not redacted by registration and must not be relied on to stay out of
// text. Safe for concurrent use.
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
// assignments, and control characters. Absolute filesystem paths are
// rewritten relative to the working directory.
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

// redactHome rewrites absolute filesystem paths relative to the working
// directory. URLs are left alone. A path that cannot be made relative still
// hides the home directory as ~.
func redactHome(text string) string {
	if text == "" {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		return tildeHome(text)
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return tildeHome(text)
	}
	var b strings.Builder
	b.Grow(len(text))
	i := 0
	for i < len(text) {
		start, end, ok := nextAbsPath(text, i)
		if !ok {
			b.WriteString(text[i:])
			break
		}
		b.WriteString(text[i:start])
		raw := text[start:end]
		path, suffix := trimPathSuffix(raw)
		b.WriteString(displayPath(cwd, path))
		b.WriteString(suffix)
		i = end
	}
	return b.String()
}

// Path returns one filesystem path in the form Text shows. A URL is unchanged.
func Path(path string) string {
	if path == "" || strings.Contains(path, "://") {
		return path
	}
	return redactHome(path)
}

func displayPath(cwd, path string) string {
	rel, err := filepath.Rel(cwd, path)
	if err != nil || rel == "" {
		return tildeHome(path)
	}
	rel = filepath.ToSlash(rel)
	switch {
	case rel == ".":
		return "."
	case rel == ".." || strings.HasPrefix(rel, "../"):
		return rel
	case strings.HasPrefix(rel, "./"):
		return rel
	default:
		return "./" + rel
	}
}

func tildeHome(text string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return text
	}
	return strings.ReplaceAll(text, home, "~")
}

func nextAbsPath(text string, from int) (start, end int, ok bool) {
	for i := from; i < len(text); i++ {
		if !absPathAt(text, i) {
			continue
		}
		j := i + 1
		for j < len(text) && !isPathSpace(text[j]) {
			j++
		}
		if j == i+1 {
			continue
		}
		return i, j, true
	}
	return 0, 0, false
}

func absPathAt(text string, i int) bool {
	if i >= len(text) {
		return false
	}
	if text[i] == '/' || text[i] == '\\' {
		if i > 0 && (text[i-1] == ':' || text[i-1] == '/' || text[i-1] == '\\') {
			return false
		}
		return i == 0 || isPathBoundary(text[i-1])
	}
	if i+2 >= len(text) || text[i+1] != ':' {
		return false
	}
	c := text[i]
	if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
		return false
	}
	if text[i+2] != '\\' && text[i+2] != '/' {
		return false
	}
	return i == 0 || isPathBoundary(text[i-1])
}

func isPathBoundary(c byte) bool {
	return isPathSpace(c) || c == '(' || c == '"' || c == '\'' || c == '='
}

func isPathSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func trimPathSuffix(raw string) (path, suffix string) {
	end := len(raw)
	for end > 0 {
		switch raw[end-1] {
		case ':', ',', ';', ')', ']', '}', '"', '\'', '>':
			end--
		case '.':
			if end > 1 && raw[end-2] != '.' {
				end--
				continue
			}
			return raw[:end], raw[end:]
		default:
			return raw[:end], raw[end:]
		}
	}
	return raw[:end], raw[end:]
}
