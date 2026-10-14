package netpolicy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"localhost.", true},
		{"127.0.0.1", true},
		{"127.8.9.10", true},
		{"::1", true},
		{"[::1]", true},
		{"::ffff:127.0.0.1", true},
		{"128.0.0.1", false},
		{"10.0.0.1", false},
		{"0.0.0.0", false},
		{"example.com", false},
		{"localhost.example.com", false},
		{"foo.localhost", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := IsLoopbackHost(tt.host); got != tt.want {
				t.Errorf("IsLoopbackHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{"https://github.com/user/repo", false},
		{"https://example.com:8443/path?q=1", false},
		{"http://github.com/user/repo", true},
		{"http://localhost/path", false},
		{"http://localhost:8080/path", false},
		{"http://127.0.0.1/path", false},
		{"http://127.5.5.5:9/path", false},
		{"http://[::1]/path", false},
		{"http://[::1]:8080/path", false},
		{"http://10.0.0.1/path", true},
		{"http://169.254.169.254/latest/meta-data", true},
		{"https://user:pass@example.com/", true},
		{"https://user@example.com/", true},
		{"https://example.com/#frag", true},
		{"ftp://example.com/", true},
		{"file:///etc/passwd", true},
		{"example.com/path", true},
		{"https://", true},
		{"https:///path", true},
		{"https://:443/path", true},
		{"", true},
		{"https://exa mple.com/", true},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := ValidateURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrUnsafeURL) {
				t.Errorf("ValidateURL(%q) error = %v, want ErrUnsafeURL", tt.url, err)
			}
		})
	}
}

func TestValidateURLErrorHidesCredentials(t *testing.T) {
	err := ValidateURL("https://user:hunter2@exa mple.com/")
	if err == nil {
		t.Fatal("ValidateURL() error = nil")
	}
	if got := err.Error(); strings.Contains(got, "hunter2") {
		t.Errorf("ValidateURL() error leaks credentials: %q", got)
	}
}

func TestCheckAddress(t *testing.T) {
	tests := []struct {
		addr          string
		allowLoopback bool
		wantErr       bool
	}{
		{"8.8.8.8", false, false},
		{"93.184.216.34", false, false},
		{"2606:4700:4700::1111", false, false},
		{"::ffff:8.8.8.8", false, false},
		{"100.63.255.255", false, false},
		{"100.128.0.1", false, false},
		{"172.15.0.1", false, false},
		{"172.32.0.1", false, false},

		{"0.0.0.0", false, true},
		{"0.1.2.3", false, true},
		{"::", false, true},
		{"10.0.0.1", false, true},
		{"172.16.0.1", false, true},
		{"172.31.255.255", false, true},
		{"192.168.1.1", false, true},
		{"fc00::1", false, true},
		{"fd12:3456::1", false, true},
		{"fd00:ec2::254", false, true},
		{"100.64.0.1", false, true},
		{"100.100.100.200", false, true},
		{"100.127.255.255", false, true},
		{"169.254.169.254", false, true},
		{"169.254.0.1", false, true},
		{"fe80::1", false, true},
		{"fe80::1%eth0", false, true},
		{"224.0.0.1", false, true},
		{"239.255.255.250", false, true},
		{"ff02::1", false, true},
		{"255.255.255.255", false, true},
		{"240.0.0.1", false, true},
		{"::ffff:10.0.0.1", false, true},
		{"::ffff:169.254.169.254", false, true},
		{"::ffff:127.0.0.1", false, true},

		{"127.0.0.1", false, true},
		{"127.1.2.3", false, true},
		{"::1", false, true},
		{"127.0.0.1", true, false},
		{"127.1.2.3", true, false},
		{"::1", true, false},
		{"::ffff:127.0.0.1", true, false},
		{"10.0.0.1", true, true},
		{"169.254.169.254", true, true},
		{"0.0.0.0", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			ip, err := netip.ParseAddr(tt.addr)
			if err != nil {
				t.Fatalf("ParseAddr(%q): %v", tt.addr, err)
			}
			err = CheckAddress(ip, tt.allowLoopback)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckAddress(%s, loopback=%v) error = %v, wantErr %v", tt.addr, tt.allowLoopback, err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrBlockedAddress) {
				t.Errorf("CheckAddress(%s) error = %v, want ErrBlockedAddress", tt.addr, err)
			}
		})
	}
	if err := CheckAddress(netip.Addr{}, true); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("CheckAddress(zero) error = %v, want ErrBlockedAddress", err)
	}
}

