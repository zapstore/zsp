package source

import (
	"context"
	"errors"
	"fmt"

	"github.com/zapstore/zsp/internal/config"
)

// ErrNoFallback indicates the source has no alternate release backend, or is
// already using its fallback (configured release_source).
var ErrNoFallback = errors.New("no release source fallback available")

// preferRepoSource tries forge releases from config.Repository first, then
// falls back to a primary (non-forge) source such as F-Droid, Izzy, or web.
// Used when release_source is not a forge API source but repository is
// GitHub, GitLab, or Gitea/Forgejo/Codeberg and may publish APKs directly.
type preferRepoSource struct {
	primary Source // non-forge release_source (F-Droid, web, …)
	repo    Source // forge source built from repository URL
	active  Source // set after FetchLatestRelease chooses a backend
}

// ReleaseFallbacker is implemented by sources that can switch from a preferred
// backend (forge) to a configured fallback (F-Droid/Izzy/web) after the forge
// release proves unusable during selection, download, or APK parsing.
type ReleaseFallbacker interface {
	FallbackRelease(ctx context.Context) (*Release, error)
}

// newPreferRepoSource wraps primary with a forge source derived from cfg.Repository.
// Returns (primary, nil) when the repository is missing or not a forge release source.
func newPreferRepoSource(cfg *config.Config, opts Options, primary Source) (Source, error) {
	repoSrc, err := forgeSourceFromRepository(cfg, opts)
	if err != nil {
		return nil, err
	}
	if repoSrc == nil {
		return primary, nil
	}
	return &preferRepoSource{primary: primary, repo: repoSrc}, nil
}

// forgeSourceFromRepository builds a forge release source from cfg.Repository
// for any forge that supports native release APIs (GitHub, GitLab, Gitea).
// Returns (nil, nil) when repository is empty or not a forge release source.
func forgeSourceFromRepository(cfg *config.Config, opts Options) (Source, error) {
	if cfg == nil || cfg.Repository == "" {
		return nil, nil
	}
	repoType := config.DetectSourceType(cfg.Repository)
	if !config.IsForgeReleaseSource(repoType) {
		return nil, nil
	}

	// Point release_source at the repository so GetAPKSourceURL / constructors
	// resolve the forge, not the primary non-forge URL.
	forgeCfg := *cfg
	forgeCfg.ReleaseSource = &config.ReleaseSource{URL: cfg.Repository}
	// Use newConcreteSource to avoid re-entering prefer-repo wrapping.
	src, err := newConcreteSource(&forgeCfg, opts, repoType)
	if err != nil {
		return nil, fmt.Errorf("creating repository release source: %w", err)
	}
	return src, nil
}

func (p *preferRepoSource) Type() config.SourceType {
	if p.active != nil {
		return p.active.Type()
	}
	return p.primary.Type()
}

func (p *preferRepoSource) FetchLatestRelease(ctx context.Context) (*Release, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	release, err := p.repo.FetchLatestRelease(ctx)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, err
	case errors.Is(err, ErrNotModified):
		// Repository has releases and the local cache says they're unchanged.
		p.active = p.repo
		return nil, ErrNotModified
	case err == nil && release != nil && HasSelectableAPKs(release.Assets):
		p.active = p.repo
		return release, nil
	}

	// No usable forge release — fall back to the configured release_source.
	return p.FallbackRelease(ctx)
}

// FallbackRelease switches from the forge backend to the configured
// release_source (F-Droid/Izzy/web) and fetches that release. Callers use this
// when a forge release was selected but later fails selection, download, or APK parsing.
func (p *preferRepoSource) FallbackRelease(ctx context.Context) (*Release, error) {
	if p.active == p.primary {
		return nil, ErrNoFallback
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.active = p.primary
	return p.primary.FetchLatestRelease(ctx)
}

// FallbackRelease switches src to its fallback release backend when supported.
// Returns ErrNoFallback when src does not implement ReleaseFallbacker.
func FallbackRelease(ctx context.Context, src Source) (*Release, error) {
	f, ok := src.(ReleaseFallbacker)
	if !ok {
		return nil, ErrNoFallback
	}
	return f.FallbackRelease(ctx)
}

func (p *preferRepoSource) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	return p.selected().Download(ctx, asset, destDir, progress)
}

func (p *preferRepoSource) selected() Source {
	if p.active != nil {
		return p.active
	}
	return p.primary
}

// CommitCache implements CacheCommitter for the selected backend.
func (p *preferRepoSource) CommitCache() error {
	if c, ok := p.selected().(CacheCommitter); ok {
		return c.CommitCache()
	}
	return nil
}

// ClearCache implements CacheClearer for both backends.
func (p *preferRepoSource) ClearCache() error {
	var errs []error
	if c, ok := p.repo.(CacheClearer); ok {
		if err := c.ClearCache(); err != nil {
			errs = append(errs, err)
		}
	}
	if c, ok := p.primary.(CacheClearer); ok {
		if err := c.ClearCache(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// GetCachedRelease implements CachedReleaseProvider.
func (p *preferRepoSource) GetCachedRelease() *Release {
	if c, ok := p.selected().(CachedReleaseProvider); ok {
		if r := c.GetCachedRelease(); r != nil {
			return r
		}
	}
	// Before selection, prefer repo cache (matches fetch priority).
	if p.active == nil {
		if c, ok := p.repo.(CachedReleaseProvider); ok {
			if r := c.GetCachedRelease(); r != nil {
				return r
			}
		}
		if c, ok := p.primary.(CachedReleaseProvider); ok {
			return c.GetCachedRelease()
		}
	}
	return nil
}

// SetSkipCache implements CacheSkipper on both backends.
func (p *preferRepoSource) SetSkipCache(v bool) {
	if s, ok := p.repo.(CacheSkipper); ok {
		s.SetSkipCache(v)
	}
	if s, ok := p.primary.(CacheSkipper); ok {
		s.SetSkipCache(v)
	}
}

// GetPublishedVersion implements PublishedVersionReader.
func (p *preferRepoSource) GetPublishedVersion() string {
	if r, ok := p.selected().(PublishedVersionReader); ok {
		if v := r.GetPublishedVersion(); v != "" {
			return v
		}
	}
	if p.active == nil {
		if r, ok := p.repo.(PublishedVersionReader); ok {
			if v := r.GetPublishedVersion(); v != "" {
				return v
			}
		}
		if r, ok := p.primary.(PublishedVersionReader); ok {
			return r.GetPublishedVersion()
		}
	}
	return ""
}
