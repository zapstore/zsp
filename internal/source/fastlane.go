package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/zapstore/zsp/internal/config"
)

const fastlaneMetadataPath = "fastlane/metadata/android"

// errFastlaneUnavailable indicates that a repository does not contain Fastlane
// metadata. It is distinct from a failed request so callers can safely select
// the native repository metadata fallback only when Fastlane is absent.
var errFastlaneUnavailable = errors.New("Fastlane metadata not found")

type fastlaneEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"`
	DownloadURL string `json:"download_url"`
}

// fetchFastlaneMetadata fetches Android store metadata maintained alongside the
// source repository. GitHub and GitLab are supported.
func (f *MetadataFetcher) fetchFastlaneMetadata(ctx context.Context) (*AppMetadata, error) {
	switch config.DetectSourceType(f.cfg.Repository) {
	case config.SourceGitHub:
		return f.fetchGitHubFastlaneMetadata(ctx)
	case config.SourceGitLab:
		return f.fetchGitLabFastlaneMetadata(ctx)
	default:
		return nil, fmt.Errorf("%w: repository must be GitHub or GitLab", errFastlaneUnavailable)
	}
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

	return f.buildFastlaneMetadata(ctx, basePath, textURL, listDirectory, mediaURL)
}

func (f *MetadataFetcher) githubContents(ctx context.Context, repoPath, path string) ([]fastlaneEntry, error) {
	requestURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s", repoPath, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub Fastlane request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching GitHub Fastlane directory: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", errFastlaneUnavailable, path)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub Fastlane API error: %d", resp.StatusCode)
	}

	var entries []fastlaneEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxRemoteDownloadSize)).Decode(&entries); err != nil {
		return nil, fmt.Errorf("parsing GitHub Fastlane directory: %w", err)
	}
	return entries, nil
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

	return f.buildFastlaneMetadata(ctx, basePath, textURL, listDirectory, mediaURL)
}

func (f *MetadataFetcher) gitLabTree(ctx context.Context, baseURL, projectPath, path string) ([]fastlaneEntry, error) {
	requestURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/tree?path=%s&per_page=100&ref=HEAD",
		baseURL, projectPath, url.QueryEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GitLab Fastlane request: %w", err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching GitLab Fastlane directory: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", errFastlaneUnavailable, path)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitLab Fastlane API error: %d", resp.StatusCode)
	}

	var entries []fastlaneEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxRemoteDownloadSize)).Decode(&entries); err != nil {
		return nil, fmt.Errorf("parsing GitLab Fastlane directory: %w", err)
	}
	return entries, nil
}

func (f *MetadataFetcher) gitLabRawURL(baseURL, projectPath, path string) string {
	return fmt.Sprintf("%s/api/v4/projects/%s/repository/files/%s/raw?ref=HEAD",
		baseURL, projectPath, url.PathEscape(path))
}

func (f *MetadataFetcher) buildFastlaneMetadata(
	ctx context.Context,
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
			content, err := f.fetchFastlaneText(ctx, rawURL)
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

func (f *MetadataFetcher) fetchFastlaneText(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating Fastlane text request: %w", err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching Fastlane text: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Fastlane text request returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxRemoteDownloadSize))
	if err != nil {
		return "", fmt.Errorf("reading Fastlane text: %w", err)
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
