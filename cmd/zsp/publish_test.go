package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/ui"
)

func TestLoadPublicConfigRejectsAPKArgument(t *testing.T) {
	_, err := loadPublicConfig(&cli.Options{Args: []string{"app-release.apk"}})
	if !errors.Is(err, zsp.ErrInvalidConfig) {
		t.Fatalf("loadPublicConfig error = %v, want ErrInvalidConfig", err)
	}
	var coded codedError
	if !errors.As(err, &coded) || coded.Code() != "invalid_arguments" {
		t.Fatalf("code = %v, want invalid_arguments", err)
	}
	if !strings.Contains(err.Error(), "release_source") {
		t.Fatalf("error = %v, want release_source", err)
	}
}

func TestLoadPublicConfigRequiresZapstoreYAML(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := loadPublicConfig(&cli.Options{})
	opts := &cli.Options{}
	opts.Publish.RepoURL = "github.com/example/app"
	_, flagged := loadPublicConfig(opts)
	if err == nil || flagged == nil {
		t.Fatal("publish must load zapstore.yaml even when source flags are set")
	}
}

func TestLoadPublicConfigAppliesReleaseSourceFlag(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "app-release.apk")
	if err := os.WriteFile(apk, []byte("apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "zapstore.yaml")
	if err := os.WriteFile(configPath, []byte("repository: https://github.com/example/app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := &cli.Options{Args: []string{configPath}}
	opts.Publish.ReleaseSource = apk
	config, err := loadPublicConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	if config.Fetch.Repository != "https://github.com/example/app" {
		t.Fatalf("Repository = %q", config.Fetch.Repository)
	}
	if config.Fetch.ReleaseSource == nil || config.Fetch.ReleaseSource.LocalPath != apk {
		t.Fatalf("ReleaseSource = %#v, want local path %s", config.Fetch.ReleaseSource, apk)
	}
}

func TestSelectPublicAPKWrapsSelectionSentinel(t *testing.T) {
	options := &cli.Options{}
	options.Publish.Quiet = true
	_, err := selectPublicAPK(options, []*zsp.APK{
		{Hash: "one", CertificateHash: "certificate-one"},
		{Hash: "two", CertificateHash: "certificate-two"},
	})
	if !errors.Is(err, zsp.ErrAPKSelectionRequired) {
		t.Fatalf("selectPublicAPK error = %v, want ErrAPKSelectionRequired", err)
	}
}

func TestSelectPublicAPKSkipsSelectionForOneCertificate(t *testing.T) {
	options := &cli.Options{}
	options.Publish.Quiet = true
	candidates := []*zsp.APK{
		{Hash: "one", CertificateHash: "certificate"},
		{Hash: "two", CertificateHash: "certificate"},
	}

	got, err := selectPublicAPK(options, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if got != candidates[0] {
		t.Fatalf("selectPublicAPK() = %#v, want first candidate %#v", got, candidates[0])
	}
}

func TestSelectPublicAPKWrapsUncheckedSentinel(t *testing.T) {
	options := &cli.Options{}
	options.Publish.APKHash = "missing"
	_, err := selectPublicAPK(options, []*zsp.APK{{Hash: "present"}})
	if !errors.Is(err, zsp.ErrAPKNotChecked) {
		t.Fatalf("selectPublicAPK error = %v, want ErrAPKNotChecked", err)
	}
}

func TestAPKSelectionChoicesGroupsCandidatesByCertificateHash(t *testing.T) {
	candidates := []*zsp.APK{
		{Hash: "apk-a", CertificateHash: "certificate-a", Filename: "example-arm64.apk"},
		{Hash: "apk-b", CertificateHash: "certificate-a", Filename: "example-offline-arm64.apk"},
		{Hash: "apk-c", CertificateHash: "certificate-b", Filename: "example-x86_64.apk"},
	}

	got := apkSelectionChoices(candidates)
	want := []ui.SelectChoice{
		{Label: "certificate-a\n  example-arm64.apk\n  example-offline-arm64.apk", Value: "apk-a"},
		{Label: "certificate-b\n  example-x86_64.apk", Value: "apk-c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("apkSelectionChoices() = %#v, want %#v", got, want)
	}
}

func TestCLIErrorDocumentKeepsPartialResultAtTopLevel(t *testing.T) {
	encoded, err := json.Marshal(cliErrorDocument{
		OK:        false,
		Operation: "publish",
		Error:     cliErrorBody{Code: "temporary_failure"},
		Result:    &publishResultBody{Status: "partial"},
	})
	if err != nil {
		t.Fatal(err)
	}
	document := string(encoded)
	if !strings.Contains(document, `"result":{"status":"partial"}`) {
		t.Fatalf("partial result is not top-level and minimal: %s", document)
	}
	if strings.Contains(document, `"next_steps"`) || !strings.Contains(document, `"next_step":null`) {
		t.Fatalf("error must carry one next_step: %s", document)
	}
}

func TestLoadPublicConfigCheckAcceptsAPK(t *testing.T) {
	apk := filepath.Join(t.TempDir(), "app-release.apk")
	if err := os.WriteFile(apk, []byte("apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadPublicConfig(&cli.Options{Command: cli.CommandCheck, Args: []string{apk}})
	if err != nil {
		t.Fatal(err)
	}
	if config.Fetch.ReleaseSource == nil || config.Fetch.ReleaseSource.LocalPath != apk {
		t.Fatalf("ReleaseSource = %#v, want %s", config.Fetch.ReleaseSource, apk)
	}
	if _, err := loadPublicConfig(&cli.Options{Command: cli.CommandCheck, Args: []string{apk + ".missing.apk"}}); !errors.Is(err, zsp.ErrInvalidConfig) {
		t.Fatalf("missing APK error = %v, want ErrInvalidConfig", err)
	}
}
