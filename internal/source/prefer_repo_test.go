package source

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

type stubReleaseSource struct {
	typ      config.SourceType
	release  *Release
	err      error
	fetched  int
	version  string
	download string
}

func (s *stubReleaseSource) Type() config.SourceType { return s.typ }

func (s *stubReleaseSource) FetchLatestRelease(context.Context) (*Release, error) {
	s.fetched++
	return s.release, s.err
}

func (s *stubReleaseSource) Download(context.Context, *Asset, string, DownloadProgress) (string, error) {
	return s.download, nil
}

func (s *stubReleaseSource) GetPublishedVersion() string { return s.version }

func TestPreferRepoSource_FetchLatestRelease(t *testing.T) {
	apkRelease := &Release{
		Version: "2.0.0",
		Assets:  []*Asset{{Name: "app.apk", URL: "https://example.com/app.apk"}},
	}
	noAPKRelease := &Release{
		Version: "2.0.0",
		Assets:  []*Asset{{Name: "app.dmg", URL: "https://example.com/app.dmg"}},
	}
	fdroidRelease := &Release{
		Version: "1.9.0",
		Assets:  []*Asset{{Name: "app_190.apk", URL: "https://f-droid.org/repo/app_190.apk"}},
	}

	tests := []struct {
		name               string
		repo               *stubReleaseSource
		primary            *stubReleaseSource
		ctx                context.Context
		wantVersion        string
		wantErr            error
		wantType           config.SourceType
		wantRepoFetches    int
		wantPrimaryFetches int
	}{
		{
			name: "repository with APKs wins",
			repo: &stubReleaseSource{
				typ:     config.SourceGitHub,
				release: apkRelease,
			},
			primary: &stubReleaseSource{
				typ:     config.SourceFDroid,
				release: fdroidRelease,
			},
			wantVersion:        "2.0.0",
			wantType:           config.SourceGitHub,
			wantRepoFetches:    1,
			wantPrimaryFetches: 0,
		},
		{
			name: "repository with no APKs falls back",
			repo: &stubReleaseSource{
				typ:     config.SourceGitHub,
				release: noAPKRelease,
			},
			primary: &stubReleaseSource{
				typ:     config.SourceFDroid,
				release: fdroidRelease,
			},
			wantVersion:        "1.9.0",
			wantType:           config.SourceFDroid,
			wantRepoFetches:    1,
			wantPrimaryFetches: 1,
		},
		{
			name: "repository with only unsigned APKs falls back",
			repo: &stubReleaseSource{
				typ: config.SourceGitHub,
				release: &Release{
					Version: "2.0.0",
					Assets:  []*Asset{{Name: "app-release-unsigned.apk", URL: "https://example.com/app-release-unsigned.apk"}},
				},
			},
			primary: &stubReleaseSource{
				typ:     config.SourceFDroid,
				release: fdroidRelease,
			},
			wantVersion:        "1.9.0",
			wantType:           config.SourceFDroid,
			wantRepoFetches:    1,
			wantPrimaryFetches: 1,
		},
		{
			name: "repository error falls back",
			repo: &stubReleaseSource{
				typ: config.SourceGitHub,
				err: fmt.Errorf("no releases found for owner/repo"),
			},
			primary: &stubReleaseSource{
				typ:     config.SourceFDroid,
				release: fdroidRelease,
			},
			wantVersion:        "1.9.0",
			wantType:           config.SourceFDroid,
			wantRepoFetches:    1,
			wantPrimaryFetches: 1,
		},
		{
			name: "repository parse error falls back",
			repo: &stubReleaseSource{
				typ: config.SourceGitHub,
				err: fmt.Errorf("failed to parse releases: unexpected EOF"),
			},
			primary: &stubReleaseSource{
				typ:     config.SourceFDroid,
				release: fdroidRelease,
			},
			wantVersion:        "1.9.0",
			wantType:           config.SourceFDroid,
			wantRepoFetches:    1,
			wantPrimaryFetches: 1,
		},
		{
			name: "ErrNotModified from repository does not fall back",
			repo: &stubReleaseSource{
				typ: config.SourceGitHub,
				err: ErrNotModified,
			},
			primary: &stubReleaseSource{
				typ:     config.SourceFDroid,
				release: fdroidRelease,
			},
			wantErr:            ErrNotModified,
			wantType:           config.SourceGitHub,
			wantRepoFetches:    1,
			wantPrimaryFetches: 0,
		},
		{
			name: "cancelled context is not swallowed",
			repo: &stubReleaseSource{
				typ: config.SourceGitHub,
				err: context.Canceled,
			},
			primary: &stubReleaseSource{
				typ:     config.SourceFDroid,
				release: fdroidRelease,
			},
			wantErr:            context.Canceled,
			wantType:           config.SourceFDroid, // active unset on cancel
			wantRepoFetches:    1,
			wantPrimaryFetches: 0,
		},
		{
			name: "pre-cancelled context fails before fetch",
			repo: &stubReleaseSource{
				typ:     config.SourceGitHub,
				release: apkRelease,
			},
			primary: &stubReleaseSource{
				typ:     config.SourceFDroid,
				release: fdroidRelease,
			},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			wantErr:            context.Canceled,
			wantType:           config.SourceFDroid,
			wantRepoFetches:    0,
			wantPrimaryFetches: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &preferRepoSource{primary: tt.primary, repo: tt.repo}
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}

			release, err := src.FetchLatestRelease(ctx)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("FetchLatestRelease() error = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("FetchLatestRelease() unexpected error: %v", err)
			} else if release == nil || release.Version != tt.wantVersion {
				t.Fatalf("FetchLatestRelease() version = %v, want %s", release, tt.wantVersion)
			}

			if src.Type() != tt.wantType {
				t.Errorf("Type() = %v, want %v", src.Type(), tt.wantType)
			}
			if tt.repo.fetched != tt.wantRepoFetches {
				t.Errorf("repo fetches = %d, want %d", tt.repo.fetched, tt.wantRepoFetches)
			}
			if tt.primary.fetched != tt.wantPrimaryFetches {
				t.Errorf("primary fetches = %d, want %d", tt.primary.fetched, tt.wantPrimaryFetches)
			}
		})
	}
}

