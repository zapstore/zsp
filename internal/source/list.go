package source

import (
	"context"
	"fmt"

	"github.com/zapstore/zsp/v2/internal/config"
)

// Lister is implemented by the forge sources, which can list more than the
// release a fetch would pick.
type Lister interface {
	// ListReleases returns up to limit of the newest releases, newest first,
	// each with only its APK assets. Zero limit uses the default scan size.
	//
	// It applies neither the release filter nor the pre-release choice, so a
	// caller can see what a filter hides; PreRelease says which releases a
	// fetch would skip. Drafts and upcoming releases are left out. A release
	// with no APK is kept, with no assets. It reads and caches nothing.
	ListReleases(ctx context.Context, limit int) ([]*Release, error)
}

// apkAssets returns the APK assets of a release.
func apkAssets(assets []*Asset) []*Asset {
	apks := make([]*Asset, 0, len(assets))
	for _, asset := range assets {
		if asset.IsAPK() {
			apks = append(apks, asset)
		}
	}
	return apks
}

// ListReleases implements [Lister].
func (g *GitHub) ListReleases(ctx context.Context, limit int) ([]*Release, error) {
	limit, err := releasesToCheck(limit)
	if err != nil {
		return nil, err
	}
	req, err := g.newAPIRequest(ctx, g.apiURL("/repos/%s/%s/releases?per_page=%d", g.owner, g.repo, limit))
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer resp.Body.Close()
	g.moved.observe(ctx, g.client, resp)
	if err := g.checkStatus(resp); err != nil {
		return nil, err
	}

	var listed []githubRelease
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &listed); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	releases := make([]*Release, 0, len(listed))
	for i := range listed {
		if listed[i].Draft {
			continue
		}
		release := g.convertRelease(&listed[i])
		release.Assets = apkAssets(release.Assets)
		releases = append(releases, release)
	}
	return releases, nil
}

// ListReleases implements [Lister].
func (g *GitLab) ListReleases(ctx context.Context, limit int) ([]*Release, error) {
	limit, err := releasesToCheck(limit)
	if err != nil {
		return nil, err
	}
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/releases?per_page=%d", g.baseURL, g.projectID, limit)
	req, err := newForgeRequest(ctx, config.SourceGitLab, apiURL)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer resp.Body.Close()
	g.moved.observe(ctx, g.client, resp)
	if err := checkHTTPStatus(resp, "GitLab API"); err != nil {
		return nil, describeNotFound(err, fmt.Sprintf("no releases found or project %s not accessible", g.repoPath))
	}

	var listed []gitlabRelease
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &listed); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	if len(listed) > 0 {
		// The numeric id resolves markdown /uploads/ attachments.
		if err := g.ensureNumericProjectID(ctx); err != nil {
			return nil, err
		}
	}
	releases := make([]*Release, 0, len(listed))
	for i := range listed {
		if listed[i].UpcomingRelease {
			continue
		}
		release := g.convertRelease(&listed[i])
		release.Assets = apkAssets(release.Assets)
		releases = append(releases, release)
	}
	return releases, nil
}

// ListReleases implements [Lister].
func (g *Gitea) ListReleases(ctx context.Context, limit int) ([]*Release, error) {
	limit, err := releasesToCheck(limit)
	if err != nil {
		return nil, err
	}
	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/releases?limit=%d", g.baseURL, g.owner, g.repo, limit)
	req, err := newForgeRequest(ctx, config.SourceGitea, apiURL)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer resp.Body.Close()
	g.moved.observe(ctx, g.client, resp)
	if err := checkHTTPStatus(resp, "Gitea API"); err != nil {
		return nil, describeNotFound(err, fmt.Sprintf("no releases found for %s/%s", g.owner, g.repo))
	}

	var listed []giteaRelease
	if err := decodeJSONResponse(resp, MaxJSONResponseSize, &listed); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	releases := make([]*Release, 0, len(listed))
	for i := range listed {
		if listed[i].Draft {
			continue
		}
		release := g.convertRelease(&listed[i])
		release.Assets = apkAssets(release.Assets)
		releases = append(releases, release)
	}
	return releases, nil
}

var (
	_ Lister = (*GitHub)(nil)
	_ Lister = (*GitLab)(nil)
	_ Lister = (*Gitea)(nil)
)
