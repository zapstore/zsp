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
// On absence, unsupported forge, or unparseable YAML it returns indexerCfg unchanged.
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
		if errors.Is(err, errRepoConfigUnavailable) {
			return indexerCfg, nil
		}
		return nil, err
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
	requestURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s", repoPath, repoConfigPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub repo config request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.raw")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doRepoConfigRequest(client, req, "GitHub")
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
	return doRepoConfigRequest(client, req, "GitLab")
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
	return doRepoConfigRequest(client, req, "Gitea")
}

func doRepoConfigRequest(client *http.Client, req *http.Request, forge string) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s repo config: %w", forge, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errRepoConfigUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s repo config API error: %d", forge, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxRemoteDownloadSize))
	if err != nil {
		return nil, fmt.Errorf("reading %s repo config: %w", forge, err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errRepoConfigUnavailable
	}
	return body, nil
}
