package source

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zapstore/zsp/v2/internal/config"
	"github.com/zapstore/zsp/v2/internal/localpath"
)

const fastlaneMetadataPath = "fastlane/metadata/android"

func hasLocalFastlane(baseDir string) bool {
	if baseDir == "" {
		return false
	}
	_, err := localpath.ReadDir(baseDir, fastlaneMetadataPath)
	return err == nil
}

// errFastlaneUnavailable indicates that a repository does not contain Fastlane
// metadata. It is distinct from a failed request so callers can safely select
// the native repository metadata fallback only when Fastlane is absent.
var errFastlaneUnavailable = errors.New("fastlane metadata not found")

type fastlaneEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"`
	DownloadURL string `json:"download_url"`
}

// fetchFastlaneMetadata fetches Android store metadata maintained alongside the
// source repository. GitHub, GitLab, and Gitea-compatible forges (Codeberg,
// Forgejo, self-hosted Gitea) are supported.
func (f *MetadataFetcher) fetchFastlaneMetadata(ctx context.Context) (*AppMetadata, error) {
	if f.cfg.BaseDir != "" {
		if metadata, err := f.fetchLocalFastlaneMetadata(); err == nil {
			return metadata, nil
		} else if !errors.Is(err, errFastlaneUnavailable) {
			return nil, err
		}
	}
	switch repositoryMetadataHost(f.cfg) {
	case config.SourceGitHub:
		return f.fetchGitHubFastlaneMetadata(ctx)
	case config.SourceGitLab:
		return f.fetchGitLabFastlaneMetadata(ctx)
	case config.SourceGitea:
		return f.fetchGiteaFastlaneMetadata(ctx)
	default:
		return nil, fmt.Errorf("%w: repository must be GitHub, GitLab, or Gitea/Codeberg", errFastlaneUnavailable)
	}
}

// fetchLocalFastlaneMetadata reads the Fastlane tree under the base directory.
// Symlinked directories and files are not followed: a symlinked entry is
// treated as absent, so a repository cannot point Fastlane at other files.
func (f *MetadataFetcher) fetchLocalFastlaneMetadata() (*AppMetadata, error) {
	baseDir, err := filepath.Abs(f.cfg.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve base directory: %w", err)
	}
	locales, err := localpath.ReadDir(baseDir, fastlaneMetadataPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", errFastlaneUnavailable, fastlaneMetadataPath)
	}
	if err != nil {
		return nil, fmt.Errorf("read local fastlane metadata: %w", err)
	}
	var names []string
	for _, locale := range locales {
		if locale.IsDir() {
			names = append(names, locale.Name())
		}
	}
	locale, err := selectFastlaneLocaleFromNames(names)
	if err != nil {
		return nil, err
	}
	localeDir := path.Join(fastlaneMetadataPath, locale)
	meta := &AppMetadata{}
	for _, field := range []struct {
		name string
		set  func(string)
	}{
		{"title.txt", func(value string) { meta.Name = value }},
		{"short_description.txt", func(value string) { meta.Summary = value }},
		{"full_description.txt", func(value string) { meta.Description = value }},
	} {
		data, readErr := localpath.ReadFile(baseDir, path.Join(localeDir, field.name), MaxRemoteDownloadSize)
		if errors.Is(readErr, fs.ErrNotExist) || errors.Is(readErr, localpath.ErrRejected) {
			continue
		}
		if readErr != nil {
			return nil, fmt.Errorf("read local fastlane %s: %w", field.name, readErr)
		}
		field.set(strings.TrimSpace(string(data)))
	}

	imagesDir := path.Join(localeDir, "images")
	images, err := localpath.ReadDir(baseDir, imagesDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, localpath.ErrRejected) {
		return nil, fmt.Errorf("read local fastlane images: %w", err)
	}
	for _, entry := range images {
		if entry.Name() == "icon.png" && entry.Type().IsRegular() {
			meta.LocalIcon = filepath.Join(baseDir, filepath.FromSlash(imagesDir), entry.Name())
		}
	}
	screenshotsDir := path.Join(imagesDir, "phoneScreenshots")
	screenshots, err := localpath.ReadDir(baseDir, screenshotsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, localpath.ErrRejected) {
		return nil, fmt.Errorf("read local fastlane screenshots: %w", err)
	}
	for _, entry := range screenshots {
		if entry.Type().IsRegular() {
			meta.LocalImages = append(meta.LocalImages, filepath.Join(baseDir, filepath.FromSlash(screenshotsDir), entry.Name()))
		}
	}
	return meta, nil
}

