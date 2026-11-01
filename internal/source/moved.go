package source

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// movedProbe reports a repository that its forge now serves under another
// path. A forge answers an API request for a renamed or transferred repository
// with a permanent redirect, which the HTTP client follows without telling
// anyone. GitHub's redirect target holds only the numeric repository id, so the
// new name comes from the repository's web page, which every forge redirects
// the same way.
//
// The probe is best effort: a failure here never fails the fetch. It reports
// at most once per source.
type movedProbe struct {
	onMoved func(repository string)
	// repoURL is the repository URL the source was configured with.
	repoURL string
	// canonical turns a URL on the same host into the repository path the
	// adapter would use for it, or "" when the URL is not a repository.
	canonical func(rawURL string) string
	// current is the repository path the source uses now.
	current string
	done    bool
}

// newMovedProbe returns nil, which observe accepts, when nobody listens.
func newMovedProbe(onMoved func(string), repoURL, current string, canonical func(string) string) *movedProbe {
	if onMoved == nil {
		return nil
	}
	return &movedProbe{onMoved: onMoved, repoURL: repoURL, current: current, canonical: canonical}
}

// observe inspects resp, the answer to one API request. When a permanent
// redirect led to it, observe asks the web page for the repository's new URL
// and reports it.
func (p *movedProbe) observe(ctx context.Context, client *http.Client, resp *http.Response) {
	if p == nil || p.done || resp == nil || client == nil {
		return
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
		return
	}
	if !followedPermanentRedirect(resp) {
		return
	}
	p.done = true

	moved := p.resolve(ctx, client)
	if moved != "" {
		p.onMoved(moved)
	}
}

// followedPermanentRedirect reports whether any hop that led to resp was a 301
// or 308.
func followedPermanentRedirect(resp *http.Response) bool {
	for r := resp; r != nil && r.Request != nil && r.Request.Response != nil; r = r.Request.Response {
		switch r.Request.Response.StatusCode {
		case http.StatusMovedPermanently, http.StatusPermanentRedirect:
			return true
		}
	}
	return false
}

// resolve returns the repository's new URL, or "" when the web page does not
// point to a different repository on the same host.
func (p *movedProbe) resolve(ctx context.Context, client *http.Client) string {
	req, err := newRequest(ctx, http.MethodGet, p.repoURL)
	if err != nil {
		return ""
	}
	probe := *client
	probe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := probe.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return ""
	}
	location, err := resp.Location()
	if err != nil || !strings.EqualFold(location.Host, req.URL.Host) {
		return ""
	}
	path := p.canonical(location.String())
	if path == "" || strings.EqualFold(path, p.current) {
		return ""
	}
	moved := url.URL{Scheme: req.URL.Scheme, Host: req.URL.Host, Path: "/" + path}
	return moved.String()
}
