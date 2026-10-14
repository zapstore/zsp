// Package source handles fetching APKs from various sources.
package source

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/config"
)

// unsupportedArchRegex matches APK filenames that explicitly indicate unsupported architectures.
// We only want arm64-v8a. Filter out x86, x86_64 (Intel/AMD) and armeabi/armeabi-v7a (32-bit ARM).
var unsupportedArchRegex = regexp.MustCompile(`(?i)(^|[-_.])(x86_64|x86|armeabi-v7a|armeabi)([-_.]|$)`)

// defaultMaxReleases is how many recent releases to scan for a usable APK
// when the caller does not set a limit. Some repos publish desktop and
// mobile separately.
const defaultMaxReleases = 10

// maxReleasesPage is one GitHub or GitLab page. Gitea may return fewer.
const maxReleasesPage = 100

// Asset represents a downloadable APK asset.
type Asset struct {
	Name        string // Filename
	URL         string // Download URL (empty for local files)
	Size        int64  // Size in bytes (0 if unknown)
	SHA256      string // Expected SHA-256 as lowercase hex, when the source publishes one; Download verifies it
	LocalPath   string // Local file path (set after download or for local sources)
	ContentType string // MIME type (if known)
	ExcludeURL  bool   // If true, don't include URL in event (use Blossom URL only)
}

// Release represents a release containing one or more APK assets.
type Release struct {
	Version    string    // Version string (e.g., "1.2.3" or "v1.2.3")
	TagName    string    // Git tag name (if applicable)
	Name       string    // Human-readable release title (if applicable)
	Changelog  string    // Release notes/changelog
	Assets     []*Asset  // Available APK assets
	PreRelease bool      // Whether this is a pre-release
	URL        string    // Release page URL (e.g., https://github.com/user/repo/releases/tag/v1.0)
	CreatedAt  time.Time // Release creation/publish date (zero if unknown)
}

// Source is the interface for APK sources.
type Source interface {
	// Type returns the source type.
	Type() config.SourceType

	// FetchLatestRelease fetches the latest release information.
	FetchLatestRelease(ctx context.Context) (*Release, error)

	// Download downloads an asset and returns the local path.
	// For local sources, this may just return the existing path.
	// The optional progress callback is called during download.
	Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error)
}

// Options contains options for creating a source.
type Options struct {
	// BaseDir is the base directory for resolving relative paths.
	// Typically the directory containing the config file.
	BaseDir string

	// IncludePreReleases includes pre-releases when fetching the latest release (--pre-release).
	IncludePreReleases bool

	// SkipHTTPCache bypasses ETag/Last-Modified checks and does not persist a
	// new HTTP cache entry.
	SkipHTTPCache bool

	// MaxReleases is how many recent releases to scan for a usable APK.
	// Zero uses defaultMaxReleases. Values outside 1..maxReleasesPage are rejected.
	MaxReleases int

	// CacheDir is the directory holding HTTP validator caches. Empty uses the
	// user cache directory.
	CacheDir string

	// TorTransport builds the transport used for the one-shot Tor retry on
	// HTTP 403. It is called at most once per source. Nil dials the local
	// SOCKS5 proxy.
	TorTransport func() (http.RoundTripper, error)
}

// tor returns the Tor fallback shared by every client of one source.
func (o Options) tor() *torFallback {
	return &torFallback{newTransport: o.TorTransport}
}

// releasesToCheck resolves the scan size. Zero means the default.
func releasesToCheck(n int) (int, error) {
	if n == 0 {
		return defaultMaxReleases, nil
	}
	if n < 1 || n > maxReleasesPage {
		return 0, fmt.Errorf("max releases must be from 1 to %d", maxReleasesPage)
	}
	return n, nil
}

// releaseScanLimit is the scan size stored on a forge client. Zero means the
// default, for clients constructed without NewWithOptions.
func releaseScanLimit(n int) int {
	if n <= 0 {
		return defaultMaxReleases
	}
	return n
}

// ErrNotModified is returned when a cached ETag shows the release is unchanged.
var ErrNotModified = errors.New("release not modified")

// CacheCommitter persists pending ETag data after a successful Fetch.
type CacheCommitter interface {
	CommitCache() error
}

