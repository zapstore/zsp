// Package config handles YAML configuration parsing and validation.
package config

import (
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ZapstoreCLIConfig represents the old zapstore-cli zapstore.yaml format.
// Used for detection and migration to the new zsp format.
type ZapstoreCLIConfig struct {
	// Fields that map directly (same name)
	Name        string   `yaml:"name,omitempty"`
	Summary     string   `yaml:"summary,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Repository  string   `yaml:"repository,omitempty"`
	License     string   `yaml:"license,omitempty"`
	Changelog   string   `yaml:"changelog,omitempty"`
	Icon        string   `yaml:"icon,omitempty"`
	Images      []string `yaml:"images,omitempty"`

	// Fields that need renaming
	Homepage       string   `yaml:"homepage,omitempty"`       // -> website
	RemoteMetadata []string `yaml:"remote_metadata,omitempty"` // -> metadata_sources
	Tags           string   `yaml:"tags,omitempty"`           // space-delimited -> array

	// Legacy-only fields (to be dropped)
	Identifier       string   `yaml:"identifier,omitempty"`
	Version          yaml.Node `yaml:"version,omitempty"` // can be string or list
	Assets           []string `yaml:"assets,omitempty"`
	Executables      []string `yaml:"executables,omitempty"`
	ReleaseRepository string   `yaml:"release_repository,omitempty"`
	BlossomServer    string   `yaml:"blossom_server,omitempty"`
}

// MigrationResult contains the migration output.
type MigrationResult struct {
	Config   *Config  // The migrated config
	Warnings []string // Non-fatal warnings about dropped/changed fields
}

// NeedsMigration checks if raw YAML data is in the old zapstore-cli format.
// Returns true if any zapstore-cli-only fields are present.
func NeedsMigration(data []byte) bool {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false
	}
	return NeedsMigrationMap(raw)
}

// NeedsMigrationMap checks if a parsed YAML map needs migration.
func NeedsMigrationMap(raw map[string]any) bool {
	// These fields only exist in zapstore-cli format
	cliOnlyFields := []string{
		"assets",          // zsp uses release_source
		"homepage",        // zsp uses website
		"remote_metadata", // zsp uses metadata_sources
		"identifier",      // auto-extracted from APK
		"executables",     // CLI-only, not supported
		"release_repository",
		"blossom_server",
	}

	for _, field := range cliOnlyFields {
		if _, exists := raw[field]; exists {
			return true
		}
	}

	// Check if version is a list (web scraping spec)
	if v, exists := raw["version"]; exists {
		if _, isList := v.([]any); isList {
			return true
		}
	}

	// Check if tags is a string (legacy uses space-delimited)
	if t, exists := raw["tags"]; exists {
		if _, isString := t.(string); isString {
			return true
		}
	}

	return false
}

// CanMigrate checks if a zapstore-cli config can be automatically migrated.
// Returns an error describing why migration is not possible.
func CanMigrate(data []byte) error {
	var old ZapstoreCLIConfig
	if err := yaml.Unmarshal(data, &old); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Web version scraping is not supported
	if old.Version.Kind == yaml.SequenceNode {
		return fmt.Errorf("web version scraping (version as list) is not supported for auto-migration. Manual migration required")
	}

	// Web asset URLs are not supported
	for _, asset := range old.Assets {
		if strings.HasPrefix(asset, "http://") || strings.HasPrefix(asset, "https://") {
			return fmt.Errorf("web asset URLs are not supported for auto-migration. Manual migration required")
		}
	}

	// Multiple asset patterns are not supported
	if len(old.Assets) > 1 {
		return fmt.Errorf("multiple asset patterns are not supported for auto-migration. Use 'match' field manually")
	}

	// release_repository is not supported
	if old.ReleaseRepository != "" {
		return fmt.Errorf("release_repository is not supported for auto-migration. Use release_source manually")
	}

	return nil
}

// MigrateConfig converts a zapstore-cli config to zsp format.
// Returns the migrated config.
func MigrateConfig(data []byte) (*MigrationResult, error) {
	// First check if migration is possible
	if err := CanMigrate(data); err != nil {
		return nil, err
	}

	var old ZapstoreCLIConfig
	if err := yaml.Unmarshal(data, &old); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	result := &MigrationResult{
		Config:   &Config{},
		Warnings: []string{},
	}
	cfg := result.Config

	// Direct field mappings
	cfg.Name = old.Name
	cfg.Summary = old.Summary
	cfg.Description = old.Description
	cfg.Repository = old.Repository
	cfg.License = old.License
	cfg.Changelog = old.Changelog
	cfg.Icon = old.Icon
	cfg.Images = old.Images

	// Renamed fields
	if old.Homepage != "" {
		cfg.Website = old.Homepage
	}
	if len(old.RemoteMetadata) > 0 {
		cfg.MetadataSources = old.RemoteMetadata
	}

	// Tags: space-delimited string -> array
	if old.Tags != "" {
		cfg.Tags = strings.Fields(old.Tags)
	}

	// Assets migration
	if len(old.Assets) > 0 {
		migrateAssets(old, cfg)
	}

	// Silently drop zapstore-cli-only fields:
	// - identifier: auto-extracted from APK
	// - version: auto-extracted from APK/release
	// - executables: CLI binaries not supported
	// - blossom_server: use BLOSSOM_URL env var

	return result, nil
}

// migrateAssets handles the assets -> release_source + match migration.
func migrateAssets(old ZapstoreCLIConfig, cfg *Config) {
	if len(old.Assets) == 0 {
		return
	}

	asset := old.Assets[0]

	// Check if asset is local (contains /)
	if strings.Contains(asset, "/") {
		// Local mode: asset becomes release_source
		cfg.ReleaseSource = &ReleaseSource{
			LocalPath: asset,
		}
	} else {
		// Remote mode (GitHub/GitLab): asset becomes match pattern
		// Skip if it's just .* (match all) - these are default patterns
		if asset != ".*" && asset != ".*$" && asset != ".*.apk$" {
			cfg.Match = asset
		}
	}
}

// MigrateConfigFile reads a zapstore-cli config file, migrates it, and writes the new format.
// Creates a backup at path.bak before overwriting.
func MigrateConfigFile(path string) (*MigrationResult, error) {
	// Read original file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Check if it actually needs migration
	if !NeedsMigration(data) {
		return nil, fmt.Errorf("config does not need migration")
	}

	// Migrate
	result, err := MigrateConfig(data)
	if err != nil {
		return nil, err
	}

	// Create backup
	backupPath := path + ".bak"
	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to create backup at %s: %w", backupPath, err)
	}

	// Write migrated config
	if err := WriteMigratedConfig(path, result.Config); err != nil {
		return nil, fmt.Errorf("failed to write migrated config: %w", err)
	}

	return result, nil
}

// WriteMigratedConfig writes a Config to a YAML file with nice formatting.
func WriteMigratedConfig(path string, cfg *Config) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return WriteMigratedConfigTo(f, cfg)
}

// WriteMigratedConfigTo writes a Config to a writer with nice formatting.
func WriteMigratedConfigTo(w io.Writer, cfg *Config) error {
	// Build output manually for better control over field order and comments
	var lines []string

	// Add header comment
	lines = append(lines, "# Migrated from zapstore-cli format")
	lines = append(lines, "")

	// Repository first (most important)
	if cfg.Repository != "" {
		lines = append(lines, fmt.Sprintf("repository: %s", cfg.Repository))
	}

	// Release source
	if cfg.ReleaseSource != nil && cfg.ReleaseSource.LocalPath != "" {
		lines = append(lines, fmt.Sprintf("release_source: %q", cfg.ReleaseSource.LocalPath))
	}

	// Match pattern
	if cfg.Match != "" {
		lines = append(lines, fmt.Sprintf("match: %q", cfg.Match))
	}

	// Add blank line before metadata section
	if cfg.Name != "" || cfg.Summary != "" || cfg.Description != "" {
		lines = append(lines, "")
	}

	// Metadata
	if cfg.Name != "" {
		lines = append(lines, fmt.Sprintf("name: %s", cfg.Name))
	}
	if cfg.Summary != "" {
		lines = append(lines, fmt.Sprintf("summary: %s", cfg.Summary))
	}
	if cfg.Description != "" {
		// Multi-line description
		if strings.Contains(cfg.Description, "\n") {
			lines = append(lines, "description: |")
			for _, line := range strings.Split(cfg.Description, "\n") {
				lines = append(lines, "  "+line)
			}
		} else {
			lines = append(lines, fmt.Sprintf("description: %s", cfg.Description))
		}
	}

	// Tags
	if len(cfg.Tags) > 0 {
		lines = append(lines, fmt.Sprintf("tags: [%s]", strings.Join(cfg.Tags, ", ")))
	}

	// License and website
	if cfg.License != "" {
		lines = append(lines, fmt.Sprintf("license: %s", cfg.License))
	}
	if cfg.Website != "" {
		lines = append(lines, fmt.Sprintf("website: %s", cfg.Website))
	}

	// Media
	if cfg.Icon != "" || len(cfg.Images) > 0 {
		lines = append(lines, "")
	}
	if cfg.Icon != "" {
		lines = append(lines, fmt.Sprintf("icon: %s", cfg.Icon))
	}
	if len(cfg.Images) > 0 {
		lines = append(lines, "images:")
		for _, img := range cfg.Images {
			lines = append(lines, fmt.Sprintf("  - %s", img))
		}
	}

	// Changelog
	if cfg.Changelog != "" {
		lines = append(lines, fmt.Sprintf("changelog: %s", cfg.Changelog))
	}

	// Metadata sources
	if len(cfg.MetadataSources) > 0 {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("metadata_sources: [%s]", strings.Join(cfg.MetadataSources, ", ")))
	}

	// Write all lines
	content := strings.Join(lines, "\n") + "\n"
	_, err := w.Write([]byte(content))
	return err
}

// LoadWithMigrationCheck loads a config file, detecting if migration is needed.
// If the config needs migration and quiet is false, returns (nil, true, nil) to prompt user.
// If the config needs migration and quiet is true, returns an error.
func LoadWithMigrationCheck(path string, quiet bool) (*Config, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("failed to read config file: %w", err)
	}

	if !NeedsMigration(data) {
		// Already in zsp format, load normally
		cfg, err := Load(path)
		return cfg, false, err
	}

	// Migration needed
	if quiet {
		return nil, true, fmt.Errorf("zapstore-cli config format detected. Run 'zsp publish' without --quiet to migrate")
	}

	// Check if auto-migration is possible
	if err := CanMigrate(data); err != nil {
		return nil, true, fmt.Errorf("config migration needed but cannot auto-migrate: %w", err)
	}

	// Return that migration is needed, caller should prompt user
	return nil, true, nil
}
