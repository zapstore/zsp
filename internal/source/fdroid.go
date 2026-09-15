package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/zapstore/zsp/internal/config"
	"gopkg.in/yaml.v3"
)

// FDroid implements Source for F-Droid compatible repositories.
// Supports: f-droid.org, IzzyOnDroid (apt.izzysoft.de), and other F-Droid repos.
type FDroid struct {
	cfg      *config.Config
	repoInfo *config.FDroidRepoInfo
	client   *http.Client
}

// MaxFDroidIndexSize limits repository indexes while allowing large legitimate indexes.
const MaxFDroidIndexSize int64 = 100 * 1024 * 1024

// NewFDroid creates a new F-Droid source.
func NewFDroid(cfg *config.Config) (*FDroid, error) {
	url := cfg.GetAPKSourceURL()
	repoInfo := config.GetFDroidRepoInfo(url)
	if repoInfo == nil {
		return nil, fmt.Errorf("invalid F-Droid URL: %s", url)
	}

	return &FDroid{
		cfg:      cfg,
		repoInfo: repoInfo,
		client:   newDownloadHTTPClient(), // No total timeout, uses stall detection
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
				Name: apkName,
				URL:  apkURL,
				Size: version.Size,
			},
		},
		CreatedAt: createdAt,
	}
}

// fetchLatestVersion fetches the latest version for this package by downloading
// the shared repo index fresh on every call.
func (f *FDroid) fetchLatestVersion(ctx context.Context) (*fdroidPackageVersion, error) {
	return f.fetchLatestVersionFromIndex(ctx)
}

// fetchLatestVersionFromIndex downloads the shared repo index and selects the
// best version for this package.
func (f *FDroid) fetchLatestVersionFromIndex(ctx context.Context) (*fdroidPackageVersion, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", f.repoInfo.IndexURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch repo index: %w", err)
	}
	defer resp.Body.Close()

	// Validate HTTP status
	if err := checkHTTPStatus(resp, "F-Droid repository"); err != nil {
		return nil, err
	}

	if resp.ContentLength > MaxFDroidIndexSize {
		return nil, fmt.Errorf("repo index exceeds maximum size of %d bytes", MaxFDroidIndexSize)
	}
	// F-Droid indexes can be large and slow to download. Use both a generous
	// size cap and stall detection so an unresponsive or hostile mirror cannot
	// consume memory or leave the operation blocked indefinitely.
	reader := &StallTimeoutReader{
		Reader:  io.LimitReader(resp.Body, MaxFDroidIndexSize+1),
		Timeout: downloadStallTimeout,
	}

	// Read the full body before decoding so a mid-stream timeout surfaces as a
	// clear network error rather than a misleading "failed to parse" message.
	body, err := io.ReadAll(reader)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("timed out reading repo index (no data received for 30s): %w", err)
		}
		return nil, fmt.Errorf("failed to read repo index: %w", err)
	}
	if int64(len(body)) > MaxFDroidIndexSize {
		return nil, fmt.Errorf("repo index exceeds maximum size of %d bytes", MaxFDroidIndexSize)
	}

	var index fdroidIndex
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("failed to parse repo index: %w", err)
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

// Download downloads an APK from F-Droid, using the shared DownloadHTTP
// pipeline (size limit, redirect validation, stall detection, bounded
// retries). F-Droid repositories require no authentication.
func (f *FDroid) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", fmt.Errorf("asset has no download URL")
	}

	destPath, err := prepareDownloadDest(destDir, asset.Name)
	if err != nil {
		return "", err
	}

	if err := DownloadHTTP(ctx, asset.URL, destPath, asset.Size, nil, progress); err != nil {
		return "", err
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

	// Validate HTTP status
	if err := checkHTTPStatus(resp, "F-Droid metadata"); err != nil {
		return nil, err
	}

	if resp.ContentLength > MaxRemoteDownloadSize {
		return nil, fmt.Errorf("metadata exceeds maximum size of %d bytes", MaxRemoteDownloadSize)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxRemoteDownloadSize+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}
	if int64(len(data)) > MaxRemoteDownloadSize {
		return nil, fmt.Errorf("metadata exceeds maximum size of %d bytes", MaxRemoteDownloadSize)
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
