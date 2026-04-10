package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNeedsMigration(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		expected bool
	}{
		{
			name:     "zapstore-cli with assets",
			yaml:     "repository: https://github.com/user/repo\nassets:\n  - .*.apk$",
			expected: true,
		},
		{
			name:     "zapstore-cli with homepage",
			yaml:     "repository: https://github.com/user/repo\nhomepage: https://example.com",
			expected: true,
		},
		{
			name:     "zapstore-cli with remote_metadata",
			yaml:     "repository: https://github.com/user/repo\nremote_metadata:\n  - playstore",
			expected: true,
		},
		{
			name:     "zapstore-cli with identifier",
			yaml:     "identifier: com.example.app\nassets:\n  - ./app.apk",
			expected: true,
		},
		{
			name:     "zapstore-cli with executables",
			yaml:     "repository: https://github.com/user/tool\nexecutables:\n  - mytool",
			expected: true,
		},
		{
			name:     "zapstore-cli with space-delimited tags",
			yaml:     "repository: https://github.com/user/repo\ntags: foo bar baz",
			expected: true,
		},
		{
			name:     "zapstore-cli with version list (web scraping)",
			yaml:     "version:\n  - https://example.com\n  - $.version\n  - v(.*)",
			expected: true,
		},
		{
			name:     "zsp format - minimal",
			yaml:     "repository: https://github.com/user/repo",
			expected: false,
		},
		{
			name:     "zsp format - with release_source",
			yaml:     "release_source: https://github.com/user/repo",
			expected: false,
		},
		{
			name:     "zsp format - with array tags",
			yaml:     "repository: https://github.com/user/repo\ntags: [foo, bar]",
			expected: false,
		},
		{
			name:     "zsp format - with website",
			yaml:     "repository: https://github.com/user/repo\nwebsite: https://example.com",
			expected: false,
		},
		{
			name:     "zsp format - with metadata_sources",
			yaml:     "repository: https://github.com/user/repo\nmetadata_sources: [playstore]",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeedsMigration([]byte(tt.yaml))
			if got != tt.expected {
				t.Errorf("NeedsMigration() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestCanMigrate(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantError bool
		errorMsg  string
	}{
		{
			name:      "simple github with assets",
			yaml:      "repository: https://github.com/user/repo\nassets:\n  - .*.apk$",
			wantError: false,
		},
		{
			name:      "local assets",
			yaml:      "assets:\n  - ./build/*.apk",
			wantError: false,
		},
		{
			name:      "web version scraping - not supported",
			yaml:      "version:\n  - https://example.com\n  - $.version\n  - v(.*)\nassets:\n  - https://example.com/app.apk",
			wantError: true,
			errorMsg:  "web version scraping",
		},
		{
			name:      "web asset URLs - not supported",
			yaml:      "assets:\n  - https://example.com/app.apk",
			wantError: true,
			errorMsg:  "web asset URLs",
		},
		{
			name:      "release_repository - not supported",
			yaml:      "repository: https://github.com/user/repo\nrelease_repository: https://github.com/user/releases",
			wantError: true,
			errorMsg:  "release_repository",
		},
		{
			name:      "multiple asset patterns - not supported",
			yaml:      "repository: https://github.com/user/repo\nassets:\n  - .*-arm64\\.apk$\n  - .*-x86_64\\.apk$",
			wantError: true,
			errorMsg:  "multiple asset patterns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CanMigrate([]byte(tt.yaml))
			if tt.wantError {
				if err == nil {
					t.Errorf("CanMigrate() expected error containing %q, got nil", tt.errorMsg)
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("CanMigrate() error = %v, want error containing %q", err, tt.errorMsg)
				}
			} else if err != nil {
				t.Errorf("CanMigrate() unexpected error: %v", err)
			}
		})
	}
}

func TestMigrateConfig(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		wantRepo    string
		wantMatch   string
		wantLocal   string
		wantWebsite string
		wantTags    []string
		wantMeta    []string
	}{
		{
			name:     "github with assets and remote_metadata",
			yaml:     "repository: https://github.com/ZeusLN/zeus\nassets:\n  - .*.apk$\nremote_metadata:\n  - playstore",
			wantRepo: "https://github.com/ZeusLN/zeus",
			wantMeta: []string{"playstore"},
		},
		{
			name:        "local full config",
			yaml:        "name: Sample\nhomepage: https://zapstore.dev\ntags: zapstore repository blah\nrepository: https://github.com/zapstore/zapstore\nassets:\n  - ./.*.apk",
			wantRepo:    "https://github.com/zapstore/zapstore",
			wantWebsite: "https://zapstore.dev",
			wantTags:    []string{"zapstore", "repository", "blah"},
			wantLocal:   "./.*.apk",
		},
		{
			name:      "gitlab with match pattern",
			yaml:      "repository: https://gitlab.com/AuroraOSS/AuroraStore\nassets:\n  - .*-arm64\\.apk$",
			wantRepo:  "https://gitlab.com/AuroraOSS/AuroraStore",
			wantMatch: ".*-arm64\\.apk$",
		},
		{
			name:      "local minimal",
			yaml:      "assets:\n  - apks/mempal.*.apk\nremote_metadata:\n  - fdroid",
			wantLocal: "apks/mempal.*.apk",
			wantMeta:  []string{"fdroid"},
		},
		{
			name:      "with dropped fields",
			yaml:      "identifier: com.example.app\nversion: 1.0.0\nexecutables:\n  - myapp\nblossom_server: https://blossom.example.com\nassets:\n  - ./app.apk",
			wantLocal: "./app.apk",
			// Dropped fields (identifier, version, executables, blossom_server) are silently ignored
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := MigrateConfig([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("MigrateConfig() error: %v", err)
			}

			cfg := result.Config

			if cfg.Repository != tt.wantRepo {
				t.Errorf("Repository = %q, want %q", cfg.Repository, tt.wantRepo)
			}

			if cfg.Match != tt.wantMatch {
				t.Errorf("Match = %q, want %q", cfg.Match, tt.wantMatch)
			}

			if tt.wantLocal != "" {
				if cfg.ReleaseSource == nil {
					t.Errorf("ReleaseSource is nil, want LocalPath = %q", tt.wantLocal)
				} else if cfg.ReleaseSource.LocalPath != tt.wantLocal {
					t.Errorf("ReleaseSource.LocalPath = %q, want %q", cfg.ReleaseSource.LocalPath, tt.wantLocal)
				}
			}

			if cfg.Website != tt.wantWebsite {
				t.Errorf("Website = %q, want %q", cfg.Website, tt.wantWebsite)
			}

			if tt.wantTags != nil {
				if len(cfg.Tags) != len(tt.wantTags) {
					t.Errorf("Tags = %v, want %v", cfg.Tags, tt.wantTags)
				} else {
					for i, tag := range tt.wantTags {
						if cfg.Tags[i] != tag {
							t.Errorf("Tags[%d] = %q, want %q", i, cfg.Tags[i], tag)
						}
					}
				}
			}

			if tt.wantMeta != nil {
				if len(cfg.MetadataSources) != len(tt.wantMeta) {
					t.Errorf("MetadataSources = %v, want %v", cfg.MetadataSources, tt.wantMeta)
				}
			}
		})
	}
}