func TestPreferRepoSource_FallbackReleaseAfterForgeSelected(t *testing.T) {
	apkRelease := &Release{
		Version: "2.0.0",
		Assets:  []*Asset{{Name: "app.apk", URL: "https://example.com/app.apk"}},
	}
	fdroidRelease := &Release{
		Version: "1.9.0",
		Assets:  []*Asset{{Name: "app_190.apk", URL: "https://f-droid.org/repo/app_190.apk"}},
	}
	repo := &stubReleaseSource{typ: config.SourceGitHub, release: apkRelease}
	primary := &stubReleaseSource{typ: config.SourceFDroid, release: fdroidRelease}
	src := &preferRepoSource{primary: primary, repo: repo}

	if _, err := src.FetchLatestRelease(context.Background()); err != nil {
		t.Fatalf("FetchLatestRelease: %v", err)
	}
	if src.Type() != config.SourceGitHub {
		t.Fatalf("Type() = %v, want github after forge select", src.Type())
	}

	fallback, err := src.FallbackRelease(context.Background())
	if err != nil {
		t.Fatalf("FallbackRelease: %v", err)
	}
	if fallback.Version != "1.9.0" {
		t.Fatalf("fallback version = %q, want 1.9.0", fallback.Version)
	}
	if src.Type() != config.SourceFDroid {
		t.Fatalf("Type() = %v, want fdroid after fallback", src.Type())
	}

	if _, err := src.FallbackRelease(context.Background()); !errors.Is(err, ErrNoFallback) {
		t.Fatalf("second FallbackRelease error = %v, want ErrNoFallback", err)
	}
}

