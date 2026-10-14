package source

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"

	"github.com/zapstore/zsp/internal/sanitize"
)

var (
	// ErrRateLimited marks a response that refused the request because of rate
	// limiting: HTTP 429, or a 403 that reports an exhausted quota.
	ErrRateLimited = errors.New("rate limited")

	// ErrChecksumMismatch marks a download whose SHA-256 differs from the
	// digest the source published.
	ErrChecksumMismatch = errors.New("checksum mismatch")

	errDownloadStalled = errors.New("download stalled")
	errTooLarge        = errors.New("response too large")
)

// HTTPStatusError is a response with an unexpected HTTP status. Callers branch
// on it with errors.As, and on rate limiting with errors.Is(err, ErrRateLimited).
type HTTPStatusError struct {
	StatusCode int
	// Service names the remote, such as "GitHub API", for messages.
	Service string
	// URL is the request URL without userinfo, query, or fragment.
	URL string
	// RetryAfter is the Retry-After header, in seconds or as an HTTP date.
	RetryAfter  string
	rateLimited bool
	hint        string
}

func (e *HTTPStatusError) Error() string {
	var msg string
	switch {
	case e.rateLimited && e.RetryAfter != "":
		msg = fmt.Sprintf("%s rate limited (status %d): retry after %s", e.Service, e.StatusCode, e.RetryAfter)
	case e.rateLimited:
		msg = fmt.Sprintf("%s rate limited (status %d)", e.Service, e.StatusCode)
	case e.StatusCode == http.StatusNotFound:
		msg = fmt.Sprintf("%s returned 404 Not Found: %s", e.Service, e.URL)
	case e.StatusCode == http.StatusForbidden:
		msg = fmt.Sprintf("%s access forbidden (status 403): you may be rate limited or IP blocked", e.Service)
	case e.Temporary() && e.StatusCode >= 500:
		msg = fmt.Sprintf("%s temporarily unavailable (status %d): try again later", e.Service, e.StatusCode)
	default:
		msg = fmt.Sprintf("%s returned status %d", e.Service, e.StatusCode)
	}
	if e.hint != "" {
		msg += ": " + e.hint
	}
	return msg
}

// Is reports ErrRateLimited for a rate-limited response.
func (e *HTTPStatusError) Is(target error) bool {
	return target == ErrRateLimited && (e.rateLimited || e.StatusCode == http.StatusTooManyRequests)
}

// Temporary reports whether the same request may succeed later: 408, 429, and
// any 5xx status.
func (e *HTTPStatusError) Temporary() bool {
	return e.StatusCode == http.StatusRequestTimeout || e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// checkHTTPStatus returns nil for a 200 response and an *HTTPStatusError for
// any other status. It is the one place response status becomes an error.
// service names the remote in messages, such as "F-Droid" or "GitHub API".
func checkHTTPStatus(resp *http.Response, service string) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	statusErr := &HTTPStatusError{
		StatusCode: resp.StatusCode,
		Service:    service,
		RetryAfter: resp.Header.Get("Retry-After"),
	}
	if resp.Request != nil && resp.Request.URL != nil {
		statusErr.URL = sanitize.URL(resp.Request.URL.String())
	}
	statusErr.rateLimited = resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode == http.StatusForbidden && quotaExhausted(resp.Header)
	return statusErr
}

// quotaExhausted reports a rate-limit signal on a 403: GitHub and Gitea send
// X-RateLimit-Remaining, GitLab sends RateLimit-Remaining, and GitHub's
// secondary limits send Retry-After.
func quotaExhausted(header http.Header) bool {
	return header.Get("X-RateLimit-Remaining") == "0" ||
		header.Get("RateLimit-Remaining") == "0" ||
		header.Get("Retry-After") != ""
}

// withHint appends actionable advice to a rate-limit status error. Other
// errors pass through unchanged.
func withHint(err error, hint string) error {
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) && statusErr.rateLimited {
		statusErr.hint = hint
	}
	return err
}

// isStatus reports whether err is an *HTTPStatusError with the given status.
func isStatus(err error, code int) bool {
	var statusErr *HTTPStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == code
}

// describeNotFound prefixes a 404 status error with what was being looked up,
// keeping the *HTTPStatusError reachable through errors.As.
func describeNotFound(err error, what string) error {
	if isStatus(err, http.StatusNotFound) {
		return fmt.Errorf("%s: %w", what, err)
	}
	return err
}

// IsTransient reports whether err is worth retrying later: a timeout, a reset,
// refused, or broken connection, a truncated or stalled download, or a 408,
// 429, or 5xx response. TLS and certificate verification failures, unsafe
// URLs, and checksum mismatches are never transient. Callers check context
// cancellation themselves.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Temporary()
	}
	if errors.Is(err, errDownloadStalled) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
