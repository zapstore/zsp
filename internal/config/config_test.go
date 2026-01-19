package config

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(*Config) bool
	}{
		{
			name: "minimal github",
			yaml: `repository: https://github.com/user/app`,
			check: func(c *Config) bool {
				return c.Repository == "https://github.com/user/app" &&
					c.GetSourceType() == SourceGitHub
			},
		},
		{
			name: "minimal gitlab",
			yaml: `repository: https://gitlab.com/user/app`,
			check: func(c *Config) bool {
				return c.Repository == "https://gitlab.com/user/app" &&
					c.GetSourceType() == SourceGitLab
			},
		},
		{
			name: "minimal local",
			yaml: `release_source: ./build/app.apk`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.ReleaseSource.LocalPath == "./build/app.apk" &&
					c.GetSourceType() == SourceLocal
			},
		},
		{
			name: "local takes precedence",
			yaml: `
release_source: ./app.apk
repository: https://github.com/user/app
`,
			check: func(c *Config) bool {
				return c.GetSourceType() == SourceLocal
			},
		},
		{
			name: "release_source string",
			yaml: `
repository: https://github.com/user/app
release_source: https://github.com/user/app-releases
`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.ReleaseSource.URL == "https://github.com/user/app-releases" &&
					!c.ReleaseSource.IsWebSource &&
					c.GetSourceType() == SourceGitHub
			},
		},
		{
			name: "release_source fdroid",
			yaml: `
repository: https://github.com/user/app
release_source: https://f-droid.org/packages/com.example.app
`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.GetSourceType() == SourceFDroid &&
					GetFDroidPackageID(c.ReleaseSource.URL) == "com.example.app"
			},
		},
		{
			name: "release_source izzyondroid",
			yaml: `
repository: https://github.com/user/app
release_source: https://apt.izzysoft.de/fdroid/index/apk/com.example.app
`,
			check: func(c *Config) bool {
				info := GetFDroidRepoInfo(c.ReleaseSource.URL)
				return c.ReleaseSource != nil &&
					c.GetSourceType() == SourceFDroid &&
					info != nil &&
					info.PackageID == "com.example.app" &&
					info.RepoURL == "https://apt.izzysoft.de/fdroid/repo"
			},
		},
		{
			name: "release_source codeberg",
			yaml: `
repository: https://codeberg.org/user/app
release_source: https://codeberg.org/user/app-releases
`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.ReleaseSource.URL == "https://codeberg.org/user/app-releases" &&
					c.GetSourceType() == SourceGitea
			},
		},
		{
			name: "release_source with explicit type for self-hosted forgejo",
			yaml: `
repository: https://my-forgejo.example.com/user/app
release_source:
  url: https://my-forgejo.example.com/user/app
  type: gitea
`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.ReleaseSource.URL == "https://my-forgejo.example.com/user/app" &&
					c.ReleaseSource.Type == "gitea" &&
					!c.ReleaseSource.IsWebSource &&
					c.GetSourceType() == SourceGitea
			},
		},
		{
			name: "release_source with explicit type for self-hosted gitlab",
			yaml: `
repository: https://gitlab.mycompany.com/user/app
release_source:
  url: https://gitlab.mycompany.com/user/app
  type: gitlab
`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.ReleaseSource.URL == "https://gitlab.mycompany.com/user/app" &&
					c.ReleaseSource.Type == "gitlab" &&
					!c.ReleaseSource.IsWebSource &&
					c.GetSourceType() == SourceGitLab
			},
		},
		{
			name: "release_source web with version extractor",
			yaml: `
repository: https://github.com/user/app
release_source:
  version:
    url: https://example.com/releases
    selector: ".version"
    match: "([0-9.]+)"
  asset_url: https://example.com/app_{version}.apk
`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.ReleaseSource.IsWebSource &&
					c.ReleaseSource.Version != nil &&
					c.ReleaseSource.Version.URL == "https://example.com/releases" &&
					c.ReleaseSource.Version.Selector == ".version" &&
					c.ReleaseSource.AssetURL == "https://example.com/app_{version}.apk" &&
					c.GetSourceType() == SourceWeb
			},
		},
		{
			name: "release_source string URL from unknown source becomes web source",
			yaml: `
repository: https://github.com/user/app
release_source: https://example.com/downloads/app.apk
`,
			check: func(c *Config) bool {
				// Shorthand: release_source: URL is equivalent to { asset_url: URL }
				// URL is empty so we don't scrape a page, we use asset_url directly
				return c.ReleaseSource != nil &&
					c.ReleaseSource.IsWebSource &&
					c.ReleaseSource.URL == "" &&
					c.ReleaseSource.AssetURL == "https://example.com/downloads/app.apk" &&
					c.GetSourceType() == SourceWeb
			},
		},
		{
			name: "release_source string URL from known source stays normal",
			yaml: `
repository: https://gitlab.com/user/app
release_source: https://github.com/other/releases
`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					!c.ReleaseSource.IsWebSource &&
					c.ReleaseSource.URL == "https://github.com/other/releases" &&
					c.ReleaseSource.AssetURL == "" &&
					c.GetSourceType() == SourceGitHub
			},
		},
		{
			name: "full config",
			yaml: `
repository: https://github.com/user/app
release_source: https://github.com/user/app-releases
name: My App
description: A great app
summary: Short desc
tags: [a, b, c]
license: MIT
website: https://example.com
icon: ./icon.png
images:
  - https://example.com/1.png
  - https://example.com/2.png
changelog: CHANGELOG.md
match: ".*arm64.*\\.apk$"
`,
			check: func(c *Config) bool {
				return c.Name == "My App" &&
					c.Description == "A great app" &&
					len(c.Tags) == 3 &&
					c.License == "MIT" &&
					len(c.Images) == 2 &&
					c.Match == ".*arm64.*\\.apk$"
			},
		},
		{
			name: "unknown fields ignored",
			yaml: `
repository: https://github.com/user/app
unknown_field: should be ignored
another_unknown: 123
`,
			check: func(c *Config) bool {
				return c.Repository == "https://github.com/user/app"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse(strings.NewReader(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil && tt.check != nil && !tt.check(cfg) {
				t.Errorf("Parse() check failed for %s", tt.name)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "release_source as list",
			yaml: `
repository: https://github.com/user/app
release_source:
  - https://example1.com
  - https://example2.com
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.yaml))
			if err == nil {
				t.Errorf("Parse() expected error for %s", tt.name)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name:    "empty config",
			config:  Config{},
			wantErr: true,
		},
		{
			name:    "with repository",
			config:  Config{Repository: "https://github.com/user/app"},
			wantErr: false,
		},
		{
			name: "with release_source",
			config: Config{
				ReleaseSource: &ReleaseSource{URL: "https://github.com/user/releases"},
			},
			wantErr: false,
		},
		{
			name:    "with local release_source",
			config:  Config{ReleaseSource: &ReleaseSource{LocalPath: "./app.apk"}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDetectSourceType(t *testing.T) {
	tests := []struct {
		url  string
		want SourceType
	}{
		{"https://github.com/user/repo", SourceGitHub},
		{"https://GITHUB.COM/User/Repo", SourceGitHub},
		{"https://gitlab.com/user/repo", SourceGitLab},
		{"https://GITLAB.COM/User/Repo", SourceGitLab},
		// Self-hosted GitLab instances with "gitlab" in the domain
		{"https://gitlab.example.com/user/repo", SourceGitLab},
		{"https://gitlab.company.org/user/repo", SourceGitLab},
		{"https://my-gitlab.company.com/user/repo", SourceGitLab},
		{"https://codeberg.org/user/repo", SourceGitea},
		{"https://CODEBERG.ORG/User/Repo", SourceGitea},
		{"https://f-droid.org/packages/com.example", SourceFDroid},
		{"https://f-droid.org/en/packages/com.example", SourceFDroid},
		// IzzyOnDroid (F-Droid compatible)
		{"https://apt.izzysoft.de/fdroid/index/apk/com.example", SourceFDroid},
		{"https://APT.IZZYSOFT.DE/fdroid/index/apk/com.example", SourceFDroid},
		{"https://unknown.com/something", SourceUnknown},
		{"", SourceUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := DetectSourceType(tt.url); got != tt.want {
				t.Errorf("DetectSourceType(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestGetFDroidPackageID(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://f-droid.org/packages/com.example.app", "com.example.app"},
		{"https://f-droid.org/en/packages/com.example.app", "com.example.app"},
		{"https://f-droid.org/packages/com.example.app/", "com.example.app"},
		// IzzyOnDroid
		{"https://apt.izzysoft.de/fdroid/index/apk/com.example.app", "com.example.app"},
		{"https://apt.izzysoft.de/fdroid/index/apk/com.example.app/", "com.example.app"},
		{"https://github.com/user/repo", ""},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := GetFDroidPackageID(tt.url); got != tt.want {
				t.Errorf("GetFDroidPackageID(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestGetFDroidRepoInfo(t *testing.T) {
	tests := []struct {
		url          string
		wantRepoURL  string
		wantIndexURL string
		wantPkgID    string
		wantNil      bool
	}{
		{
			url:          "https://f-droid.org/packages/com.example.app",
			wantRepoURL:  "https://f-droid.org/repo",
			wantIndexURL: "https://f-droid.org/repo/index-v1.json",
			wantPkgID:    "com.example.app",
		},
		{
			url:          "https://f-droid.org/en/packages/com.example.app",
			wantRepoURL:  "https://f-droid.org/repo",
			wantIndexURL: "https://f-droid.org/repo/index-v1.json",
			wantPkgID:    "com.example.app",
		},
		{
			url:          "https://apt.izzysoft.de/fdroid/index/apk/com.example.app",
			wantRepoURL:  "https://apt.izzysoft.de/fdroid/repo",
			wantIndexURL: "https://apt.izzysoft.de/fdroid/repo/index-v1.json",
			wantPkgID:    "com.example.app",
		},
		{
			url:          "https://apt.izzysoft.de/fdroid/index/apk/com.example.app/",
			wantRepoURL:  "https://apt.izzysoft.de/fdroid/repo",
			wantIndexURL: "https://apt.izzysoft.de/fdroid/repo/index-v1.json",
			wantPkgID:    "com.example.app",
		},
		{
			url:     "https://github.com/user/repo",
			wantNil: true,
		},
		{
			url:     "https://unknown.com/something",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := GetFDroidRepoInfo(tt.url)
			if tt.wantNil {
				if got != nil {
					t.Errorf("GetFDroidRepoInfo(%q) = %+v, want nil", tt.url, got)
				}
				return
			}
			if got == nil {
				t.Errorf("GetFDroidRepoInfo(%q) = nil, want non-nil", tt.url)
				return
			}
			if got.RepoURL != tt.wantRepoURL {
				t.Errorf("GetFDroidRepoInfo(%q).RepoURL = %q, want %q", tt.url, got.RepoURL, tt.wantRepoURL)
			}
			if got.IndexURL != tt.wantIndexURL {
				t.Errorf("GetFDroidRepoInfo(%q).IndexURL = %q, want %q", tt.url, got.IndexURL, tt.wantIndexURL)
			}
			if got.PackageID != tt.wantPkgID {
				t.Errorf("GetFDroidRepoInfo(%q).PackageID = %q, want %q", tt.url, got.PackageID, tt.wantPkgID)
			}
		})
	}
}

func TestGetGitHubRepo(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/user/repo", "user/repo"},
		{"https://github.com/user/repo/releases", "user/repo"},
		{"https://GITHUB.COM/User/Repo", "User/Repo"},
		{"https://gitlab.com/user/repo", ""},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := GetGitHubRepo(tt.url); got != tt.want {
				t.Errorf("GetGitHubRepo(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestSourcePrecedence(t *testing.T) {
	// Test that release_source > repository
	tests := []struct {
		name   string
		config Config
		want   SourceType
	}{
		{
			name: "local release_source wins over repository",
			config: Config{
				Repository:    "https://github.com/user/app",
				ReleaseSource: &ReleaseSource{LocalPath: "./app.apk"},
			},
			want: SourceLocal,
		},
		{
			name: "release_source wins over repository",
			config: Config{
				Repository:    "https://github.com/user/app",
				ReleaseSource: &ReleaseSource{URL: "https://gitlab.com/user/releases"},
			},
			want: SourceGitLab,
		},
		{
			name: "repository as fallback",
			config: Config{
				Repository: "https://github.com/user/app",
			},
			want: SourceGitHub,
		},
		{
			name: "web source from release_source",
			config: Config{
				Repository: "https://github.com/user/app",
				ReleaseSource: &ReleaseSource{
					IsWebSource: true,
					AssetURL:    "https://example.com/app.apk",
				},
			},
			want: SourceWeb,
		},
		{
			name: "codeberg auto-detected as gitea",
			config: Config{
				ReleaseSource: &ReleaseSource{URL: "https://codeberg.org/user/repo"},
			},
			want: SourceGitea,
		},
		{
			name: "explicit type overrides auto-detection",
			config: Config{
				ReleaseSource: &ReleaseSource{
					URL:  "https://my-forgejo.example.com/user/repo",
					Type: "gitea",
				},
			},
			want: SourceGitea,
		},
		{
			name: "explicit gitlab type for self-hosted",
			config: Config{
				ReleaseSource: &ReleaseSource{
					URL:  "https://gitlab.mycompany.com/user/repo",
					Type: "gitlab",
				},
			},
			want: SourceGitLab,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.config.GetSourceType(); got != tt.want {
				t.Errorf("GetSourceType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseSourceType(t *testing.T) {
	tests := []struct {
		input string
		want  SourceType
	}{
		{"github", SourceGitHub},
		{"GitHub", SourceGitHub},
		{"GITHUB", SourceGitHub},
		{"gitlab", SourceGitLab},
		{"gitea", SourceGitea},
		{"Gitea", SourceGitea},
		{"fdroid", SourceFDroid},
		{"web", SourceWeb},
		{"local", SourceLocal},
		{"playstore", SourcePlayStore},
		{"unknown", SourceUnknown},
		{"", SourceUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ParseSourceType(tt.input); got != tt.want {
				t.Errorf("ParseSourceType(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetGiteaRepo(t *testing.T) {
	tests := []struct {
		url          string
		wantBase     string
		wantRepoPath string
	}{
		{
			"https://codeberg.org/user/repo",
			"https://codeberg.org",
			"user/repo",
		},
		{
			"https://codeberg.org/user/repo/releases",
			"https://codeberg.org",
			"user/repo",
		},
		{
			"https://my-forgejo.example.com/org/project",
			"https://my-forgejo.example.com",
			"org/project",
		},
		{
			"http://localhost:3000/user/repo",
			"http://localhost:3000",
			"user/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			gotBase, gotPath := GetGiteaRepo(tt.url)
			if gotBase != tt.wantBase {
				t.Errorf("GetGiteaRepo(%q) baseURL = %q, want %q", tt.url, gotBase, tt.wantBase)
			}
			if gotPath != tt.wantRepoPath {
				t.Errorf("GetGiteaRepo(%q) repoPath = %q, want %q", tt.url, gotPath, tt.wantRepoPath)
			}
		})
	}
}

func TestGetGitLabRepoWithBase(t *testing.T) {
	tests := []struct {
		url          string
		wantBase     string
		wantRepoPath string
	}{
		{
			"https://gitlab.com/user/repo",
			"https://gitlab.com",
			"user/repo",
		},
		{
			"https://gitlab.mycompany.com/org/project",
			"https://gitlab.mycompany.com",
			"org/project",
		},
		{
			"https://gitlab.com/user/repo/-/releases",
			"https://gitlab.com",
			"user/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			gotBase, gotPath := GetGitLabRepoWithBase(tt.url)
			if gotBase != tt.wantBase {
				t.Errorf("GetGitLabRepoWithBase(%q) baseURL = %q, want %q", tt.url, gotBase, tt.wantBase)
			}
			if gotPath != tt.wantRepoPath {
				t.Errorf("GetGitLabRepoWithBase(%q) repoPath = %q, want %q", tt.url, gotPath, tt.wantRepoPath)
			}
		})
	}
}

// TestParseAdditionalCases covers more parsing edge cases
func TestParseAdditionalCases(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(*Config) bool
	}{
		{
			name: "metadata_sources single",
			yaml: `
repository: https://github.com/user/app
metadata_sources:
  - playstore
`,
			check: func(c *Config) bool {
				return len(c.MetadataSources) == 1 && c.MetadataSources[0] == "playstore"
			},
		},
		{
			name: "metadata_sources multiple",
			yaml: `
repository: https://github.com/user/app
metadata_sources:
  - github
  - fdroid
  - playstore
`,
			check: func(c *Config) bool {
				return len(c.MetadataSources) == 3
			},
		},
		{
			name: "empty tags array",
			yaml: `
repository: https://github.com/user/app
tags: []
`,
			check: func(c *Config) bool {
				return len(c.Tags) == 0
			},
		},
		{
			name: "empty images array",
			yaml: `
repository: https://github.com/user/app
images: []
`,
			check: func(c *Config) bool {
				return len(c.Images) == 0
			},
		},
		{
			name: "multiline description",
			yaml: `
repository: https://github.com/user/app
description: |
  This is a multiline
  description that spans
  multiple lines.
`,
			check: func(c *Config) bool {
				return strings.Contains(c.Description, "multiline") &&
					strings.Contains(c.Description, "multiple lines")
			},
		},
		{
			name: "play store URL detected",
			yaml: `repository: https://play.google.com/store/apps/details?id=com.example.app`,
			check: func(c *Config) bool {
				return c.GetSourceType() == SourcePlayStore
			},
		},
		{
			name: "release_source only (no repository)",
			yaml: `release_source: https://github.com/user/releases`,
			check: func(c *Config) bool {
				return c.Repository == "" &&
					c.ReleaseSource != nil &&
					c.ReleaseSource.URL == "https://github.com/user/releases"
			},
		},
		{
			name: "local glob pattern",
			yaml: `release_source: "./builds/*.apk"`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.ReleaseSource.LocalPath == "./builds/*.apk" &&
					c.GetSourceType() == SourceLocal
			},
		},
		{
			name: "complex match regex",
			yaml: `
repository: https://github.com/user/app
match: "(?i).*-(arm64|universal).*\\.apk$"
`,
			check: func(c *Config) bool {
				return c.Match == "(?i).*-(arm64|universal).*\\.apk$"
			},
		},
		{
			name: "web source with only asset_url makes it web source",
			yaml: `
repository: https://github.com/user/app
release_source:
  asset_url: https://example.com/app.apk
`,
			check: func(c *Config) bool {
				return c.ReleaseSource.IsWebSource && c.GetSourceType() == SourceWeb
			},
		},
		{
			name: "web source with version html extractor",
			yaml: `
repository: https://github.com/user/app
release_source:
  version:
    url: https://example.com/releases
    selector: ".version"
    match: "([0-9.]+)"
  asset_url: https://example.com/app-{version}.apk
`,
			check: func(c *Config) bool {
				return c.ReleaseSource.IsWebSource &&
					c.ReleaseSource.Version != nil &&
					c.ReleaseSource.Version.Mode() == "html" &&
					c.ReleaseSource.Version.Selector == ".version" &&
					c.ReleaseSource.Version.Attribute == "" && // empty = text extraction
					c.ReleaseSource.Version.Match == "([0-9.]+)"
			},
		},
		{
			name: "web source with version html extractor using attribute",
			yaml: `
repository: https://github.com/user/app
release_source:
  version:
    url: https://example.com/releases
    selector: "a.download"
    attribute: href
    match: "app-([0-9.]+)\\.apk"
  asset_url: https://example.com/app-{version}.apk
`,
			check: func(c *Config) bool {
				return c.ReleaseSource.IsWebSource &&
					c.ReleaseSource.Version != nil &&
					c.ReleaseSource.Version.Mode() == "html" &&
					c.ReleaseSource.Version.Selector == "a.download" &&
					c.ReleaseSource.Version.Attribute == "href" &&
					c.ReleaseSource.Version.Match == "app-([0-9.]+)\\.apk"
			},
		},
		{
			name: "web source with version json extractor",
			yaml: `
repository: https://github.com/user/app
release_source:
  version:
    url: https://api.example.com/releases
    path: "$.tag_name"
    match: "v([0-9.]+)"
  asset_url: https://example.com/app-{version}.apk
`,
			check: func(c *Config) bool {
				return c.ReleaseSource.IsWebSource &&
					c.ReleaseSource.Version != nil &&
					c.ReleaseSource.Version.Mode() == "json" &&
					c.ReleaseSource.Version.Path == "$.tag_name" &&
					c.ReleaseSource.Version.Match == "v([0-9.]+)"
			},
		},
		{
			name: "web source with version header extractor",
			yaml: `
repository: https://github.com/user/app
release_source:
  version:
    url: https://example.com/download
    header: location
    match: "app-([0-9.]+)\\.apk"
  asset_url: https://cdn.example.com/app-{version}.apk
`,
			check: func(c *Config) bool {
				return c.ReleaseSource.IsWebSource &&
					c.ReleaseSource.Version != nil &&
					c.ReleaseSource.Version.Mode() == "header" &&
					c.ReleaseSource.Version.Header == "location" &&
					c.ReleaseSource.Version.Match == "app-([0-9.]+)\\.apk"
			},
		},
		{
			name: "explicit type github overrides detection",
			yaml: `
repository: https://example.com/user/app
release_source:
  url: https://example.com/user/app
  type: github
`,
			check: func(c *Config) bool {
				return c.ReleaseSource.Type == "github" && c.GetSourceType() == SourceGitHub
			},
		},
		{
			name: "explicit type fdroid",
			yaml: `
repository: https://github.com/user/app
release_source:
  url: https://custom-fdroid.example.com/repo
  type: fdroid
`,
			check: func(c *Config) bool {
				return c.ReleaseSource.Type == "fdroid" && c.GetSourceType() == SourceFDroid
			},
		},
		{
			name: "whitespace in tags",
			yaml: `
repository: https://github.com/user/app
tags:
  - " spaced tag "
  - normal
`,
			check: func(c *Config) bool {
				return len(c.Tags) == 2
			},
		},
		{
			name: "unicode in name and description",
			yaml: `
repository: https://github.com/user/app
name: "アプリ名"
description: "日本語の説明 🎉"
`,
			check: func(c *Config) bool {
				return c.Name == "アプリ名" && strings.Contains(c.Description, "🎉")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse(strings.NewReader(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil && tt.check != nil && !tt.check(cfg) {
				t.Errorf("Parse() check failed for %s", tt.name)
			}
		})
	}
}

// TestParseErrorCases covers cases that should produce errors
func TestParseErrorCases(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "invalid yaml syntax",
			yaml: `
repository: [invalid
  nested: bad
`,
		},
		{
			name: "release_source invalid type (list instead of string/map)",
			yaml: `
repository: https://github.com/user/app
release_source:
  - https://example1.com
  - https://example2.com
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.yaml))
			if err == nil {
				t.Errorf("Parse() expected error for %s", tt.name)
			}
		})
	}
}

// TestValidateURLCases covers URL validation edge cases
func TestValidateURLCases(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{"https://github.com/user/repo", false},
		{"http://github.com/user/repo", false},
		{"https://localhost:8080/user/repo", false},
		{"http://localhost/path", false},
		{"ftp://github.com/user/repo", true}, // Invalid scheme
		{"github.com/user/repo", true},       // No scheme
		{"https://", true},                   // No host
		{"https:///path", true},              // No host
		{"https://singleword", true},         // No TLD (unless localhost)
		{"", true},                           // Empty
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := ValidateURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

// TestValidateConfigCases covers more validation scenarios
func TestValidateConfigCases(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name:    "empty config fails",
			config:  Config{},
			wantErr: true,
		},
		{
			name:    "repository only passes",
			config:  Config{Repository: "https://github.com/user/app"},
			wantErr: false,
		},
		{
			name:    "local release_source only passes",
			config:  Config{ReleaseSource: &ReleaseSource{LocalPath: "./app.apk"}},
			wantErr: false,
		},
		{
			name: "release_source only passes",
			config: Config{
				ReleaseSource: &ReleaseSource{URL: "https://github.com/user/releases"},
			},
			wantErr: false,
		},
		{
			name:    "invalid repository URL fails",
			config:  Config{Repository: "not-a-valid-url"},
			wantErr: true,
		},
		{
			name:    "repository with invalid scheme fails",
			config:  Config{Repository: "ftp://github.com/user/app"},
			wantErr: true,
		},
		{
			name: "web source skips URL validation",
			config: Config{
				ReleaseSource: &ReleaseSource{
					IsWebSource: true,
					AssetURL:    "https://example.com/app.apk",
				},
			},
			wantErr: false,
		},
		{
			name: "web source with version placeholder but no version extractor fails",
			config: Config{
				ReleaseSource: &ReleaseSource{
					IsWebSource: true,
					AssetURL:    "https://example.com/app-{version}.apk",
				},
			},
			wantErr: true,
		},
		{
			name: "web source with version extractor and placeholder passes",
			config: Config{
				ReleaseSource: &ReleaseSource{
					IsWebSource: true,
					Version: &VersionExtractor{
						URL:      "https://example.com/releases",
						Selector: ".version",
						Match:    "([0-9.]+)",
					},
					AssetURL: "https://example.com/app-{version}.apk",
				},
			},
			wantErr: false,
		},
		{
			name: "non-web release_source with invalid URL fails",
			config: Config{
				ReleaseSource: &ReleaseSource{
					URL:         "invalid-url",
					IsWebSource: false,
				},
			},
			wantErr: true,
		},
		{
			name: "local release_source with repository set",
			config: Config{
				Repository:    "https://github.com/user/app",
				ReleaseSource: &ReleaseSource{LocalPath: "./app.apk"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestSourceTypeString covers SourceType.String() method
func TestSourceTypeString(t *testing.T) {
	tests := []struct {
		st   SourceType
		want string
	}{
		{SourceUnknown, "unknown"},
		{SourceLocal, "local"},
		{SourceGitHub, "github"},
		{SourceGitLab, "gitlab"},
		{SourceGitea, "gitea"},
		{SourceFDroid, "fdroid"},
		{SourceWeb, "web"},
		{SourcePlayStore, "playstore"},
		{SourceType(999), "unknown"}, // Unknown value
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.st.String(); got != tt.want {
				t.Errorf("SourceType(%d).String() = %q, want %q", tt.st, got, tt.want)
			}
		})
	}
}

// TestGetAPKSourceURL covers GetAPKSourceURL method
func TestGetAPKSourceURL(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{
			name:   "repository only",
			config: Config{Repository: "https://github.com/user/app"},
			want:   "https://github.com/user/app",
		},
		{
			name: "release_source takes precedence",
			config: Config{
				Repository:    "https://github.com/user/app",
				ReleaseSource: &ReleaseSource{URL: "https://gitlab.com/user/releases"},
			},
			want: "https://gitlab.com/user/releases",
		},
		{
			name:   "empty config",
			config: Config{},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.config.GetAPKSourceURL(); got != tt.want {
				t.Errorf("GetAPKSourceURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDetectSourceTypeAdditional covers more URL detection cases
func TestDetectSourceTypeAdditional(t *testing.T) {
	tests := []struct {
		url  string
		want SourceType
	}{
		// Play Store variations
		{"https://play.google.com/store/apps/details?id=com.example", SourcePlayStore},
		{"https://PLAY.GOOGLE.COM/store/apps/details?id=com.example", SourcePlayStore},

		// F-Droid variations
		{"https://www.f-droid.org/packages/com.example", SourceFDroid},
		{"https://f-droid.org/de/packages/com.example", SourceFDroid},
		{"https://f-droid.org/fr/packages/com.example", SourceFDroid},

		// GitHub variations
		{"https://github.com/user/repo/releases/latest", SourceGitHub},
		{"https://github.com/user/repo/releases/tag/v1.0.0", SourceGitHub},
		// Note: raw.githubusercontent.com is NOT detected as GitHub since it's not a repo URL
		{"https://raw.githubusercontent.com/user/repo/main/README.md", SourceUnknown},

		// GitLab variations
		{"https://gitlab.com/user/repo/-/releases", SourceGitLab},
		{"https://gitlab.com/group/subgroup/repo", SourceGitLab},

		// Self-hosted with gitlab in name
		{"https://gitlab-ce.company.com/user/repo", SourceGitLab},
		{"https://my.gitlab.server.com/user/repo", SourceGitLab},

		// Unknown cases
		{"https://bitbucket.org/user/repo", SourceUnknown},
		{"https://sourceforge.net/projects/example", SourceUnknown},
		{"https://example.com/downloads/app.apk", SourceUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := DetectSourceType(tt.url); got != tt.want {
				t.Errorf("DetectSourceType(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// TestLoadConfigFile tests loading config from file paths
func TestLoadConfigFile(t *testing.T) {
	// Test loading a valid config file from testdata
	cfg, err := Load("../../testdata/configs/github.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Repository == "" {
		t.Error("Load() repository should not be empty")
	}

	if cfg.BaseDir == "" {
		t.Error("Load() should set BaseDir")
	}

	// Test loading non-existent file
	_, err = Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("Load() should error for non-existent file")
	}
}

// TestLoadAllFixtures tests that all fixture configs parse successfully
func TestLoadAllFixtures(t *testing.T) {
	// Each fixture tests a unique feature/combination - no duplicates
	fixtures := []string{
		// Source types
		"local-minimal.yaml",      // Local source (glob pattern)
		"local-full.yaml",         // Local + all metadata fields
		"github.yaml",             // GitHub source
		"gitlab.yaml",             // GitLab source
		"gitea-codeberg.yaml",     // Gitea/Codeberg source
		"fdroid.yaml",             // F-Droid release source
		"izzy.yaml",               // IzzyOnDroid release source
		"self-hosted-gitlab.yaml", // Type override: gitlab
		"self-hosted-gitea.yaml",  // Type override: gitea
		// Web scraping extractors
		"web-html.yaml",     // HTML selector extractor (version.selector)
		"web-json.yaml",     // JSON API extractor (version.path)
		"web-redirect.yaml", // Header extractor (version.header)
		"web-direct.yaml",   // Direct URL (no version extraction)
		// Features
		"match-pattern.yaml",      // Asset match regex
		"playstore-metadata.yaml", // Play Store metadata
		"metadata-sources.yaml",   // Multiple metadata sources
	}

	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			cfg, err := Load("../../testdata/configs/" + fixture)
			if err != nil {
				t.Errorf("Load(%s) error = %v", fixture, err)
				return
			}

			// Basic validation that config loaded
			if cfg == nil {
				t.Errorf("Load(%s) returned nil config", fixture)
				return
			}

			// Validate each loaded config
			if err := cfg.Validate(); err != nil {
				t.Errorf("Load(%s) Validate() error = %v", fixture, err)
			}
		})
	}
}