func TestWriteMigratedConfig(t *testing.T) {
	cfg := &Config{
		Repository:      "https://github.com/zapstore/zapstore",
		Match:           ".*-arm64\\.apk$",
		Name:            "Sample App",
		Summary:         "A sample app",
		Tags:            []string{"zapstore", "sample"},
		License:         "MIT",
		Website:         "https://zapstore.dev",
		Icon:            "icon.png",
		Images:          []string{"screenshot1.png", "screenshot2.png"},
		Changelog:       "CHANGELOG.md",
		MetadataSources: []string{"github", "playstore"},
	}

	var buf bytes.Buffer
	err := WriteMigratedConfigTo(&buf, cfg)
	if err != nil {
		t.Fatalf("WriteMigratedConfigTo() error: %v", err)
	}

	output := buf.String()

	// Check key fields are present
	checks := []string{
		"repository: https://github.com/zapstore/zapstore",
		`match: ".*-arm64\\.apk$"`,
		"name: Sample App",
		"tags: [zapstore, sample]",
		"website: https://zapstore.dev",
		"metadata_sources: [github, playstore]",
	}

	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("output missing %q\nGot:\n%s", check, output)
		}
	}

	// Check header comment
	if !strings.Contains(output, "# Migrated from") {
		t.Errorf("output missing migration header comment")
	}

	// Verify the output can be parsed back
	parsed, err := Parse(strings.NewReader(output))
	if err != nil {
		t.Fatalf("Parse() failed on migrated output: %v\nOutput:\n%s", err, output)
	}

	// Verify key fields match
	if parsed.Repository != cfg.Repository {
		t.Errorf("parsed.Repository = %q, want %q", parsed.Repository, cfg.Repository)
	}
	if parsed.Match != cfg.Match {
		t.Errorf("parsed.Match = %q, want %q", parsed.Match, cfg.Match)
	}
	if parsed.Website != cfg.Website {
		t.Errorf("parsed.Website = %q, want %q", parsed.Website, cfg.Website)
	}
}

