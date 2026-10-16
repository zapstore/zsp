package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zapstore/zsp"
)

func TestWizardMetadataDescription(t *testing.T) {
	got := wizardMetadataDescription([]string{"fastlane", "github", "playstore"})
	want := "Found Fastlane, GitHub, and a Play Store listing."
	if got != want {
		t.Fatalf("wizardMetadataDescription() = %q, want %q", got, want)
	}
	if got := wizardMetadataDescription([]string{"fdroid"}); got != "Found an F-Droid package page." {
		t.Fatalf("single = %q", got)
	}
}

func TestOrderMetadataSources(t *testing.T) {
	got := orderMetadataSources([]string{"playstore", "github", "fastlane", "github"})
	want := []string{"fastlane", "github", "playstore"}
	if !slices.Equal(got, want) {
		t.Fatalf("orderMetadataSources() = %v, want %v", got, want)
	}
	if got := orderMetadataSources(nil); got == nil || len(got) != 0 {
		t.Fatalf("orderMetadataSources(nil) = %#v, want empty non-nil", got)
	}
}

func TestSaveMetadataSourcesWritesBasicFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	run := wizardRun{config: zsp.Config{Fetch: zsp.FetchConfig{Repository: "https://github.com/example/app"}}}
	path, err := run.saveMetadataSources([]string{"fastlane", "github", "playstore"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !run.wroteYAML || run.config.Publish.MetadataSources == nil {
		t.Fatalf("run = %#v, want sources recorded", run)
	}
	cfg := readWizardYAML(t, path)
	if cfg.Fetch.Repository != "https://github.com/example/app" {
		t.Fatalf("Repository = %q", cfg.Fetch.Repository)
	}
	if !slices.Equal(cfg.Publish.MetadataSources, []string{"fastlane", "github", "playstore"}) {
		t.Fatalf("MetadataSources = %v", cfg.Publish.MetadataSources)
	}
}

func TestSaveMetadataSourcesWritesEmptySelection(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	apk := filepath.Join(dir, "app-release.apk")
	run := wizardRun{config: zsp.Config{Fetch: zsp.FetchConfig{ReleaseSource: &zsp.ReleaseSource{LocalPath: apk}}}}
	path, err := run.saveMetadataSources([]string{}, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := readWizardYAML(t, path)
	if cfg.Fetch.ReleaseSource == nil || cfg.Fetch.ReleaseSource.LocalPath != "./app-release.apk" {
		t.Fatalf("ReleaseSource = %#v", cfg.Fetch.ReleaseSource)
	}
	if cfg.Publish.MetadataSources == nil || len(cfg.Publish.MetadataSources) != 0 {
		t.Fatalf("MetadataSources = %#v, want empty", cfg.Publish.MetadataSources)
	}
}

func TestSaveMetadataSourcesAppendsWithoutRewriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, wizardConfigName)
	original := "repository: https://github.com/example/app\nsummary: Notes\n"
	run := wizardRun{
		config:      zsp.Config{Fetch: zsp.FetchConfig{Repository: "https://github.com/example/app"}},
		configLabel: path,
		yamlBytes:   []byte(original),
		localYAML:   true,
		hadYAML:     true,
	}
	written, err := run.saveMetadataSources([]string{"github"}, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(written)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "summary: Notes\n") || !strings.HasSuffix(string(data), "metadata_sources: [github]\n") {
		t.Fatalf("file =\n%s", data)
	}
}

func TestSaveMetadataSourcesRewritesChangedSource(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, wizardConfigName)
	run := wizardRun{
		config:        zsp.Config{Fetch: zsp.FetchConfig{Repository: "https://github.com/example/other"}},
		configLabel:   path,
		yamlBytes:     []byte("repository: https://github.com/example/app\nrelease_source: ./old.apk\nsummary: Notes\n"),
		localYAML:     true,
		hadYAML:       true,
		sourceChanged: true,
	}
	written, err := run.saveMetadataSources([]string{"github"}, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := readWizardYAML(t, written)
	if cfg.Fetch.Repository != "https://github.com/example/other" || cfg.Fetch.ReleaseSource != nil {
		t.Fatalf("source = %#v", cfg.Fetch)
	}
	if cfg.Publish.Summary != "Notes" || !slices.Equal(cfg.Publish.MetadataSources, []string{"github"}) {
		t.Fatalf("publish = %#v", cfg.Publish)
	}
}

func TestSaveMetadataSourcesLeavesExplicitFileAlone(t *testing.T) {
	dir := t.TempDir()
	run := wizardRun{
		yamlBytes: []byte("repository: https://github.com/example/app\n"),
		hadYAML:   true,
	}
	path, err := run.saveMetadataSources(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != "" || run.wroteYAML {
		t.Fatalf("path = %q, wrote = %v", path, run.wroteYAML)
	}
	if _, err := os.Stat(filepath.Join(dir, wizardConfigName)); !os.IsNotExist(err) {
		t.Fatal("save wrote a file")
	}
}

func readWizardYAML(t *testing.T, path string) zsp.Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := zsp.ParseConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ParseConfig() = %v\n%s", err, data)
	}
	return cfg
}
