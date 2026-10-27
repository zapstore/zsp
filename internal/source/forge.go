package source

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/zapstore/zsp/v2/internal/config"
)

// forgeAuthHeaders returns the authentication headers for a request to
// rawURL, or nil when no token applies. config.ForgeToken decides: a token
// goes only to the forge host it belongs to.
func forgeAuthHeaders(kind config.SourceType, rawURL string) map[string]string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	token := config.ForgeToken(kind, parsed.Host)
	if token == "" {
		return nil
	}
	switch kind {
	case config.SourceGitHub:
		return map[string]string{"Authorization": "Bearer " + token}
	case config.SourceGitea:
		return map[string]string{"Authorization": "token " + token}
	case config.SourceGitLab:
		return map[string]string{"PRIVATE-TOKEN": token}
	}
	return nil
}

// setForgeAuth attaches the forge token for req's host, if one applies.
func setForgeAuth(req *http.Request, kind config.SourceType) {
	for name, value := range forgeAuthHeaders(kind, req.URL.String()) {
		req.Header.Set(name, value)
	}
}

// newForgeRequest builds a GET request to a forge API and attaches the forge's token.
func newForgeRequest(ctx context.Context, kind config.SourceType, rawURL string) (*http.Request, error) {
	req, err := newRequest(ctx, http.MethodGet, rawURL)
	if err != nil {
		return nil, err
	}
	setForgeAuth(req, kind)
	return req, nil
}

// releaseFilter selects releases by tag or name. A nil filter selects all.
type releaseFilter struct {
	re *regexp.Regexp
}

// newReleaseFilter compiles release_filter once. An empty pattern yields a nil
// filter; an invalid one is an error rather than a filter that matches nothing.
func newReleaseFilter(pattern string) (*releaseFilter, error) {
	if pattern == "" {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid release filter %q: %w", pattern, err)
	}
	return &releaseFilter{re: re}, nil
}

// matches reports whether the tag or any of the names matches the filter.
func (f *releaseFilter) matches(tagName string, names ...string) bool {
	if f == nil {
		return true
	}
	if f.re.MatchString(tagName) {
		return true
	}
	for _, name := range names {
		if f.re.MatchString(name) {
			return true
		}
	}
	return false
}

// versionFromTag strips the conventional leading "v" from a release tag.
func versionFromTag(tag string) string {
	return strings.TrimPrefix(tag, "v")
}

// parseReleaseTime parses the first non-empty RFC 3339 timestamp. It returns
// the zero time when none is present or parseable.
func parseReleaseTime(values ...string) time.Time {
	for _, value := range values {
		if value == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, value); err == nil {
			return t
		}
		return time.Time{}
	}
	return time.Time{}
}