func (f *MetadataFetcher) fetchGitHubFastlaneMetadata(ctx context.Context) (*AppMetadata, error) {
	repoPath := config.GetGitHubRepo(f.cfg.Repository)
	if repoPath == "" {
		return nil, fmt.Errorf("%w: no GitHub repository configured", errFastlaneUnavailable)
	}

	root, err := f.githubContents(ctx, repoPath, fastlaneMetadataPath)
	if err != nil {
		return nil, err
	}
	locale, err := selectFastlaneLocale(root)
	if err != nil {
		return nil, err
	}
	basePath := fastlaneMetadataPath + "/" + locale

	entries, err := f.githubContents(ctx, repoPath, basePath)
	if err != nil {
		return nil, fmt.Errorf("fetching Fastlane locale: %w", err)
	}
	textURL := func(name string) string {
		for _, entry := range entries {
			if entry.Name == name && entry.Type == "file" {
				return entry.DownloadURL
			}
		}
		return ""
	}
	listDirectory := func(path string) ([]fastlaneEntry, error) {
		return f.githubContents(ctx, repoPath, path)
	}
	mediaURL := func(entry fastlaneEntry) string { return entry.DownloadURL }

	return f.buildFastlaneMetadata(ctx, config.SourceGitHub, basePath, textURL, listDirectory, mediaURL)
}

func (f *MetadataFetcher) githubContents(ctx context.Context, repoPath, path string) ([]fastlaneEntry, error) {
	requestURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s", repoPath, path)
	return f.fastlaneEntries(ctx, config.SourceGitHub, "GitHub Fastlane API", requestURL, path, "application/vnd.github+json")
}

// fastlaneEntries lists one Fastlane directory through a forge API. A 404 is
// errFastlaneUnavailable; any other failure is a failed request.
func (f *MetadataFetcher) fastlaneEntries(ctx context.Context, kind config.SourceType, service, requestURL, path, accept string) ([]fastlaneEntry, error) {
	req, err := newForgeRequest(ctx, kind, requestURL)
	if err != nil {
		return nil, fmt.Errorf("create %s request: %w", service, err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s directory: %w", service, err)
	}
	defer resp.Body.Close()

	if err := checkHTTPStatus(resp, service); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return nil, fmt.Errorf("%w: %s", errFastlaneUnavailable, path)
		}
		return nil, err
	}

	var entries []fastlaneEntry
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &entries); err != nil {
		return nil, fmt.Errorf("parse %s directory: %w", service, err)
	}
	return entries, nil
}

func (f *MetadataFetcher) fetchGiteaFastlaneMetadata(ctx context.Context) (*AppMetadata, error) {
	baseURL, repoPath := config.GetGiteaRepo(f.cfg.Repository)
	if repoPath == "" {
		return nil, fmt.Errorf("%w: no Gitea/Codeberg repository configured", errFastlaneUnavailable)
	}
	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: invalid Gitea repo path: %s", errFastlaneUnavailable, repoPath)
	}
	owner, repo := parts[0], parts[1]

	root, err := f.giteaContents(ctx, baseURL, owner, repo, fastlaneMetadataPath)
	if err != nil {
		return nil, err
	}
	locale, err := selectFastlaneLocale(root)
	if err != nil {
		return nil, err
	}
	basePath := fastlaneMetadataPath + "/" + locale

	entries, err := f.giteaContents(ctx, baseURL, owner, repo, basePath)
	if err != nil {
		return nil, fmt.Errorf("fetching Fastlane locale: %w", err)
	}
	textURL := func(name string) string {
		for _, entry := range entries {
			if entry.Name == name && entry.Type == "file" {
				return entry.DownloadURL
			}
		}
		return ""
	}
	listDirectory := func(path string) ([]fastlaneEntry, error) {
		return f.giteaContents(ctx, baseURL, owner, repo, path)
	}
	mediaURL := func(entry fastlaneEntry) string { return entry.DownloadURL }

	return f.buildFastlaneMetadata(ctx, config.SourceGitea, basePath, textURL, listDirectory, mediaURL)
}

// giteaContents lists a directory via the Gitea/Forgejo contents API.
// The response shape matches GitHub's contents API (name, path, type, download_url).
func (f *MetadataFetcher) giteaContents(ctx context.Context, baseURL, owner, repo, path string) ([]fastlaneEntry, error) {
	requestURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s", baseURL, owner, repo, path)
	return f.fastlaneEntries(ctx, config.SourceGitea, "Gitea Fastlane API", requestURL, path, "application/json")
}

func (f *MetadataFetcher) fetchGitLabFastlaneMetadata(ctx context.Context) (*AppMetadata, error) {
	baseURL, repoPath := config.GetGitLabRepoWithBase(f.cfg.Repository)
	if repoPath == "" {
		return nil, fmt.Errorf("%w: no GitLab repository configured", errFastlaneUnavailable)
	}
	projectPath := url.PathEscape(repoPath)

	root, err := f.gitLabTree(ctx, baseURL, projectPath, fastlaneMetadataPath)
	if err != nil {
		return nil, err
	}
	locale, err := selectFastlaneLocale(root)
	if err != nil {
		return nil, err
	}
	basePath := fastlaneMetadataPath + "/" + locale

	entries, err := f.gitLabTree(ctx, baseURL, projectPath, basePath)
	if err != nil {
		return nil, fmt.Errorf("fetching Fastlane locale: %w", err)
	}
	textURL := func(name string) string {
		for _, entry := range entries {
			if entry.Name == name && entry.Type == "blob" {
				return f.gitLabRawURL(baseURL, projectPath, entry.Path)
			}
		}
		return ""
	}
	listDirectory := func(path string) ([]fastlaneEntry, error) {
		return f.gitLabTree(ctx, baseURL, projectPath, path)
	}
	mediaURL := func(entry fastlaneEntry) string {
		return f.gitLabRawURL(baseURL, projectPath, entry.Path)
	}

	return f.buildFastlaneMetadata(ctx, config.SourceGitLab, basePath, textURL, listDirectory, mediaURL)
}

