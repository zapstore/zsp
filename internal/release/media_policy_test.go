package release

import (
	"net/url"
	"testing"
)

func TestAllowedRemoteURL(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"https://example.com/icon.png", true},
		{"http://example.com/icon.png", false},
		{"http://localhost:8080/icon.png", true},
		{"http://127.0.0.2/icon.png", true},
		{"http://[::1]:8080/icon.png", true},
		{"https://user:pass@example.com/icon.png", false},
		{"https://example.com/icon.png#frag", false},
		{"ftp://example.com/icon.png", false},
		{"https:///icon.png", false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			parsed, err := url.Parse(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := allowedRemoteURL(parsed); got != tt.want {
				t.Errorf("allowedRemoteURL(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
	if allowedRemoteURL(nil) {
		t.Error("allowedRemoteURL(nil) = true, want false")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "127.5.5.5": true, "::1": true, "example.com": false, "10.0.0.1": false, "": false} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}
