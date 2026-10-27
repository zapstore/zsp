package source

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/zapstore/zsp/v2/internal/netpolicy"
	"github.com/zapstore/zsp/v2/internal/sanitize"
	"golang.org/x/net/proxy"
)

const torSOCKSAddress = "127.0.0.1:9050"

// credentialHeaders carry forge tokens and must not follow a redirect to
// another host.
var credentialHeaders = []string{"Authorization", "PRIVATE-TOKEN"}

// newSecureHTTPClient creates an HTTP client for metadata and API calls:
// TLS 1.2 minimum, bounded connection pools, a total request timeout, the
// network policy at connect time and on every redirect, and one Tor SOCKS5
// retry on HTTP 403. tor may be nil. For large downloads use
// newDownloadHTTPClient, which has no total timeout.
func newSecureHTTPClient(timeout time.Duration, tor *torFallback) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     withTorFallback(netpolicy.NewTransport(), tor),
		CheckRedirect: validateRedirect,
	}
}

// newDownloadHTTPClient creates an HTTP client for large file downloads. It
// has no total request timeout: the caller wraps the body with a stall guard
// instead. It retries once through Tor on HTTP 403.
func newDownloadHTTPClient(tor *torFallback) *http.Client {
	transport := netpolicy.NewTransport()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{
		Transport:     withTorFallback(transport, tor),
		CheckRedirect: validateRedirect,
	}
}

// validateRedirect applies the network policy to a redirect hop and withholds
// credentials from any host other than the one the request started at.
func validateRedirect(req *http.Request, via []*http.Request) error {
	if err := netpolicy.CheckRedirect(req, via); err != nil {
		return err
	}
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		for _, name := range credentialHeaders {
			req.Header.Del(name)
		}
	}
	return nil
}

// validateDownloadURL applies the network URL policy to a request target. It
// covers explicit configuration values and runtime-derived ones: an API's
// download URL, a GitLab interstitial target, or a web extractor result.
func validateDownloadURL(rawURL string) error {
	return netpolicy.ValidateURL(rawURL)
}

// newRequest builds a bodyless request after validating rawURL. Every
// outbound request in this package starts here, so a URL taken from a remote
// response cannot bypass the policy.
func newRequest(ctx context.Context, method, rawURL string) (*http.Request, error) {
	if err := validateDownloadURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %s", sanitize.Text(err.Error()))
	}
	return req, nil
}

// newTorTransport returns a transport routed through a locally running Tor
// SOCKS5 proxy. The proxy resolves hostnames, so the dial guard applies to IP
// literals only; Tor exits refuse internal destinations on their own.
func newTorTransport() (http.RoundTripper, error) {
	dialer, err := proxy.SOCKS5("tcp", torSOCKSAddress, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("create Tor SOCKS5 dialer: %w", err)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("tor SOCKS5 dialer does not support context cancellation")
	}
	transport := netpolicy.NewTransport()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if err := netpolicy.GuardTarget(addr); err != nil {
			return nil, err
		}
		return contextDialer.DialContext(ctx, network, addr)
	}
	return transport, nil
}

// torFallback builds the Tor transport at most once and shares it among every
// client of one source. The zero value dials the local SOCKS5 proxy.
type torFallback struct {
	newTransport func() (http.RoundTripper, error)

	once      sync.Once
	transport http.RoundTripper
	err       error
}

func (t *torFallback) get() (http.RoundTripper, error) {
	t.once.Do(func() {
		build := t.newTransport
		if build == nil {
			build = newTorTransport
		}
		t.transport, t.err = build()
	})
	return t.transport, t.err
}

// torFallbackTransport replays a request once through Tor when the direct
// response is HTTP 403. The replay keeps the request's headers except
// credentials, so a Tor exit never sees a token. Redirects stay with the
// client, so its redirect policy applies to every hop.
type torFallbackTransport struct {
	base http.RoundTripper
	tor  *torFallback
}

func withTorFallback(base http.RoundTripper, tor *torFallback) http.RoundTripper {
	if tor == nil {
		tor = &torFallback{}
	}
	return &torFallbackTransport{base: base, tor: tor}
}

func (t *torFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		return resp, err
	}
	if req.Body != nil && req.Body != http.NoBody || netpolicy.IsLoopbackHost(req.URL.Hostname()) {
		return resp, nil // a body can't be replayed, and Tor can't reach loopback
	}

	torResp, torErr := t.viaTor(req)
	if torErr == nil {
		resp.Body.Close()
		return torResp, nil
	}
	if quotaExhausted(resp.Header) {
		// The direct response carries the rate-limit hints; callers need them.
		return resp, nil
	}
	resp.Body.Close()
	return nil, torErr
}

func (t *torFallbackTransport) viaTor(req *http.Request) (*http.Response, error) {
	transport, err := t.tor.get()
	if err != nil {
		return nil, fmt.Errorf("request received 403; retry through Tor unavailable (start Tor with SOCKS5 on %s): %w", torSOCKSAddress, err)
	}
	torReq := req.Clone(req.Context())
	for _, name := range credentialHeaders {
		torReq.Header.Del(name)
	}
	resp, err := transport.RoundTrip(torReq)
	if err != nil {
		return nil, fmt.Errorf("request received 403; retry through Tor at %s failed: %w", torSOCKSAddress, err)
	}
	return resp, nil
}

// DoWithTorFallback sends req with client. If the client's transport does not
// already provide Tor fallback and the response is 403, it replays the request
// once through the local Tor SOCKS5 proxy without credentials. The client's
// redirect policy is kept; a client without one gets the network policy. A nil
// client uses the standard API client.
func DoWithTorFallback(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = newSecureHTTPClient(30*time.Second, nil)
	} else if _, ok := client.Transport.(*torFallbackTransport); !ok {
		cloned := *client
		base := client.Transport
		if base == nil {
			guarded := netpolicy.NewTransport()
			guarded.DisableKeepAlives = true
			base = guarded
		}
		cloned.Transport = withTorFallback(base, nil)
		if cloned.CheckRedirect == nil {
			cloned.CheckRedirect = validateRedirect
		}
		client = &cloned
	}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	return resp, nil
}