func TestPreferRepoSource_DownloadUsesActive(t *testing.T) {
	repo := &stubReleaseSource{
		typ:      config.SourceGitHub,
		release:  &Release{Version: "1.0", Assets: []*Asset{{Name: "a.apk", URL: "https://x/a.apk"}}},
		download: "/tmp/repo.apk",
	}
	primary := &stubReleaseSource{
		typ:      config.SourceFDroid,
		release:  &Release{Version: "1.0", Assets: []*Asset{{Name: "b.apk", URL: "https://y/b.apk"}}},
		download: "/tmp/fdroid.apk",
	}
	src := &preferRepoSource{primary: primary, repo: repo}

	if _, err := src.FetchLatestRelease(context.Background()); err != nil {
		t.Fatalf("FetchLatestRelease: %v", err)
	}
	path, err := src.Download(context.Background(), &Asset{Name: "a.apk"}, "", nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if path != "/tmp/repo.apk" {
		t.Fatalf("Download path = %q, want repo path", path)
	}
}

func TestNewWithOptions_PrefersForgeRepository(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		wantPrefer bool
		wantType   config.SourceType
	}{
		{
			name: "fdroid with github repository wraps",
			cfg: &config.Config{
				Repository: "https://github.com/AntennaPod/AntennaPod",
				ReleaseSource: &config.ReleaseSource{
					URL: "https://f-droid.org/packages/de.danoeh.antennapod",
				},
			},
			wantPrefer: true,
			wantType:   config.SourceFDroid,
		},
		{
			name: "izzy with github repository wraps",
			cfg: &config.Config{
				Repository: "https://github.com/AntennaPod/AntennaPod",
				ReleaseSource: &config.ReleaseSource{
					URL: "https://apt.izzysoft.de/fdroid/index/apk/de.danoeh.antennapod",
				},
			},
			wantPrefer: true,
			wantType:   config.SourceFDroid,
		},
		{
			name: "fdroid with gitlab repository wraps",
			cfg: &config.Config{
				Repository: "https://gitlab.com/AuroraOSS/AuroraStore",
				ReleaseSource: &config.ReleaseSource{
					URL: "https://f-droid.org/packages/com.aurora.store",
				},
			},
			wantPrefer: true,
			wantType:   config.SourceFDroid,
		},
		{
			name: "fdroid with codeberg repository wraps",
			cfg: &config.Config{
				Repository: "https://codeberg.org/Freeyourgadget/Gadgetbridge",
				ReleaseSource: &config.ReleaseSource{
					URL: "https://f-droid.org/packages/nodomain.freeyourgadget.gadgetbridge",
				},
			},
			wantPrefer: true,
			wantType:   config.SourceFDroid,
		},
		{
			name: "web with github repository wraps",
			cfg: &config.Config{
				Repository: "https://github.com/AntennaPod/AntennaPod",
				ReleaseSource: &config.ReleaseSource{
					URL:         "https://example.com/download",
					IsWebSource: true,
					AssetURL:    "https://example.com/app-{version}.apk",
				},
			},
			wantPrefer: true,
			wantType:   config.SourceWeb,
		},
		{
			name: "fdroid without repository is plain",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{
					URL: "https://f-droid.org/packages/de.danoeh.antennapod",
				},
			},
			wantPrefer: false,
			wantType:   config.SourceFDroid,
		},
		{
			name: "fdroid with unknown repository is plain",
			cfg: &config.Config{
				Repository: "https://example.com/app",
				ReleaseSource: &config.ReleaseSource{
					URL: "https://f-droid.org/packages/de.danoeh.antennapod",
				},
			},
			wantPrefer: false,
			wantType:   config.SourceFDroid,
		},
		{
			name: "github release_source is not wrapped",
			cfg: &config.Config{
				Repository: "https://github.com/owner/app",
				ReleaseSource: &config.ReleaseSource{
					URL: "https://github.com/owner/app",
				},
			},
			wantPrefer: false,
			wantType:   config.SourceGitHub,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := NewWithOptions(tt.cfg, Options{})
			if err != nil {
				t.Fatalf("NewWithOptions() error: %v", err)
			}
			_, isPrefer := src.(*preferRepoSource)
			if isPrefer != tt.wantPrefer {
				t.Fatalf("preferRepoSource = %v, want %v (type %T)", isPrefer, tt.wantPrefer, src)
			}
			if src.Type() != tt.wantType {
				t.Fatalf("Type() before fetch = %v, want %v", src.Type(), tt.wantType)
			}
		})
	}
}

func TestForgeSourceFromRepository(t *testing.T) {
	tests := []struct {
		name     string
		repo     string
		wantNil  bool
		wantType config.SourceType
	}{
		{name: "empty", repo: "", wantNil: true},
		{name: "github", repo: "https://github.com/owner/app", wantType: config.SourceGitHub},
		{name: "gitlab", repo: "https://gitlab.com/owner/app", wantType: config.SourceGitLab},
		{name: "codeberg", repo: "https://codeberg.org/owner/app", wantType: config.SourceGitea},
		{name: "unknown", repo: "https://example.com/app", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Repository: tt.repo,
				ReleaseSource: &config.ReleaseSource{
					URL: "https://f-droid.org/packages/com.example",
				},
			}
			src, err := forgeSourceFromRepository(cfg, Options{})
			if err != nil {
				t.Fatalf("forgeSourceFromRepository() error: %v", err)
			}
			if tt.wantNil {
				if src != nil {
					t.Fatalf("expected nil source, got %T", src)
				}
				return
			}
			if src == nil {
				t.Fatal("expected forge source, got nil")
			}
			if src.Type() != tt.wantType {
				t.Fatalf("Type() = %v, want %v", src.Type(), tt.wantType)
			}
		})
	}
}