func TestMigrateConfigFile(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "zapstore.yaml")

	// Write a zapstore-cli config
	oldContent := `repository: https://github.com/user/app
homepage: https://example.com
tags: foo bar baz
assets:
  - .*.apk$
remote_metadata:
  - playstore
`
	if err := os.WriteFile(configPath, []byte(oldContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// Migrate
	result, err := MigrateConfigFile(configPath)
	if err != nil {
		t.Fatalf("MigrateConfigFile() error: %v", err)
	}

	// Check backup was created
	backupPath := configPath + ".bak"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Errorf("backup file not created at %s", backupPath)
	}

	// Check backup content matches original
	backupContent, _ := os.ReadFile(backupPath)
	if string(backupContent) != oldContent {
		t.Errorf("backup content doesn't match original")
	}

	// Check migrated config
	if result.Config.Repository != "https://github.com/user/app" {
		t.Errorf("Repository = %q, want %q", result.Config.Repository, "https://github.com/user/app")
	}
	if result.Config.Website != "https://example.com" {
		t.Errorf("Website = %q, want %q", result.Config.Website, "https://example.com")
	}
	if len(result.Config.Tags) != 3 || result.Config.Tags[0] != "foo" {
		t.Errorf("Tags = %v, want [foo bar baz]", result.Config.Tags)
	}

	// Read back the written file and verify it doesn't need migration
	newContent, _ := os.ReadFile(configPath)
	if NeedsMigration(newContent) {
		t.Errorf("migrated file still needs migration:\n%s", string(newContent))
	}
}

func TestNeedsMigration_Fixtures(t *testing.T) {
	fixtureDir := "../../testdata/configs/zapstore-cli"
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Skipf("fixtures not found: %v", err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		t.Run(entry.Name(), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(fixtureDir, entry.Name()))
			if err != nil {
				t.Fatalf("failed to read fixture: %v", err)
			}

			if !NeedsMigration(data) {
				t.Errorf("fixture %s should need migration", entry.Name())
			}
		})
	}
}

func TestMigrateConfig_Fixtures(t *testing.T) {
	fixtureDir := "../../testdata/configs/zapstore-cli"

	// These fixtures should migrate successfully
	migratable := []string{
		"github-apk.yaml",
		"local-apk-full.yaml",
		"gitlab-apk.yaml",
		"local-minimal.yaml",
	}

	for _, name := range migratable {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(fixtureDir, name))
			if err != nil {
				t.Skipf("fixture not found: %v", err)
			}

			result, err := MigrateConfig(data)
			if err != nil {
				t.Errorf("MigrateConfig() error: %v", err)
				return
			}

			// Verify the result can be validated
			if result.Config.Repository == "" && result.Config.ReleaseSource == nil {
				t.Errorf("migrated config has no repository or release_source")
			}
		})
	}

	// This fixture should NOT migrate
	t.Run("web-unsupported.yaml", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(fixtureDir, "web-unsupported.yaml"))
		if err != nil {
			t.Skipf("fixture not found: %v", err)
		}

		_, err = MigrateConfig(data)
		if err == nil {
			t.Errorf("MigrateConfig() should have failed for web-unsupported.yaml")
		}
	})
}