func TestDialerControl(t *testing.T) {
	tests := []struct {
		name    string
		dialed  string
		resolve string
		wantErr bool
	}{
		{"public name resolves public", "example.com:443", "93.184.216.34:443", false},
		{"public name resolves loopback (rebinding)", "example.com:443", "127.0.0.1:443", true},
		{"public name resolves loopback v6", "example.com:443", "[::1]:443", true},
		{"public name resolves metadata", "example.com:443", "169.254.169.254:443", true},
		{"public name resolves private", "example.com:443", "10.1.2.3:443", true},
		{"public name resolves mapped private", "example.com:443", "[::ffff:192.168.0.1]:443", true},
		{"localhost resolves loopback", "localhost:8080", "127.0.0.1:8080", false},
		{"localhost resolves loopback v6", "localhost:8080", "[::1]:8080", false},
		{"localhost resolves private", "localhost:8080", "10.0.0.1:8080", true},
		{"loopback literal", "127.0.0.1:8080", "127.0.0.1:8080", false},
		{"private literal", "10.0.0.1:80", "10.0.0.1:80", true},
		{"unparsable address", "example.com:443", "garbage", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dialer(tt.dialed).Control("tcp", tt.resolve, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Control(dial %q, resolved %q) error = %v, wantErr %v", tt.dialed, tt.resolve, err, tt.wantErr)
			}
		})
	}
}

func TestDialContextBlocksInternalLiterals(t *testing.T) {
	for _, addr := range []string{"10.0.0.1:80", "169.254.169.254:80", "[::ffff:192.168.1.1]:80", "100.64.0.1:80", "0.0.0.0:80"} {
		t.Run(addr, func(t *testing.T) {
			conn, err := DialContext(t.Context(), "tcp", addr)
			if err == nil {
				conn.Close()
				t.Fatalf("DialContext(%q) connected, want ErrBlockedAddress", addr)
			}
			if !errors.Is(err, ErrBlockedAddress) {
				t.Errorf("DialContext(%q) error = %v, want ErrBlockedAddress", addr, err)
			}
		})
	}
}

func TestTransportReachesLoopbackServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: NewTransport(), CheckRedirect: CheckRedirect}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get(%q): %v", srv.URL, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestGuardTarget(t *testing.T) {
	tests := []struct {
		addr    string
		wantErr bool
	}{
		{"example.com:443", false},
		{"8.8.8.8:443", false},
		{"10.0.0.1:443", true},
		{"[fd00::1]:443", true},
		{"127.0.0.1:9", false},
		{"localhost:9", false},
		{"noport", true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if err := GuardTarget(tt.addr); (err != nil) != tt.wantErr {
				t.Errorf("GuardTarget(%q) error = %v, wantErr %v", tt.addr, err, tt.wantErr)
			}
		})
	}
}

func TestCheckRedirect(t *testing.T) {
	mustReq := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		return &http.Request{URL: u}
	}
	tests := []struct {
		name    string
		from    string
		to      string
		hops    int
		wantErr bool
	}{
		{"https to https", "https://a.example/x", "https://b.example/y", 1, false},
		{"https to http remote", "https://a.example/x", "http://b.example/y", 1, true},
		{"remote to loopback http", "https://a.example/x", "http://127.0.0.1/y", 1, true},
		{"remote to localhost", "https://a.example/x", "https://localhost/y", 1, true},
		{"remote to loopback v6", "https://a.example/x", "http://[::1]/y", 1, true},
		{"loopback to loopback", "http://127.0.0.1:1/x", "http://127.0.0.1:2/y", 1, false},
		{"loopback to remote https", "http://127.0.0.1/x", "https://b.example/y", 1, false},
		{"userinfo", "https://a.example/x", "https://u:p@b.example/y", 1, true},
		{"fragment", "https://a.example/x", "https://b.example/y#f", 1, true},
		{"scheme", "https://a.example/x", "ftp://b.example/y", 1, true},
		{"too many", "https://a.example/x", "https://b.example/y", 10, true},
		{"nine hops is fine", "https://a.example/x", "https://b.example/y", 9, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			via := make([]*http.Request, tt.hops)
			for i := range via {
				via[i] = mustReq(tt.from)
			}
			err := CheckRedirect(mustReq(tt.to), via)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckRedirect(%s -> %s, %d hops) error = %v, wantErr %v", tt.from, tt.to, tt.hops, err, tt.wantErr)
			}
		})
	}
}

func TestLoopbackRedirectIsFollowed(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("internal"))
	}))
	t.Cleanup(local.Close)

	// Both hops are loopback, so the redirect is allowed.
	redirector := httptest.NewServer(http.RedirectHandler(local.URL, http.StatusFound))
	t.Cleanup(redirector.Close)
	client := &http.Client{Transport: NewTransport(), CheckRedirect: CheckRedirect}
	resp, err := client.Get(redirector.URL)
	if err != nil {
		t.Fatalf("loopback to loopback redirect: %v", err)
	}
	resp.Body.Close()
}
