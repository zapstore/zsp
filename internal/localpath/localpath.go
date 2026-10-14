// Package localpath reads files named by configuration without leaving a base
// directory and without following symlinks.
package localpath

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrRejected marks a path that is outside the base directory, is a symlink,
// or traverses one. Missing files and read failures are reported as they are.
var ErrRejected = errors.New("local path not allowed")

// ErrTooLarge marks a file larger than the caller's limit.
var ErrTooLarge = errors.New("file too large")

// Open opens the regular file at p. A relative p is resolved against baseDir;
// an absolute p must lie inside it. No component of the path, including the
// file itself, may be a symlink.
func Open(baseDir, p string) (*os.File, error) {
	root, rel, err := confine(baseDir, p)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := walk(root, rel, p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrRejected, p)
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err == nil && !os.SameFile(info, opened) {
		err = fmt.Errorf("%w: %s changed while opening", ErrRejected, p)
	}
	if err != nil {
		_ = file.Close() // the file is unused after a failed check
		return nil, err
	}
	return file, nil
}

// ReadFile reads the file at p as Open does. It returns ErrTooLarge when the
// file is longer than limit bytes.
func ReadFile(baseDir, p string, limit int64) ([]byte, error) {
	file, err := Open(baseDir, p)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, p, limit)
	}
	return data, nil
}

// ReadDir lists the directory at p, sorted by name, under the same rules as
// Open. Entries that are symlinks are listed as symlinks; callers decide
// whether to skip them.
func ReadDir(baseDir, p string) ([]fs.DirEntry, error) {
	root, rel, err := confine(baseDir, p)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := walk(root, rel, p)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrRejected, p)
	}
	dir, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

// confine opens baseDir as a root and returns p relative to it.
func confine(baseDir, p string) (*os.Root, string, error) {
	if baseDir == "" {
		return nil, "", fmt.Errorf("%w: no base directory for %s", ErrRejected, p)
	}
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, "", err
	}
	rel := filepath.Clean(p)
	if filepath.IsAbs(rel) {
		rel, err = filepath.Rel(base, rel)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %s is outside the base directory", ErrRejected, p)
		}
	}
	if rel != "." && !filepath.IsLocal(rel) {
		return nil, "", fmt.Errorf("%w: %s is outside the base directory", ErrRejected, p)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", err
	}
	return root, rel, nil
}

// walk lstats every component of rel and rejects any symlink. It returns the
// info of the final component.
func walk(root *os.Root, rel, p string) (fs.FileInfo, error) {
	if rel == "." {
		return root.Lstat(".")
	}
	var (
		info fs.FileInfo
		err  error
		path string
	)
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err = root.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s is or passes through a symlink", ErrRejected, p)
		}
	}
	return info, nil
}
