package source

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

func TestAvailableSources(t *testing.T) {
	local := t.TempDir()
	if err := os.MkdirAll(filepath.Join(local, "fastlane", "metadata", "android", "en-US"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		cfg       *config.Config
		packageID string
		transport roundTripperFunc
		cancel    bool
		want      []string
		wantErr   error
	}{
		{
			name:      "local fastlane, github, and play store",
			cfg:       &config.Config{Repository: "https://github.com/example/app", BaseDir: local},
			packageID: "com.example.app",
			transport: func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "play.google.com" {
					return testResponse(http.StatusOK, `<html><span itemprop="name">Example</span></html>`), nil
				}
				return testResponse(http.StatusNotFound, "missing"), nil
			},
			want: []string{"fastlane", "github", "playstore"},
		},
		{
			name:      "github without fastlane or store listings",
			cfg:       &config.Config{Repository: "https://github.com/example/app", BaseDir: t.TempDir()},
			packageID: "com.example.app",
			transport: func(*http.Request) (*http.Response, error) {
				return testResponse(http.StatusNotFound, "missing"), nil
			},
			want: []string{"github"},
		},
		{
			name:      "local apk with no listings",
			cfg:       &config.Config{ReleaseSource: &config.ReleaseSource{LocalPath: "app.apk"}, BaseDir: t.TempDir()},
			packageID: "com.example.app",
			transport: func(*http.Request) (*http.Response, error) {
				return testResponse(http.StatusNotFound, "missing"), nil
			},
		},
		{
			name: "fdroid release keeps fdroid when the page lookup fails",
			cfg: &config.Config{ReleaseSource: &config.ReleaseSource{
				URL: "https://f-droid.org/packages/com.example.app",
			}},
			packageID: "com.example.app",
			transport: func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "f-droid.org" {
					return nil, errors.New("connection reset")
				}
				return testResponse(http.StatusNotFound, "missing"), nil
			},
			want: []string{"fdroid"},
		},
		{
			name:      "play store absence is not offered",
			cfg:       &config.Config{Repository: "https://github.com/example/app", BaseDir: t.TempDir()},
			packageID: "com.example.app",
			transport: func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "play.google.com" {
					return nil, errors.New("connection reset")
				}
				return testResponse(http.StatusNotFound, "missing"), nil
			},
			want: []string{"github"},
		},
		{
			name:      "fastlane lookup failure still offers fastlane for a forge",
			cfg:       &config.Config{Repository: "https://github.com/example/app", BaseDir: t.TempDir()},
			packageID: "com.example.app",
			transport: func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "api.github.com" {
					return nil, errors.New("connection reset")
				}
				return testResponse(http.StatusNotFound, "missing"), nil
			},
			want: []string{"fastlane", "github"},
		},
		{
			name:    "cancelled",
			cfg:     &config.Config{Repository: "https://github.com/example/app"},
			cancel:  true,
			wantErr: context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetcher := NewMetadataFetcherWithPackageID(test.cfg, test.packageID)
			if test.transport != nil {
				fetcher.client = &http.Client{Transport: test.transport}
			}
			ctx := t.Context()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := fetcher.AvailableSources(ctx)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("AvailableSources() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("AvailableSources() error = %v", err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("AvailableSources() = %v, want %v", got, test.want)
			}
		})
	}
}