// CacheClearer removes a persisted HTTP cache so the next Fetch is unconditional.
type CacheClearer interface {
	ClearHTTPCache() error
}

// sourceCacheDir returns the validator cache directory for one source kind
// under base, or under the user cache directory when base is empty.
func sourceCacheDir(base, kind string) string {
	if base == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = os.TempDir()
		}
		base = dir
	}
	return filepath.Join(base, "zsp", kind)
}

// NewWithOptions creates a new source with options.
func NewWithOptions(cfg *config.Config, opts Options) (Source, error) {
	limit, err := releasesToCheck(opts.MaxReleases)
	if err != nil {
		return nil, err
	}
	sourceType := cfg.GetSourceType()

	switch sourceType {
	case config.SourceLocal:
		localPath := ""
		if cfg.ReleaseSource != nil {
			localPath = cfg.ReleaseSource.LocalPath
		}
		return NewLocalWithBase(localPath, opts.BaseDir)
	case config.SourceGitHub:
		gh, err := newGitHub(cfg, opts)
		if err != nil {
			return nil, err
		}
		gh.IncludePreReleases = opts.IncludePreReleases
		gh.SkipHTTPCache = opts.SkipHTTPCache
		gh.maxReleases = limit
		return gh, nil
	case config.SourceGitLab:
		gl, err := newGitLab(cfg, opts)
		if err != nil {
			return nil, err
		}
		gl.IncludePreReleases = opts.IncludePreReleases
		gl.maxReleases = limit
		return gl, nil
	case config.SourceGitea:
		gt, err := newGitea(cfg, opts)
		if err != nil {
			return nil, err
		}
		gt.IncludePreReleases = opts.IncludePreReleases
		gt.maxReleases = limit
		return gt, nil
	case config.SourceFDroid:
		fd, err := newFDroid(cfg, opts)
		if err != nil {
			return nil, err
		}
		fd.SkipHTTPCache = opts.SkipHTTPCache
		return fd, nil
	case config.SourceWeb:
		web, err := newWeb(cfg, opts)
		if err != nil {
			return nil, err
		}
		web.SkipHTTPCache = opts.SkipHTTPCache
		return web, nil
	default:
		return nil, fmt.Errorf("unsupported source type: %s", sourceType)
	}
}

// HasUnsupportedArchitecture returns true if the filename explicitly indicates
// an unsupported architecture (x86, x86_64, etc.).
func HasUnsupportedArchitecture(filename string) bool {
	// Only check APK files
	if !strings.HasSuffix(strings.ToLower(filename), ".apk") {
		return false
	}
	return unsupportedArchRegex.MatchString(filename)
}

// apkMediaType is the MIME type of an Android package. A response with this
// type is an APK even when the URL path ends in another suffix, such as .bin.
const apkMediaType = "application/vnd.android.package-archive"

// IsAPKContentType reports whether contentType is an Android package,
// ignoring parameters such as charset.
func IsAPKContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(contentType)
	}
	return strings.EqualFold(mediaType, apkMediaType)
}

// IsAPKURL checks if a URL points to an APK file.
// Properly handles URLs with query parameters.
func IsAPKURL(rawURL string) bool {
	u := strings.ToLower(rawURL)
	// Strip query parameters
	if idx := strings.Index(u, "?"); idx >= 0 {
		u = u[:idx]
	}
	return strings.HasSuffix(u, ".apk")
}

// IsAPKAsset checks if an asset (by name or URL) is an APK file.
func IsAPKAsset(name, url string) bool {
	if strings.HasSuffix(strings.ToLower(name), ".apk") {
		return true
	}
	return IsAPKURL(url)
}

// IsAPK reports whether the asset is an APK by media type, filename, or URL.
// Media type wins when a CDN path does not end in .apk.
func (a *Asset) IsAPK() bool {
	if a == nil {
		return false
	}
	return IsAPKContentType(a.ContentType) || IsAPKAsset(a.Name, a.URL)
}

// HasValidAPKs returns true if the assets contain at least one APK file.
// Used to determine if a release is a valid mobile release (vs desktop-only).
func HasValidAPKs(assets []*Asset) bool {
	for _, asset := range assets {
		if asset.IsAPK() {
			return true
		}
	}
	return false
}
