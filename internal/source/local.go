package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zapstore/zsp/internal/config"
)

// Local implements Source for local filesystem APKs.
type Local struct {
	pattern string // File path or glob pattern
	baseDir string // Base directory for relative paths
}

// NewLocal creates a new local source.
func NewLocal(pattern string) (*Local, error) {
	if pattern == "" {
		return nil, fmt.Errorf("local path is empty")
	}
	return &Local{pattern: pattern}, nil
}

// NewLocalWithBase creates a new local source with a base directory for relative paths.
func NewLocalWithBase(pattern, baseDir string) (*Local, error) {
	if pattern == "" {
		return nil, fmt.Errorf("local path is empty")
	}
	return &Local{pattern: pattern, baseDir: baseDir}, nil
}

// Type returns the source type.
func (l *Local) Type() config.SourceType {
	return config.SourceLocal
}

// FetchLatestRelease finds local APK files matching the pattern.
func (l *Local) FetchLatestRelease(ctx context.Context) (*Release, error) {
	// Resolve pattern relative to base directory if set
	pattern := l.pattern
	if l.baseDir != "" && !filepath.IsAbs(pattern) {
		pattern = filepath.Join(l.baseDir, pattern)
	}

	// Expand glob pattern
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid glob pattern %q: %w", l.pattern, err)
	}

	// If no glob characters, treat as literal path
	if len(matches) == 0 {
		// Check if it's a literal path that exists
		if _, err := os.Stat(pattern); err == nil {
			matches = []string{pattern}
		} else {
			return nil, fmt.Errorf("no APK files found matching %q", l.pattern)
		}
	}

	// Filter to only .apk files
	var apkFiles []string
	for _, m := range matches {
		if filepath.Ext(m) == ".apk" {
			apkFiles = append(apkFiles, m)
		}
	}

	if len(apkFiles) == 0 {
		return nil, fmt.Errorf("no APK files found matching %q", l.pattern)
	}

	// Create assets from found files
	assets := make([]*Asset, 0, len(apkFiles))
	for _, path := range apkFiles {
		absPath, err := filepath.Abs(path)
		if err != nil {
			absPath = path
		}

		fi, err := os.Stat(absPath)
		if err != nil {
			continue
		}

		assets = append(assets, &Asset{
			Name:      filepath.Base(absPath),
			LocalPath: absPath,
			Size:      fi.Size(),
		})
	}

	if len(assets) == 0 {
		return nil, fmt.Errorf("no accessible APK files found matching %q", l.pattern)
	}

	return &Release{
		Version: "local",
		Assets:  assets,
	}, nil
}

// Download returns the local path (no download needed for local files).
func (l *Local) Download(ctx context.Context, asset *Asset, destDir string, progress DownloadProgress) (string, error) {
	// progress callback is ignored for local files (no download needed)
	_ = progress

	if asset.LocalPath == "" {
		return "", fmt.Errorf("asset has no local path")
	}

	// Verify file still exists
	if _, err := os.Stat(asset.LocalPath); err != nil {
		return "", fmt.Errorf("local file not found: %w", err)
	}

	return asset.LocalPath, nil
}

