package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/zapstore/zsp/internal/config"
	"gopkg.in/yaml.v3"
)

// fdroidIndexCache stores the ETag and parsed package versions for a repo index.
// Keyed on the index URL so all packages from the same repo share one cached file.
type fdroidIndexCache struct {
	ETag     string                          `json:"etag"`
	Packages map[string][]fdroidPackageVersion `json:"packages"`
}

// FDroid implements Source for F-Droid compatible repositories.
// Supports: f-droid.org, IzzyOnDroid (apt.izzysoft.de), and other F-Droid repos.
type FDroid struct {
	cfg      *config.Config
	repoInfo *config.FDroidRepoInfo
	client   *http.Client
	cacheDir string
	SkipCache bool

	// pending holds cache data from the last fetch, not yet committed to disk.
	pending *fdroidIndexCache
}

// NewFDroid creates a new F-Droid source.
func NewFDroid(cfg *config.Config) (*FDroid, error) {
	url := cfg.GetAPKSourceURL()
	repoInfo := config.GetFDroidRepoInfo(url)
	if repoInfo == nil {
		return nil, fmt.Errorf("invalid F-Droid URL: %s", url)
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	cacheDir = filepath.Join(cacheDir, "zsp", "fdroid")

	return &FDroid{
		cfg:      cfg,
		repoInfo: repoInfo,
		client:   &http.Client{Timeout: 120 * time.Second},
		cacheDir: cacheDir,
	}, nil
}

// cacheFilePath returns the path for the cached index file, keyed on the index URL.
func (f *FDroid) cacheFilePath() string {
	h := sha256.Sum256([]byte(f.repoInfo.IndexURL))
	return filepath.Join(f.cacheDir, hex.EncodeToString(h[:8])+".json")
}

// loadCache reads the cached index from disk.
func (f *FDroid) loadCache() *fdroidIndexCache {
	data, err := os.ReadFile(f.cacheFilePath())
	if err != nil {
		return nil
	}
	var cache fdroidIndexCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	return &cache
}

// saveCache writes the index cache to disk.
func (f *FDroid) saveCache(cache *fdroidIndexCache) error {
	if err := os.MkdirAll(f.cacheDir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return os.WriteFile(f.cacheFilePath(), data, 0644)
}

// CommitCache persists the pending cache to disk after successful publishing.
func (f *FDroid) CommitCache() error {
	if f.pending == nil {
		return nil
	}
	err := f.saveCache(f.pending)
	if err == nil {
		f.pending = nil
	}
	return err
}

// GetCachedRelease returns the cached release for this package if available.
func (f *FDroid) GetCachedRelease() *Release {
	cache := f.loadCache()
	if cache == nil {
		return nil
	}
	version, err := f.selectVersion(cache.Packages)
	if err != nil {
		return nil
	}
	return f.buildRelease(version)
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

// FetchLatestRelease fetches the latest release from an F-Droid compatible repository.
// For repos with a per-package API (f-droid.org), uses a lightweight API call.
// For others (IzzyOnDroid), fetches the shared index with ETag caching to avoid
// re-downloading the full 14–50 MB file when unchanged.
func (f *FDroid) FetchLatestRelease(ctx context.Context) (*Release, error) {
	version, err := f.fetchLatestVersion(ctx)
	if err != nil {
		return nil, err
	}
	return f.buildRelease(version), nil
}

// buildRelease constructs a Release from a parsed package version entry.
func (f *FDroid) buildRelease(version *fdroidPackageVersion) *Release {
	apkURL := fmt.Sprintf("%s/%s_%d.apk", f.repoInfo.RepoURL, f.repoInfo.PackageID, version.VersionCode)
	apkName := fmt.Sprintf("%s_%d.apk", f.repoInfo.PackageID, version.VersionCode)

	var createdAt time.Time
	if version.Added > 0 {
		createdAt = time.UnixMilli(version.Added)
	}

	return &Release{
		Version: version.VersionName,
		Assets: []*Asset{
			{
				Name: apkName,
				URL:  apkURL,
				Size: version.Size,
			},
		},
		CreatedAt: createdAt,
	}
}

// fetchLatestVersion fetches the latest version for this package from the shared repo
// index, using a disk-cached ETag to avoid re-downloading the full index when unchanged.
func (f *FDroid) fetchLatestVersion(ctx context.Context) (*fdroidPackageVersion, error) {
	return f.fetchLatestVersionFromIndex(ctx)
}

// fetchLatestVersionFromIndex fetches the latest version from the shared repo index,
// using a disk-cached ETag to avoid re-downloading the full 14–50 MB file when unchanged.
func (f *FDroid) fetchLatestVersionFromIndex(ctx context.Context) (*fdroidPackageVersion, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", f.repoInfo.IndexURL, nil)
	if err != nil {
		return nil, err
	}

	// Send If-None-Match if we have a cached ETag (unless skipping cache).
	var cached *fdroidIndexCache
	if !f.SkipCache {
		cached = f.loadCache()
		if cached != nil && cached.ETag != "" {
			req.Header.Set("If-None-Match", cached.ETag)
		}
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch repo index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && cached != nil {
		return f.selectVersion(cached.Packages)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("repo index fetch failed with status %d", resp.StatusCode)
	}

	// Read the full body before decoding so a mid-stream timeout surfaces as a
	// clear network error rather than a misleading "failed to parse" message.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("timed out reading repo index (index may be too large or server too slow): %w", err)
		}
		return nil, fmt.Errorf("failed to read repo index: %w", err)
	}

	var index fdroidIndex
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("failed to parse repo index: %w", err)
	}

	// Stage cache for commit after successful publish.
	etag := resp.Header.Get("ETag")
	if etag != "" {
		f.pending = &fdroidIndexCache{ETag: etag, Packages: index.Packages}
	}

	return f.selectVersion(index.Packages)
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

// Download downloads an APK from F-Droid.
// Uses a download cache to avoid re-downloading the same file.
func (f *FDroid) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", fmt.Errorf("asset has no download URL")
	}

	// Check download cache first
	if cachedPath := GetCachedDownload(asset.URL, asset.Name); cachedPath != "" {
		asset.LocalPath = cachedPath
		return cachedPath, nil
	}

	// Create destination directory if needed
	if destDir == "" {
		destDir = os.TempDir()
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Sanitize filename to prevent path traversal
	safeName := filepath.Base(asset.Name)
	destPath := filepath.Join(destDir, safeName)

	// Use download client (no total timeout — only stall detection)
	dlClient := newDownloadHTTPClient()

	req, err := http.NewRequestWithContext(ctx, "GET", asset.URL, nil)
	if err != nil {
		return "", err
	}

	resp, err := dlClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d: %s", resp.StatusCode, asset.URL)
	}

	// Use Content-Length from response if available, otherwise use asset size
	total := resp.ContentLength
	if total <= 0 {
		total = asset.Size
	}

	// Create destination file
	file, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Wrap body with stall timeout — fails only if no data received for 30s
	var reader io.Reader = &StallTimeoutReader{
		Reader:  resp.Body,
		Timeout: downloadStallTimeout,
	}

	// Wrap with progress tracking if callback provided
	if progress != nil && total > 0 {
		reader = &ProgressReader{
			Reader:     reader,
			Total:      total,
			OnProgress: progress,
		}
	}

	_, err = io.Copy(file, reader)
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	// Save to download cache (best-effort, ignore errors)
	if cachedPath, err := SaveToDownloadCache(asset.URL, asset.Name, destPath); err == nil {
		os.Remove(destPath)
		destPath = cachedPath
	}

	asset.LocalPath = destPath
	return destPath, nil
}

// FetchMetadata fetches app metadata from the repository's metadata source.
func (f *FDroid) FetchMetadata(ctx context.Context) (*fdroidMetadata, error) {
	if f.repoInfo.MetadataURL == "" {
		return nil, fmt.Errorf("no metadata URL available for this repository")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", f.repoInfo.MetadataURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata not found (status %d)", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var meta fdroidMetadata
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return &meta, nil
}

// PackageID returns the package ID.
func (f *FDroid) PackageID() string {
	return f.repoInfo.PackageID
}

// RepoInfo returns the repository information.
func (f *FDroid) RepoInfo() *config.FDroidRepoInfo {
	return f.repoInfo
}

// Helper to convert string to int64
func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}
