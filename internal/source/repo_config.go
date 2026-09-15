package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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

func fetchRepoConfig(ctx context.Context, client *http.Client, cfg *config.Config) ([]byte, error) {
	switch repositoryMetadataHost(cfg) {
	case config.SourceGitHub:
		return fetchGitHubRepoConfig(ctx, client, cfg)
	case config.SourceGitLab:
		return fetchGitLabRepoConfig(ctx, client, cfg)
	case config.SourceGitea:
		return fetchGiteaRepoConfig(ctx, client, cfg)
	default:
		return nil, errRepoConfigUnavailable
	}
}

func fetchGitHubRepoConfig(ctx context.Context, client *http.Client, cfg *config.Config) ([]byte, error) {
	repoPath := config.GetGitHubRepo(cfg.Repository)
	if repoPath == "" {
		return nil, errRepoConfigUnavailable
	}
	// Use raw.githubusercontent.com (not the Contents API) so this optional
	// check does not consume GitHub API rate limit — indexers hit this for
	// every app, and unauthenticated API 403s were aborting publishes.
	requestURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/HEAD/%s", repoPath, repoConfigPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub repo config request: %w", err)
	}
	if token := config.GetEnv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doRepoConfigRequest(client, req)
}

func fetchGitLabRepoConfig(ctx context.Context, client *http.Client, cfg *config.Config) ([]byte, error) {
	baseURL, repoPath := config.GetGitLabRepoWithBase(cfg.Repository)
	if repoPath == "" {
		return nil, errRepoConfigUnavailable
	}
	requestURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/files/%s/raw?ref=HEAD",
		baseURL, url.PathEscape(repoPath), url.PathEscape(repoConfigPath))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GitLab repo config request: %w", err)
	}
	return doRepoConfigRequest(client, req)
}

func fetchGiteaRepoConfig(ctx context.Context, client *http.Client, cfg *config.Config) ([]byte, error) {
	baseURL, repoPath := config.GetGiteaRepo(cfg.Repository)
	if repoPath == "" {
		return nil, errRepoConfigUnavailable
	}
	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: invalid Gitea repo path: %s", errRepoConfigUnavailable, repoPath)
	}
	requestURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/raw/%s", baseURL, parts[0], parts[1], repoConfigPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating Gitea repo config request: %w", err)
	}
	if token := os.Getenv("GITEA_TOKEN"); token != "" {
		req.Header.Set("Authorization", "token "+token)
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
