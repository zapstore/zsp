package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/config"
	"gopkg.in/yaml.v3"
)

// FDroid implements Source for F-Droid packages.
type FDroid struct {
	cfg           *config.Config
	packageID     string
	fdroidDataDir string // Local fdroiddata clone path (from FDROID_DATA_PATH)
	client        *http.Client
}

// NewFDroid creates a new F-Droid source.
func NewFDroid(cfg *config.Config) (*FDroid, error) {
	url := cfg.GetAPKSourceURL()
	packageID := config.GetFDroidPackageID(url)
	if packageID == "" {
		return nil, fmt.Errorf("invalid F-Droid URL: %s", url)
	}

	return &FDroid{
		cfg:           cfg,
		packageID:     packageID,
		fdroidDataDir: os.Getenv("FDROID_DATA_PATH"),
		client:        &http.Client{Timeout: 60 * time.Second},
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
	VersionCode int64  `json:"versionCode"`
	VersionName string `json:"versionName"`
	ApkName     string `json:"apkName"`
	Hash        string `json:"hash"`
	Size        int64  `json:"size"`
	MinSdkVersion int  `json:"minSdkVersion"`
	TargetSdkVersion int `json:"targetSdkVersion"`
	NativeCodes []string `json:"nativecode"`
}

// fdroidMetadata represents metadata from fdroiddata YAML files.
type fdroidMetadata struct {
	Categories    []string `yaml:"Categories"`
	License       string   `yaml:"License"`
	AuthorName    string   `yaml:"AuthorName"`
	AuthorEmail   string   `yaml:"AuthorEmail"`
	WebSite       string   `yaml:"WebSite"`
	SourceCode    string   `yaml:"SourceCode"`
	IssueTracker  string   `yaml:"IssueTracker"`
	Changelog     string   `yaml:"Changelog"`
	Donate        string   `yaml:"Donate"`
	Name          string   `yaml:"Name"`
	AutoName      string   `yaml:"AutoName"`
	Summary       string   `yaml:"Summary"`
	Description   string   `yaml:"Description"`
}

// FetchLatestRelease fetches the latest release from F-Droid.
func (f *FDroid) FetchLatestRelease(ctx context.Context) (*Release, error) {
	// Try to get version info from F-Droid index
	version, err := f.fetchLatestVersion(ctx)
	if err != nil {
		return nil, err
	}

	// Build APK download URL
	// Format: https://f-droid.org/repo/{packageId}_{versionCode}.apk
	apkURL := fmt.Sprintf("https://f-droid.org/repo/%s_%d.apk", f.packageID, version.VersionCode)
	apkName := fmt.Sprintf("%s_%d.apk", f.packageID, version.VersionCode)

	assets := []*Asset{
		{
			Name: apkName,
			URL:  apkURL,
			Size: version.Size,
		},
	}

	return &Release{
		Version: version.VersionName,
		Assets:  assets,
	}, nil
}

// fetchLatestVersion fetches the latest version info from F-Droid index.
func (f *FDroid) fetchLatestVersion(ctx context.Context) (*fdroidPackageVersion, error) {
	// Fetch the F-Droid index
	indexURL := "https://f-droid.org/repo/index-v1.json"

	req, err := http.NewRequestWithContext(ctx, "GET", indexURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch F-Droid index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("F-Droid index fetch failed with status %d", resp.StatusCode)
	}

	var index fdroidIndex
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return nil, fmt.Errorf("failed to parse F-Droid index: %w", err)
	}

	versions, ok := index.Packages[f.packageID]
	if !ok || len(versions) == 0 {
		return nil, fmt.Errorf("package %s not found in F-Droid", f.packageID)
	}

	// Find the latest version (highest versionCode)
	var latest *fdroidPackageVersion
	for i := range versions {
		if latest == nil || versions[i].VersionCode > latest.VersionCode {
			latest = &versions[i]
		}
	}

	return latest, nil
}

// Download downloads an APK from F-Droid.
func (f *FDroid) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	if asset.URL == "" {
		return "", fmt.Errorf("asset has no download URL")
	}

	// Create destination directory if needed
	if destDir == "" {
		destDir = os.TempDir()
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	destPath := filepath.Join(destDir, asset.Name)

	// Download the file
	req, err := http.NewRequestWithContext(ctx, "GET", asset.URL, nil)
	if err != nil {
		return "", err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
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

	// Wrap reader with progress tracking if callback provided
	var reader io.Reader = resp.Body
	if progress != nil && total > 0 {
		reader = &ProgressReader{
			Reader:     resp.Body,
			Total:      total,
			OnProgress: progress,
		}
	}

	_, err = io.Copy(file, reader)
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	asset.LocalPath = destPath
	return destPath, nil
}

// FetchMetadata fetches app metadata from fdroiddata.
func (f *FDroid) FetchMetadata(ctx context.Context) (*fdroidMetadata, error) {
	// Try local fdroiddata first
	if f.fdroidDataDir != "" {
		meta, err := f.fetchMetadataLocal()
		if err == nil {
			return meta, nil
		}
	}

	// Fall back to GitLab API
	return f.fetchMetadataRemote(ctx)
}

// fetchMetadataLocal reads metadata from local fdroiddata clone.
func (f *FDroid) fetchMetadataLocal() (*fdroidMetadata, error) {
	// Try YAML format first (newer)
	yamlPath := filepath.Join(f.fdroidDataDir, "metadata", f.packageID+".yml")
	data, err := os.ReadFile(yamlPath)
	if err == nil {
		var meta fdroidMetadata
		if err := yaml.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("failed to parse metadata YAML: %w", err)
		}
		return &meta, nil
	}

	// Try txt format (older)
	txtPath := filepath.Join(f.fdroidDataDir, "metadata", f.packageID+".txt")
	data, err = os.ReadFile(txtPath)
	if err != nil {
		return nil, fmt.Errorf("metadata file not found")
	}

	return f.parseTxtMetadata(string(data))
}

// fetchMetadataRemote fetches metadata from GitLab.
func (f *FDroid) fetchMetadataRemote(ctx context.Context) (*fdroidMetadata, error) {
	// Try YAML format
	url := fmt.Sprintf("https://gitlab.com/fdroid/fdroiddata/-/raw/master/metadata/%s.yml", f.packageID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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

// parseTxtMetadata parses the older .txt metadata format.
func (f *FDroid) parseTxtMetadata(content string) (*fdroidMetadata, error) {
	meta := &fdroidMetadata{}
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		if idx := strings.Index(line, ":"); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			value := strings.TrimSpace(line[idx+1:])

			switch key {
			case "Categories":
				meta.Categories = strings.Split(value, ",")
			case "License":
				meta.License = value
			case "Web Site":
				meta.WebSite = value
			case "Source Code":
				meta.SourceCode = value
			case "Issue Tracker":
				meta.IssueTracker = value
			case "Name":
				meta.Name = value
			case "Auto Name":
				meta.AutoName = value
			case "Summary":
				meta.Summary = value
			}
		}
	}

	return meta, nil
}

// PackageID returns the F-Droid package ID.
func (f *FDroid) PackageID() string {
	return f.packageID
}

// Helper to convert string to int64
func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}
