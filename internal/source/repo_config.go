package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/config"
)

const repoConfigPath = "zapstore.yaml"

// errRepoConfigUnavailable indicates the repository has no zapstore.yaml on
// the default branch, or the forge is unsupported. Callers keep the indexer config.
var errRepoConfigUnavailable = errors.New("repository zapstore.yaml not found")

// ResolveIndexerConfig returns zapstore.yaml from the repository root when the
// forge is supported and the file exists on the default branch.
// Repo config is an optional overlay: on absence, unsupported forge, fetch
// failure, or unparseable YAML it returns indexerCfg unchanged. Only context
// cancellation / deadline is fatal.
func ResolveIndexerConfig(ctx context.Context, indexerCfg *config.Config) (*config.Config, error) {
	return resolveIndexerConfig(ctx, indexerCfg, newSecureHTTPClient(30*time.Second))
}

func resolveIndexerConfig(ctx context.Context, indexerCfg *config.Config, client *http.Client) (*config.Config, error) {
	if indexerCfg == nil {
		return nil, fmt.Errorf("indexer config is nil")
	}
	if client == nil {
		client = newSecureHTTPClient(30 * time.Second)
	}

	data, err := fetchRepoConfig(ctx, client, indexerCfg)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		// Optional overlay — keep indexer YAML (404, 403/429, 5xx, network, …).
		return indexerCfg, nil
	}

	cfg, err := config.Parse(bytes.NewReader(data))
	if err != nil {
		// Malformed repo config must not block the indexer; keep the provided YAML.
		return indexerCfg, nil
	}
	return cfg, nil
}

// FetchRepoConfig returns the bytes of zapstore.yaml at the repository root
// on the default branch. Every failure except cancellation is reported as
// unavailable.
func FetchRepoConfig(ctx context.Context, repository string) ([]byte, error) {
	return fetchRepoConfig(ctx, newSecureHTTPClient(30*time.Second), &config.Config{Repository: repository})
}

// RepoFileURL returns the HTTPS URL of filePath, relative to the repository
// root, on the default branch of a GitHub, GitLab, or Gitea repository.
func RepoFileURL(repository, filePath string) (string, bool) {
	return repoFileURL(&config.Config{Repository: repository}, filePath)
}

func repoFileURL(cfg *config.Config, filePath string) (string, bool) {
	filePath = strings.TrimPrefix(path.Clean("/"+filePath), "/")
	if filePath == "" {
		return "", false
	}
	escaped := escapeRepoPath(filePath)
	switch repositoryMetadataHost(cfg) {
	case config.SourceGitHub:
		repoPath := config.GetGitHubRepo(cfg.Repository)
		if repoPath == "" {
			return "", false
		}
		// raw.githubusercontent.com (not the Contents API) does not consume
		// GitHub API rate limit; indexers hit this for every app.
		return fmt.Sprintf("https://raw.githubusercontent.com/%s/HEAD/%s", repoPath, escaped), true
	case config.SourceGitLab:
		baseURL, repoPath := config.GetGitLabRepoWithBase(cfg.Repository)
		if repoPath == "" {
			return "", false
		}
		return fmt.Sprintf("%s/api/v4/projects/%s/repository/files/%s/raw?ref=HEAD",
			baseURL, url.PathEscape(repoPath), url.PathEscape(filePath)), true
	case config.SourceGitea:
		baseURL, repoPath := config.GetGiteaRepo(cfg.Repository)
		parts := strings.Split(repoPath, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", false
		}
		return fmt.Sprintf("%s/api/v1/repos/%s/%s/raw/%s", baseURL, parts[0], parts[1], escaped), true
	default:
		return "", false
	}
}

func escapeRepoPath(filePath string) string {
	segments := strings.Split(filePath, "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func fetchRepoConfig(ctx context.Context, client *http.Client, cfg *config.Config) ([]byte, error) {
	requestURL, ok := repoFileURL(cfg, repoConfigPath)
	if !ok {
		return nil, errRepoConfigUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating repo config request: %w", err)
	}
	switch repositoryMetadataHost(cfg) {
	case config.SourceGitHub:
		if token := config.GetEnv("GITHUB_TOKEN"); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	case config.SourceGitea:
		if token := config.GetEnv("GITEA_TOKEN"); token != "" {
			req.Header.Set("Authorization", "token "+token)
		}
	}
	return doRepoConfigRequest(client, req)
}

func doRepoConfigRequest(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := DoWithTorFallback(req.Context(), client, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		// Optional overlay: any fetch failure keeps indexer YAML.
		return nil, errRepoConfigUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errRepoConfigUnavailable
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxRemoteDownloadSize))
	if err != nil {
		return nil, errRepoConfigUnavailable
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errRepoConfigUnavailable
	}
	return body, nil
}