func (f *MetadataFetcher) gitLabTree(ctx context.Context, baseURL, projectPath, path string) ([]fastlaneEntry, error) {
	requestURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/tree?path=%s&per_page=100&ref=HEAD",
		baseURL, projectPath, url.QueryEscape(path))
	return f.fastlaneEntries(ctx, config.SourceGitLab, "GitLab Fastlane API", requestURL, path, "")
}

func (f *MetadataFetcher) gitLabRawURL(baseURL, projectPath, path string) string {
	return fmt.Sprintf("%s/api/v4/projects/%s/repository/files/%s/raw?ref=HEAD",
		baseURL, projectPath, url.PathEscape(path))
}

func (f *MetadataFetcher) buildFastlaneMetadata(
	ctx context.Context,
	kind config.SourceType,
	basePath string,
	textURL func(string) string,
	listDirectory func(string) ([]fastlaneEntry, error),
	mediaURL func(fastlaneEntry) string,
) (*AppMetadata, error) {
	meta := &AppMetadata{}
	fields := []struct {
		name string
		set  func(string)
	}{
		{"title.txt", func(value string) { meta.Name = value }},
		{"short_description.txt", func(value string) { meta.Summary = value }},
		{"full_description.txt", func(value string) { meta.Description = value }},
	}
	for _, field := range fields {
		if rawURL := textURL(field.name); rawURL != "" {
			content, err := f.fetchFastlaneText(ctx, kind, rawURL)
			if err != nil {
				return nil, fmt.Errorf("fetching Fastlane %s: %w", field.name, err)
			}
			field.set(strings.TrimSpace(content))
		}
	}

	images, err := listDirectory(basePath + "/images")
	if err != nil {
		if !errors.Is(err, errFastlaneUnavailable) {
			return nil, fmt.Errorf("fetching Fastlane images: %w", err)
		}
		return meta, nil
	}
	for _, entry := range images {
		if entry.Name == "icon.png" && (entry.Type == "file" || entry.Type == "blob") {
			meta.IconURL = mediaURL(entry)
			break
		}
	}

	screenshots, err := listDirectory(basePath + "/images/phoneScreenshots")
	if err != nil {
		if errors.Is(err, errFastlaneUnavailable) {
			return meta, nil
		}
		return nil, fmt.Errorf("fetching Fastlane screenshots: %w", err)
	}
	sort.Slice(screenshots, func(i, j int) bool { return screenshots[i].Name < screenshots[j].Name })
	for _, entry := range screenshots {
		if entry.Type == "file" || entry.Type == "blob" {
			if imageURL := mediaURL(entry); imageURL != "" {
				meta.ImageURLs = append(meta.ImageURLs, imageURL)
			}
		}
	}

	return meta, nil
}

// fetchFastlaneText fetches one metadata file. rawURL comes from the forge's
// API response, so it is validated first; the forge token goes along only when
// the file is served by that forge's own host.
func (f *MetadataFetcher) fetchFastlaneText(ctx context.Context, kind config.SourceType, rawURL string) (string, error) {
	req, err := newForgeRequest(ctx, kind, rawURL)
	if err != nil {
		return "", fmt.Errorf("create Fastlane text request: %w", err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch Fastlane text: %w", err)
	}
	defer resp.Body.Close()
	if err := checkHTTPStatus(resp, "Fastlane text"); err != nil {
		return "", err
	}
	body, err := readBounded(resp.Body, MaxRemoteDownloadSize)
	if err != nil {
		return "", fmt.Errorf("read Fastlane text: %w", err)
	}
	return string(body), nil
}

func selectFastlaneLocale(entries []fastlaneEntry) (string, error) {
	var locales []string
	for _, entry := range entries {
		if entry.Type == "dir" || entry.Type == "tree" {
			locales = append(locales, entry.Name)
		}
	}
	return selectFastlaneLocaleFromNames(locales)
}

func selectFastlaneLocaleFromNames(locales []string) (string, error) {
	if len(locales) == 0 {
		return "", fmt.Errorf("%w: no Android locales", errFastlaneUnavailable)
	}
	sort.Strings(locales)
	for _, locale := range locales {
		if locale == "en-US" {
			return locale, nil
		}
	}
	return locales[0], nil
}
