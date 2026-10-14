package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/zapstore/zsp/internal/config"
)

// FDroid implements Source for F-Droid compatible repositories.
// Supports: f-droid.org, IzzyOnDroid (apt.izzysoft.de), and other F-Droid repos.
type FDroid struct {
	repoInfo      *config.FDroidRepoInfo
	client        *http.Client
	cacheDir      string
	SkipHTTPCache bool // Set to true to bypass validator checks
	pendingCache  *fdroidCache
}

// fdroidCache stores the validators of the repo index for one package: the
// index ETag and Last-Modified for conditional requests, and a fingerprint of
// the chosen version. The index changes whenever any app in the repository
// does, so the fingerprint keeps an unrelated update from reading as new.
type fdroidCache struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	Fingerprint  string `json:"fingerprint,omitempty"`
}

// MaxFDroidIndexSize limits repository indexes while allowing large legitimate indexes.
const MaxFDroidIndexSize int64 = 100 * 1024 * 1024

// NewFDroid creates a new F-Droid source.
func NewFDroid(cfg *config.Config) (*FDroid, error) {
	return newFDroid(cfg, Options{})
}

func newFDroid(cfg *config.Config, opts Options) (*FDroid, error) {
	url := cfg.GetAPKSourceURL()
	repoInfo := config.GetFDroidRepoInfo(url)
	if repoInfo == nil {
		return nil, fmt.Errorf("invalid F-Droid URL: %s", url)
	}
	for _, repoURL := range []string{repoInfo.RepoURL, repoInfo.IndexURL} {
		if err := validateDownloadURL(repoURL); err != nil {
			return nil, fmt.Errorf("f-droid repository URL: %w", err)
		}
	}

	return &FDroid{
		repoInfo: repoInfo,
		client:   newDownloadHTTPClient(opts.tor()), // No total timeout, uses stall detection
		cacheDir: sourceCacheDir(opts.CacheDir, "fdroid"),
	}, nil
}

// Type returns the source type.
func (f *FDroid) Type() config.SourceType {
	return config.SourceFDroid
}

// fdroidIndex represents the F-Droid repo index.
type fdroidIndex struct {
	Packages map[string][]fdroidPackageVersion `json:"packages"`
}

// fdroidPackageVersion represents a package version in the index.
type fdroidPackageVersion struct {
	VersionCode      int64    `json:"versionCode"`
	VersionName      string   `json:"versionName"`
	ApkName          string   `json:"apkName"`
	Hash             string   `json:"hash"`
	HashType         string   `json:"hashType"`
	Size             int64    `json:"size"`
	MinSdkVersion    int      `json:"minSdkVersion"`
	TargetSdkVersion int      `json:"targetSdkVersion"`
	NativeCodes      []string `json:"nativecode"`
	Added            int64    `json:"added"` // Unix timestamp in milliseconds when version was added
}

// fdroidMetadata represents metadata from fdroiddata YAML files.
type fdroidMetadata struct {
	Categories   []string `yaml:"Categories"`
	License      string   `yaml:"License"`
	AuthorName   string   `yaml:"AuthorName"`
	AuthorEmail  string   `yaml:"AuthorEmail"`
	WebSite      string   `yaml:"WebSite"`
	SourceCode   string   `yaml:"SourceCode"`
	IssueTracker string   `yaml:"IssueTracker"`
	Changelog    string   `yaml:"Changelog"`
	Donate       string   `yaml:"Donate"`
	Name         string   `yaml:"Name"`
	AutoName     string   `yaml:"AutoName"`
	Summary      string   `yaml:"Summary"`
	Description  string   `yaml:"Description"`
}

// FetchLatestRelease fetches the latest release from an F-Droid compatible repository
// by downloading the shared repo index and selecting this package's best version.
// The index request is conditional. It returns ErrNotModified when the index
// is unchanged, or when it changed but this package's chosen build did not.
// Set SkipHTTPCache to bypass the check.
func (f *FDroid) FetchLatestRelease(ctx context.Context) (*Release, error) {
	version, err := f.fetchLatestVersion(ctx)
	if err != nil {
		return nil, err
	}
	return f.buildRelease(version), nil
}

// buildRelease constructs a Release from a parsed package version entry.
func (f *FDroid) buildRelease(version *fdroidPackageVersion) *Release {
	apkName := version.ApkName
	if apkName == "" {
		apkName = fmt.Sprintf("%s_%d.apk", f.repoInfo.PackageID, version.VersionCode)
	}
	apkURL := fmt.Sprintf("%s/%s", f.repoInfo.RepoURL, apkName)

	var createdAt time.Time
	if version.Added > 0 {
		createdAt = time.UnixMilli(version.Added)
	}

	return &Release{
		Version: version.VersionName,
		Assets: []*Asset{
			{
				Name:   apkName,
				URL:    apkURL,
				Size:   version.Size,
				SHA256: version.sha256(),
			},
		},
		CreatedAt: createdAt,
	}
}

// sha256 returns the published SHA-256 of the APK, or "" when the index
// carries no SHA-256 hash for it.
func (v *fdroidPackageVersion) sha256() string {
	if v.HashType != "" && v.HashType != "sha256" {
		return ""
	}
	return normalizeSHA256(v.Hash)
}

