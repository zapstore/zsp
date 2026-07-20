package workflow

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zapstore/zsp/internal/source"
)

type imageRoundTripper func(*http.Request) (*http.Response, error)

func (f imageRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDownloadRemoteImageFollowsGitHubBodyRedirect(t *testing.T) {
	const rawURL = "https://raw.githubusercontent.com/owner/app/main/icon.png"
	const resolvedURL = "https://github.com/owner/app/raw/refs/heads/main/icon.png"
	imageData := []byte("image data")
	requests := 0

	client := &http.Client{Transport: imageRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			if req.URL.String() != rawURL {
				t.Fatalf("initial URL = %q, want %q", req.URL, rawURL)
			}
			return &http.Response{
				StatusCode: http.StatusNotModified,
				Body:       io.NopCloser(strings.NewReader(resolvedURL)),
				Header:     make(http.Header),
			}, nil
		}
		if req.URL.String() != resolvedURL {
			t.Fatalf("redirect URL = %q, want %q", req.URL, resolvedURL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(imageData))),
			Header:     http.Header{"Content-Type": []string{"image/png"}},
		}, nil
	})}

	data, _, mimeType, err := downloadRemoteImageWithClient(context.Background(), rawURL, client)
	if err != nil {
		t.Fatalf("downloadRemoteImageWithClient() error = %v", err)
	}
	if string(data) != string(imageData) {
		t.Errorf("data = %q, want %q", data, imageData)
	}
	if mimeType != "image/png" {
		t.Errorf("mimeType = %q, want image/png", mimeType)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestDownloadRemoteImageResolvesGitHubRelativePointer(t *testing.T) {
	const rawURL = "https://raw.githubusercontent.com/owner/app/main/fastlane/icon.png"
	const resolvedURL = "https://raw.githubusercontent.com/owner/app/main/assets/icon.png"
	requests := 0

	client := &http.Client{Transport: imageRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("../assets/icon.png")),
				Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
			}, nil
		}
		if req.URL.String() != resolvedURL {
			t.Fatalf("resolved URL = %q, want %q", req.URL, resolvedURL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("image data")),
			Header:     http.Header{"Content-Type": []string{"image/png"}},
		}, nil
	})}

	if _, _, _, err := downloadRemoteImageWithClient(context.Background(), rawURL, client); err != nil {
		t.Fatalf("downloadRemoteImageWithClient() error = %v", err)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestDownloadRemoteImageRetriesThroughTorOn403(t *testing.T) {
	const imageURL = "https://example.com/icon.png"
	imageData := []byte("image data")
	torCalls := 0

	restoreTor := source.SetTorHTTPClientForTest(func() (*http.Client, error) {
		return &http.Client{Transport: imageRoundTripper(func(req *http.Request) (*http.Response, error) {
			torCalls++
			if req.URL.String() != imageURL {
				t.Fatalf("Tor URL = %q, want %q", req.URL, imageURL)
			}
			if auth := req.Header.Get("Authorization"); auth != "" {
				t.Fatalf("Tor Authorization = %q, want empty", auth)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(imageData))),
				Header:     http.Header{"Content-Type": []string{"image/png"}},
				Request:    req,
			}, nil
		})}, nil
	})
	t.Cleanup(restoreTor)

	client := &http.Client{Transport: imageRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader("forbidden")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	data, _, mimeType, err := downloadRemoteImageWithClient(context.Background(), imageURL, client)
	if err != nil {
		t.Fatalf("downloadRemoteImageWithClient() error = %v", err)
	}
	if string(data) != string(imageData) {
		t.Errorf("data = %q, want %q", data, imageData)
	}
	if mimeType != "image/png" {
		t.Errorf("mimeType = %q, want image/png", mimeType)
	}
	if torCalls != 1 {
		t.Errorf("Tor request count = %d, want 1", torCalls)
	}
}

func TestDownloadRemoteImageTorUnavailableOn403(t *testing.T) {
	restoreTor := source.SetTorHTTPClientForTest(func() (*http.Client, error) {
		return nil, errors.New("connection refused")
	})
	t.Cleanup(restoreTor)

	client := &http.Client{Transport: imageRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader("forbidden")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	_, _, _, err := downloadRemoteImageWithClient(context.Background(), "https://example.com/icon.png", client)
	if err == nil || !strings.Contains(err.Error(), "start Tor with SOCKS5 on 127.0.0.1:9050") {
		t.Fatalf("error = %v, want Tor unavailable message", err)
	}
}
