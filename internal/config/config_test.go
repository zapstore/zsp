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
			yaml: `local: ./build/app.apk`,
			check: func(c *Config) bool {
				return c.Local == "./build/app.apk" &&
					c.GetSourceType() == SourceLocal
			},
		},
		{
			name: "local takes precedence",
			yaml: `
local: ./app.apk
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
			name: "release_source web html",
			yaml: `
repository: https://github.com/user/app
release_source:
  url: https://example.com/releases
  asset_url: https://example.com/app_$version.apk
  html:
    selector: ".version"
    attribute: text
    pattern: "v([0-9.]+)"
`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.ReleaseSource.IsWebSource &&
					c.ReleaseSource.URL == "https://example.com/releases" &&
					c.ReleaseSource.AssetURL == "https://example.com/app_$version.apk" &&
					c.ReleaseSource.HTML != nil &&
					c.ReleaseSource.HTML.Selector == ".version" &&
					c.ReleaseSource.HTML.Attribute == "text" &&
					c.ReleaseSource.HTML.Pattern == "v([0-9.]+)" &&
					c.GetSourceType() == SourceWeb
			},
		},
		{
			name: "release_source web json",
			yaml: `
repository: https://github.com/user/app
release_source:
  url: https://api.example.com/releases
  asset_url: https://cdn.example.com/app_$version.apk
  json:
    path: "$.tag_name"
    pattern: "v([0-9.]+)"
`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.ReleaseSource.IsWebSource &&
					c.ReleaseSource.JSON != nil &&
					c.ReleaseSource.JSON.Path == "$.tag_name" &&
					c.GetSourceType() == SourceWeb
			},
		},
		{
			name: "release_source web redirect",
			yaml: `
repository: https://github.com/user/app
release_source:
  url: https://example.com/latest
  asset_url: https://example.com/v$version/app.apk
  redirect:
    header: location
    pattern: "/v([0-9.]+)/"
`,
			check: func(c *Config) bool {
				return c.ReleaseSource != nil &&
					c.ReleaseSource.IsWebSource &&
					c.ReleaseSource.Redirect != nil &&
					c.ReleaseSource.Redirect.Header == "location" &&
					c.GetSourceType() == SourceWeb
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
			name: "multiple extractors",
			yaml: `
repository: https://github.com/user/app
release_source:
  url: https://example.com/releases
  asset_url: https://example.com/app.apk
  html:
    selector: ".version"
  json:
    path: "$.version"
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
			name:    "with local",
			config:  Config{Local: "./app.apk"},
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
	// Test that local > release_source > repository
	tests := []struct {
		name   string
		config Config
		want   SourceType
	}{
		{
			name: "local wins over all",
			config: Config{
				Local:         "./app.apk",
				Repository:    "https://github.com/user/app",
				ReleaseSource: &ReleaseSource{URL: "https://gitlab.com/user/releases"},
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
					URL:         "https://example.com/releases",
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