// fetchLatestVersion downloads the shared repo index, conditionally when a
// validator is cached, and selects the best version for this package.
func (f *FDroid) fetchLatestVersion(ctx context.Context) (*fdroidPackageVersion, error) {
	req, err := newRequest(ctx, http.MethodGet, f.repoInfo.IndexURL)
	if err != nil {
		return nil, err
	}
	var cache *fdroidCache
	if !f.SkipHTTPCache {
		cache = f.loadCache()
		if cache != nil && cache.ETag != "" {
			req.Header.Set("If-None-Match", cache.ETag)
		}
		if cache != nil && cache.LastModified != "" {
			req.Header.Set("If-Modified-Since", cache.LastModified)
		}
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch repo index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, ErrNotModified
	}
	if err := checkHTTPStatus(resp, "F-Droid repository"); err != nil {
		return nil, err
	}

	if resp.ContentLength > MaxFDroidIndexSize {
		return nil, fmt.Errorf("%w: repo index exceeds %d bytes", errTooLarge, MaxFDroidIndexSize)
	}
	// F-Droid indexes can be large and slow to download. Use both a generous
	// size cap and stall detection so an unresponsive or hostile mirror cannot
	// consume memory or leave the operation blocked indefinitely.
	stall := newStallReader(resp.Body, downloadStallTimeout)
	defer stall.Close()

	// Read the full body before decoding so a mid-stream failure surfaces as a
	// clear network error rather than a misleading parse error.
	body, err := readBounded(stall, MaxFDroidIndexSize)
	if err != nil {
		return nil, fmt.Errorf("read repo index: %w", err)
	}

	var index fdroidIndex
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("parse repo index: %w", err)
	}

	version, err := f.selectVersion(index.Packages)
	if err != nil {
		return nil, err
	}

	if !f.SkipHTTPCache {
		fingerprint := version.fingerprint()
		etag, lastModified := resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
		if etag != "" || lastModified != "" {
			f.pendingCache = &fdroidCache{ETag: etag, LastModified: lastModified, Fingerprint: fingerprint}
		}
		if cache != nil && cache.Fingerprint != "" && cache.Fingerprint == fingerprint {
			return nil, ErrNotModified
		}
	}
	return version, nil
}

// fingerprint identifies the build zsp would publish. The APK hash covers a
// rebuilt file; the version code and name cover an index without hashes.
func (v *fdroidPackageVersion) fingerprint() string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d\x00%s\x00%s\x00%s\x00%d", v.VersionCode, v.VersionName, v.ApkName, v.Hash, v.Size))
	return hex.EncodeToString(sum[:])
}

func (f *FDroid) cacheFilePath() string {
	sum := sha256.Sum256([]byte(f.repoInfo.IndexURL + "\x00" + f.repoInfo.PackageID))
	return filepath.Join(f.cacheDir, hex.EncodeToString(sum[:8])+".json")
}

func (f *FDroid) loadCache() *fdroidCache {
	data, err := os.ReadFile(f.cacheFilePath())
	if err != nil {
		return nil
	}
	var cache fdroidCache
	if err := json.Unmarshal(data, &cache); err != nil || (cache.ETag == "" && cache.LastModified == "") {
		return nil
	}
	return &cache
}

func (f *FDroid) saveCache(cache *fdroidCache) error {
	if err := os.MkdirAll(f.cacheDir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return os.WriteFile(f.cacheFilePath(), data, 0o644)
}

// CommitCache persists the validators from the last successful fetch.
func (f *FDroid) CommitCache() error {
	if f.pendingCache == nil {
		return nil
	}
	if err := f.saveCache(f.pendingCache); err != nil {
		return err
	}
	f.pendingCache = nil
	return nil
}

// ClearHTTPCache deletes the persisted validators so the next fetch is unconditional.
func (f *FDroid) ClearHTTPCache() error {
	f.pendingCache = nil
	err := os.Remove(f.cacheFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// selectVersion picks the best available version for this package from a packages map.
// Prefers arm64-v8a builds; falls back to architecture-independent builds.
func (f *FDroid) selectVersion(packages map[string][]fdroidPackageVersion) (*fdroidPackageVersion, error) {
	versions, ok := packages[f.repoInfo.PackageID]
	if !ok || len(versions) == 0 {
		return nil, fmt.Errorf("package %s not found in repository", f.repoInfo.PackageID)
	}

	// F-Droid publishes separate APKs for each architecture, each with a different
	// versionCode (e.g., arm64-v8a=25060102, x86=25060103, x86_64=25060104).
	// Filter to arm64-v8a first, then find the highest versionCode among those.
	var latest *fdroidPackageVersion
	for i := range versions {
		if hasArm64(versions[i].NativeCodes) {
			if latest == nil || versions[i].VersionCode > latest.VersionCode {
				latest = &versions[i]
			}
		}
	}

	// Fallback: if no arm64-v8a builds, look for architecture-independent builds
	// (pure Java/Kotlin apps with no native code).
	if latest == nil {
		for i := range versions {
			if len(versions[i].NativeCodes) == 0 {
				if latest == nil || versions[i].VersionCode > latest.VersionCode {
					latest = &versions[i]
				}
			}
		}
	}

	if latest == nil {
		return nil, fmt.Errorf("package %s has no arm64-v8a build available", f.repoInfo.PackageID)
	}

	return latest, nil
}

// hasArm64 checks if the native codes include arm64-v8a.
func hasArm64(nativeCodes []string) bool {
	for _, code := range nativeCodes {
		if code == "arm64-v8a" {
			return true
		}
	}
	return false
}

// Download downloads an APK from F-Droid, using the shared download
// pipeline (size limit, redirect validation, stall detection, bounded
// retries) and verifying the SHA-256 published in the repo index. F-Droid
// repositories require no authentication.
func (f *FDroid) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", errors.New("asset has no download URL")
	}

	destPath, err := prepareDownloadDest(destDir, asset.Name)
	if err != nil {
		return "", err
	}

	if err := downloadHTTP(ctx, f.client, asset.URL, destPath, asset.Size, asset.SHA256, nil, progress); err != nil {
		return "", err
	}

	asset.LocalPath = destPath
	return destPath, nil
}
