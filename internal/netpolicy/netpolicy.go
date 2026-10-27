// Package netpolicy is the outbound network policy for requests whose target
// comes from untrusted input: configuration URLs, API responses, and redirects.
//
// One URL rule, one dialer guard, and one redirect rule apply everywhere.
// Loopback targets stay reachable only when the URL itself names a loopback
// host, so local development and httptest servers work while DNS rebinding to
// 127.0.0.1 does not.
package netpolicy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/zapstore/zsp/v2/internal/sanitize"
)

var (
	// ErrUnsafeURL marks a URL the policy refuses to request.
	ErrUnsafeURL = errors.New("unsafe URL")
	// ErrBlockedAddress marks a connection refused because the resolved
	// address is internal.
	ErrBlockedAddress = errors.New("blocked address")
)

// maxRedirects is the redirect chain length at which CheckRedirect stops.
const maxRedirects = 10

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network"; routes to the local host on Linux
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT, includes some cloud metadata endpoints
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved, includes broadcast 255.255.255.255
}

// IsLoopbackHost reports whether host is "localhost", an address in
// 127.0.0.0/8, or ::1. Brackets around an IPv6 literal are accepted. A port
// is not.
func IsLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.Unmap().IsLoopback()
}

// ValidateURL applies the URL policy to rawURL: http or https scheme, a host,
// no userinfo, no fragment, and HTTPS unless the host is loopback.
func ValidateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		// The parse error quotes the raw input, which may carry credentials.
		return fmt.Errorf("%w: malformed URL: %s", ErrUnsafeURL, sanitize.Text(err.Error()))
	}
	return ValidateParsedURL(parsed)
}

// ValidateParsedURL is ValidateURL for an already parsed URL.
func ValidateParsedURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: missing URL", ErrUnsafeURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: URL must have http or https scheme, got %q", ErrUnsafeURL, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: URL must have a host", ErrUnsafeURL)
	}
	if u.User != nil {
		return fmt.Errorf("%w: URL must not contain userinfo", ErrUnsafeURL)
	}
	if u.Fragment != "" {
		return fmt.Errorf("%w: URL must not contain a fragment", ErrUnsafeURL)
	}
	if u.Scheme == "http" && !IsLoopbackHost(host) {
		return fmt.Errorf("%w: HTTPS is required for remote URLs (got http://%s)", ErrUnsafeURL, host)
	}
	return nil
}

// CheckRedirect is an http.Client.CheckRedirect that caps the chain, applies
// the URL policy to every hop, and refuses a hop from a remote host to a
// loopback host.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("%w: stopped after %d redirects", ErrUnsafeURL, maxRedirects)
	}
	if err := ValidateParsedURL(req.URL); err != nil {
		return err
	}
	if len(via) > 0 && !IsLoopbackHost(via[len(via)-1].URL.Hostname()) && IsLoopbackHost(req.URL.Hostname()) {
		return fmt.Errorf("%w: redirect from a remote host to loopback", ErrUnsafeURL)
	}
	return nil
}

// CheckAddress reports whether ip may be dialed. Loopback addresses are
// allowed only when allowLoopback is set. IPv4-mapped IPv6 addresses are
// checked as the IPv4 address they carry.
func CheckAddress(ip netip.Addr, allowLoopback bool) error {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlockedAddress)
	}
	if ip.IsLoopback() {
		if allowLoopback {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
	}
	if ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
		}
	}
	return nil
}

// DialContext dials addr after rejecting internal destinations. The check runs
// in the dialer's Control hook, so it sees the address after DNS resolution
// and covers every connection attempt, including Happy Eyeballs fallbacks.
// Loopback is allowed only when addr itself names a loopback host.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialer(addr).DialContext(ctx, network, addr)
}

// GuardTarget applies the address check to addr when its host is an IP
// literal. It is for dialers that hand hostnames to a remote resolver, such as
// a SOCKS5 proxy, where the local resolved address is not observable.
func GuardTarget(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBlockedAddress, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	return CheckAddress(ip, IsLoopbackHost(host))
}

func dialer(addr string) *net.Dialer {
	host, _, _ := net.SplitHostPort(addr)
	allowLoopback := IsLoopbackHost(host)
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ip, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrBlockedAddress, err)
			}
			parsed, err := netip.ParseAddr(ip)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrBlockedAddress, err)
			}
			return CheckAddress(parsed, allowLoopback)
		},
	}
}

// NewTransport returns an http.Transport whose dialer applies the address
// policy, with TLS 1.2 minimum and bounded connection pools. It never uses a
// proxy from the environment.
func NewTransport() *http.Transport {
	return &http.Transport{
		DialContext:         DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
}
