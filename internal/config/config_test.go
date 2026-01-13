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
			name: "release_repository string",
			yaml: `
repository: https://github.com/user/app
release_repository: https://github.com/user/app-releases
`,
			check: func(c *Config) bool {
				return c.ReleaseRepository != nil &&
					c.ReleaseRepository.URL == "https://github.com/user/app-releases" &&
					!c.ReleaseRepository.IsWebSource &&
					c.GetSourceType() == SourceGitHub
			},
		},
		{
			name: "release_repository fdroid",
			yaml: `
repository: https://github.com/user/app
release_repository: https://f-droid.org/packages/com.example.app
`,
			check: func(c *Config) bool {
				return c.ReleaseRepository != nil &&
					c.GetSourceType() == SourceFDroid &&
					GetFDroidPackageID(c.ReleaseRepository.URL) == "com.example.app"
			},
		},
		{
			name: "release_repository web html",
			yaml: `
repository: https://github.com/user/app
release_repository:
  url: https://example.com/releases
  asset_url: https://example.com/app_$version.apk
  html:
    selector: ".version"
    attribute: text
    pattern: "v([0-9.]+)"
`,
			check: func(c *Config) bool {
				return c.ReleaseRepository != nil &&
					c.ReleaseRepository.IsWebSource &&
					c.ReleaseRepository.URL == "https://example.com/releases" &&
					c.ReleaseRepository.AssetURL == "https://example.com/app_$version.apk" &&
					c.ReleaseRepository.HTML != nil &&
					c.ReleaseRepository.HTML.Selector == ".version" &&
					c.ReleaseRepository.HTML.Attribute == "text" &&
					c.ReleaseRepository.HTML.Pattern == "v([0-9.]+)" &&
					c.GetSourceType() == SourceWeb
			},
		},
		{
			name: "release_repository web json",
			yaml: `
repository: https://github.com/user/app
release_repository:
  url: https://api.example.com/releases
  asset_url: https://cdn.example.com/app_$version.apk
  json:
    path: "$.tag_name"
    pattern: "v([0-9.]+)"
`,
			check: func(c *Config) bool {
				return c.ReleaseRepository != nil &&
					c.ReleaseRepository.IsWebSource &&
					c.ReleaseRepository.JSON != nil &&
					c.ReleaseRepository.JSON.Path == "$.tag_name" &&
					c.GetSourceType() == SourceWeb
			},
		},
		{
			name: "release_repository web redirect",
			yaml: `
repository: https://github.com/user/app
release_repository:
  url: https://example.com/latest
  asset_url: https://example.com/v$version/app.apk
  redirect:
    header: location
    pattern: "/v([0-9.]+)/"
`,
			check: func(c *Config) bool {
				return c.ReleaseRepository != nil &&
					c.ReleaseRepository.IsWebSource &&
					c.ReleaseRepository.Redirect != nil &&
					c.ReleaseRepository.Redirect.Header == "location" &&
					c.GetSourceType() == SourceWeb
			},
		},
		{
			name: "full config",
			yaml: `
repository: https://github.com/user/app
release_repository: https://github.com/user/app-releases
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
release_repository:
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
			name: "with release_repository",
			config: Config{
				ReleaseRepository: &ReleaseRepository{URL: "https://github.com/user/releases"},
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
		{"https://f-droid.org/packages/com.example", SourceFDroid},
		{"https://f-droid.org/en/packages/com.example", SourceFDroid},
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
	// Test that local > release_repository > repository
	tests := []struct {
		name   string
		config Config
		want   SourceType
	}{
		{
			name: "local wins over all",
			config: Config{
				Local:             "./app.apk",
				Repository:        "https://github.com/user/app",
				ReleaseRepository: &ReleaseRepository{URL: "https://gitlab.com/user/releases"},
			},
			want: SourceLocal,
		},
		{
			name: "release_repository wins over repository",
			config: Config{
				Repository:        "https://github.com/user/app",
				ReleaseRepository: &ReleaseRepository{URL: "https://gitlab.com/user/releases"},
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
			name: "web source from release_repository",
			config: Config{
				Repository: "https://github.com/user/app",
				ReleaseRepository: &ReleaseRepository{
					URL:         "https://example.com/releases",
					IsWebSource: true,
					AssetURL:    "https://example.com/app.apk",
				},
			},
			want: SourceWeb,
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
