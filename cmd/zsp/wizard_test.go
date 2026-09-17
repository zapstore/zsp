package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/ui"
	"gopkg.in/yaml.v3"
)

func TestWizardDiscoverReportsOnlyAcceptedSuggestion(t *testing.T) {
	status := http.StatusCreated
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/suggest" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		writer.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	t.Setenv("RELAYS", "ws"+strings.TrimPrefix(server.URL, "http"))

	cfg := zsp.Config{FetchConfig: zsp.FetchConfig{
		Repository: "https://github.com/example/app",
	}}
	candidate := &zsp.APK{
		SourceURL:       "https://github.com/example/app/releases/download/v1/app.apk",
		CertificateHash: strings.Repeat("a", 64),
	}
	if got := wizardDiscover(t.Context(), cfg, candidate, false); got != server.URL {
		t.Fatalf("wizardDiscover() = %q, want %q", got, server.URL)
	}
	candidate.SourceURL = ""
	if got := wizardDiscover(t.Context(), cfg, candidate, false); got != server.URL {
		t.Fatalf("wizardDiscover() with local APK = %q, want %q", got, server.URL)
	}

	status = http.StatusBadRequest
	if got := wizardDiscover(t.Context(), cfg, candidate, false); got != "" {
		t.Fatalf("wizardDiscover() after rejection = %q, want empty", got)
	}
}

func TestWizardDiscoverSkipsExistingAppID(t *testing.T) {
	suggested := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		suggested = true
	}))
	t.Cleanup(server.Close)
	t.Setenv("RELAYS", "ws"+strings.TrimPrefix(server.URL, "http"))

	cfg := zsp.Config{FetchConfig: zsp.FetchConfig{
		Repository: "https://github.com/example/app",
	}}
	candidate := &zsp.APK{
		AppID:           "com.example.app",
		CertificateHash: strings.Repeat("a", 64),
	}

	if got := wizardDiscover(t.Context(), cfg, candidate, true); got != "" {
		t.Fatalf("wizardDiscover() = %q, want empty", got)
	}
	if suggested {
		t.Fatal("wizardDiscover() sent a suggestion for an existing app ID")
	}
}

func TestWizardConfigFromSourceCode(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		repository string
		wantError  bool
	}{
		{
			name:       "repository without scheme",
			source:     "github.com/example/app",
			repository: "https://github.com/example/app",
		},
		{
			name:       "GitHub release page",
			source:     "github.com/GreenArt7c3/Amber/releases",
			repository: "https://github.com/greenart7c3/amber",
		},
		{
			name:      "GitHub URL without repository",
			source:    "github.com/owner",
			wantError: true,
		},
		{
			name: "closed source",
		},
		{
			name:      "invalid source",
			source:    "http://example.com/releases",
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := wizardConfigFromSourceCode(test.source)
			if test.wantError {
				if err == nil {
					t.Fatal("wizardConfigFromSourceCode() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("wizardConfigFromSourceCode() error = %v", err)
			}
			if got.config.Repository != test.repository {
				t.Errorf("Repository = %q, want %q", got.config.Repository, test.repository)
			}
			if got.config.ReleaseSource != nil {
				t.Errorf("ReleaseSource = %#v, want nil", got.config.ReleaseSource)
			}
		})
	}
}

func TestWizardConfigFromReleaseSourcePreservesRepository(t *testing.T) {
	got, err := wizardConfigFromReleaseSource("github.com/example/app", "https://downloads.example.com/app/releases")
	if err != nil {
		t.Fatal(err)
	}
	if got.config.Repository != "https://github.com/example/app" {
		t.Errorf("Repository = %q", got.config.Repository)
	}
	if got.config.ReleaseSource == nil || got.config.ReleaseSource.URL != "https://downloads.example.com/app/releases" {
		t.Errorf("ReleaseSource = %#v", got.config.ReleaseSource)
	}
}

func TestWizardConfigFromReleaseSourceUsesForgeRepository(t *testing.T) {
	got, err := wizardConfigFromReleaseSource("", "github.com/GreenArt7c3/Amber/releases")
	if err != nil {
		t.Fatal(err)
	}
	if got.config.Repository != "https://github.com/greenart7c3/amber" {
		t.Errorf("Repository = %q", got.config.Repository)
	}
	if got.config.ReleaseSource != nil {
		t.Errorf("ReleaseSource = %#v, want nil", got.config.ReleaseSource)
	}
}

func TestWizardConfigFromReleaseSourceAcceptsLocalAPKDirectory(t *testing.T) {
	directory := t.TempDir()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	got, err := wizardConfigFromReleaseSource("github.com/example/app", ".")
	if err != nil {
		t.Fatalf("wizardConfigFromReleaseSource() error = %v", err)
	}
	if got.config.ReleaseSource == nil {
		t.Fatal("ReleaseSource is nil")
	}
	if got.config.Repository != "https://github.com/example/app" {
		t.Errorf("Repository = %q", got.config.Repository)
	}
	gotInfo, err := os.Stat(got.config.ReleaseSource.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("ReleaseSource = %#v, want local directory %q", got.config.ReleaseSource, directory)
	}
}

func TestWizardReleaseLocation(t *testing.T) {
	tests := []struct {
		name string
		cfg  zsp.Config
		want string
	}{
		{
			name: "repository",
			cfg:  zsp.Config{FetchConfig: zsp.FetchConfig{Repository: "https://github.com/example/app"}},
			want: "https://github.com/example/app",
		},
		{
			name: "release source",
			cfg: zsp.Config{FetchConfig: zsp.FetchConfig{
				Repository:    "https://github.com/example/not-the-release",
				ReleaseSource: &zsp.ReleaseSource{URL: "https://github.com/example/app"},
			}},
			want: "https://github.com/example/app",
		},
		{
			name: "local APK",
			cfg: zsp.Config{FetchConfig: zsp.FetchConfig{
				ReleaseSource: &zsp.ReleaseSource{LocalPath: "/tmp/app.apk"},
			}},
			want: "/tmp/app.apk",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := wizardReleaseLocation(test.cfg); got != test.want {
				t.Errorf("wizardReleaseLocation() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWizardMetadataSourceChoices(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "fastlane", "metadata", "android"), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		source     zsp.Config
		root       string
		wantValues []string
		wantLabels []string
	}{
		{
			name: "local Fastlane and GitHub repository",
			source: zsp.Config{FetchConfig: zsp.FetchConfig{
				Repository: "https://github.com/example/app",
			}},
			root:       root,
			wantValues: []string{"fastlane", "github", "fdroid", "playstore"},
			wantLabels: []string{"Fastlane (local metadata)", "GitHub", "F-Droid", "Google Play Store"},
		},
		{
			name: "GitLab repository and Gitea release source",
			source: zsp.Config{FetchConfig: zsp.FetchConfig{
				Repository:    "https://gitlab.com/example/app",
				ReleaseSource: &zsp.ReleaseSource{URL: "https://codeberg.org/example/app/releases"},
			}},
			root:       t.TempDir(),
			wantValues: []string{"gitlab", "gitea", "fdroid", "playstore"},
			wantLabels: []string{"GitLab", "Gitea", "F-Droid", "Google Play Store"},
		},
		{
			name: "local APK excludes Fastlane and forge sources",
			source: zsp.Config{FetchConfig: zsp.FetchConfig{
				Repository:    "https://github.com/example/app",
				ReleaseSource: &zsp.ReleaseSource{LocalPath: "/tmp/apks"},
			}},
			root:       root,
			wantValues: []string{"fdroid", "playstore"},
			wantLabels: []string{"F-Droid", "Google Play Store"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values, labels := wizardMetadataSourceChoices(test.source, test.root)
			if strings.Join(values, ",") != strings.Join(test.wantValues, ",") {
				t.Errorf("values = %v, want %v", values, test.wantValues)
			}
			if strings.Join(labels, ",") != strings.Join(test.wantLabels, ",") {
				t.Errorf("labels = %v, want %v", labels, test.wantLabels)
			}
		})
	}
}

func TestSaveWizardYAMLPreservesSourceCodeAndReleaseSource(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "zapstore.yaml")
	if err := os.WriteFile(path, []byte("repository: https://github.com/example/app\nrelease_filter: ^v\nlicense: MIT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, document, err := loadWizardYAML(path, &zsp.APK{Name: "Example"})
	if err != nil {
		t.Fatal(err)
	}
	metadata.name = "Example"
	if err := saveWizardYAML(path, document, metadata, zsp.Config{
		FetchConfig: zsp.FetchConfig{
			Repository:    "https://github.com/example/app",
			ReleaseSource: &zsp.ReleaseSource{URL: "https://downloads.example.com/app/releases"},
		},
	}, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"repository: https://github.com/example/app", "release_source: https://downloads.example.com/app/releases", "release_filter: ^v", "license: MIT"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("saved configuration does not contain %q:\n%s", want, data)
		}
	}
	for _, omit := range []string{"name:", "icon:"} {
		if strings.Contains(string(data), omit) {
			t.Errorf("saved configuration must not contain %q:\n%s", omit, data)
		}
	}
}

func TestSaveWizardYAMLOmitsNameAndIcon(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "zapstore.yaml")
	if err := os.WriteFile(path, []byte("name: Example\nicon: ./icon.png\nrepository: https://github.com/example/app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, document, err := loadWizardYAML(path, &zsp.APK{Name: "Example"})
	if err != nil {
		t.Fatal(err)
	}
	metadata.name = "Example"
	metadata.icon = "./icon.png"
	if err := saveWizardYAML(path, document, metadata, zsp.Config{
		FetchConfig: zsp.FetchConfig{Repository: "https://github.com/example/app"},
	}, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, omit := range []string{"name:", "icon:"} {
		if strings.Contains(string(data), omit) {
			t.Errorf("saved configuration must not contain %q:\n%s", omit, data)
		}
	}
}

func TestSaveWizardYAMLDisablesMetadataAndAddsGuidance(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "zapstore.yaml")
	metadata := wizardMetadata{name: "Example", guidance: true}
	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}

	if err := saveWizardYAML(path, document, metadata, zsp.Config{
		FetchConfig: zsp.FetchConfig{Repository: "https://github.com/example/app"},
	}, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"metadata_sources: []", "# zsp metadata guidance", "# tags: [privacy, music]"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("saved configuration does not contain %q:\n%s", want, data)
		}
	}
}

func TestSaveWizardYAMLDoesNotValidateGeneratedDocument(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "zapstore.yaml")
	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}

	if err := saveWizardYAML(path, document, wizardMetadata{}, zsp.Config{}, root); err != nil {
		t.Fatalf("saveWizardYAML() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved zapstore.yaml: %v", err)
	}
}

func TestWizardProjectRootSkipsPromptWhenCurrentDirectoryIsGitRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	got, err := wizardProjectRoot()
	if err != nil {
		t.Fatalf("wizardProjectRoot() error = %v", err)
	}
	got, err = filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("wizardProjectRoot() = %q, want %q", got, want)
	}
}

func TestIsGitRepositoryRoot(t *testing.T) {
	root := t.TempDir()
	if isGitRepositoryRoot(root) {
		t.Fatal("empty directory must not count as a Git repository root")
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !isGitRepositoryRoot(root) {
		t.Fatal("directory with .git must count as a Git repository root")
	}
}

func TestLoadWizardYAMLUsesAppIDWhenNameIsMissing(t *testing.T) {
	metadata, _, err := loadWizardYAML(filepath.Join(t.TempDir(), "zapstore.yaml"), &zsp.APK{AppID: "com.example.app"})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.name != "com.example.app" {
		t.Fatalf("name = %q, want app ID", metadata.name)
	}
}

func TestWizardFailureSummaryDoesNotExposeInternalErrors(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"Configuration setup stopped", "Couldn't use zapstore.yaml. Fix its configuration and try again; it was left unchanged"},
		{"Relay reachability check stopped", "Couldn't reach any configured relay. No changes were made"},
	}
	for _, test := range tests {
		t.Run(test.title, func(t *testing.T) {
			if got := wizardFailureSummary(test.title); got != test.want {
				t.Errorf("wizardFailureSummary() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWizardFailurePresentationReportsCertificateMismatch(t *testing.T) {
	const apkHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const selectedHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	kind, summary, details := wizardFailurePresentation("Identity setup stopped", errCertificateHashMismatch(apkHash, selectedHash))
	if kind != "error" {
		t.Fatalf("kind = %q, want error", kind)
	}
	if summary != "Selected certificate does not match this APK" {
		t.Fatalf("summary = %q", summary)
	}
	want := []ui.KeyValue{
		{Key: "APK certificate hash", Value: apkHash},
		{Key: "Selected certificate hash", Value: selectedHash},
	}
	if len(details) != len(want) {
		t.Fatalf("details = %#v, want %#v", details, want)
	}
	for index := range want {
		if details[index] != want[index] {
			t.Fatalf("details[%d] = %+v, want %+v", index, details[index], want[index])
		}
	}

	kind, summary, details = wizardFailurePresentation("Identity setup stopped", errors.New("look up C1 proof: timeout"))
	if kind != "info" || summary != "Ownership was not changed" || details != nil {
		t.Fatalf("unrelated failure = (%q, %q, %#v)", kind, summary, details)
	}
}

func TestWizardRelayTargetsRedactsCredentialsAndQueries(t *testing.T) {
	got := wizardRelayTargets([]string{
		"wss://relay.example/path?token=secret",
		"wss://user:password@private.example#fragment",
		"://invalid",
	})
	want := []ui.KeyValue{
		{Key: "Relay", Value: "wss://relay.example/path"},
		{Key: "Relay", Value: "wss://private.example"},
		{Key: "Relay", Value: "invalid relay URL"},
	}
	if len(got) != len(want) {
		t.Fatalf("wizardRelayTargets() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("wizardRelayTargets()[%d] = %v, want %v", index, got[index], want[index])
		}
	}
}

func TestWizardCancellationSummaryStatesWhatChanged(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"App discovery stopped", "App discovery was cancelled. No changes were made"},
		{"Configuration update stopped", "Configuration update was cancelled. The previous zapstore.yaml was left unchanged"},
		{"Publication stopped", "Publishing setup was cancelled. Your saved configuration was left unchanged"},
	}
	for _, test := range tests {
		t.Run(test.title, func(t *testing.T) {
			if got := wizardCancellationSummary(test.title); got != test.want {
				t.Fatalf("wizardCancellationSummary(%q) = %q, want %q", test.title, got, test.want)
			}
		})
	}
}

func TestExistingOwnershipProofDetailsAbbreviatesHashAndFormatsExpiry(t *testing.T) {
	const hash = "d3bd3d00681ef7da9d157961477304ff86086ba6002354dbf6a3dc5ea3f7266a"
	got := existingOwnershipProofDetails(hash, time.Date(2028, 9, 14, 11, 57, 0, 0, time.Local))
	want := []ui.KeyValue{
		{Key: "Hash", Value: "d3bd3d…f7266a"},
		{Key: "Expires", Value: "14 Sep 2028"},
	}
	if len(got) != len(want) {
		t.Fatalf("existingOwnershipProofDetails() = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("existingOwnershipProofDetails()[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
	if got := existingOwnershipProofDetails("abcd", time.Time{}); len(got) != 1 || got[0] != (ui.KeyValue{Key: "Hash", Value: "abcd"}) {
		t.Fatalf("short hash without expiry = %#v", got)
	}
}

func TestCurrentProofDetailsUsesNpubAndISOExpiry(t *testing.T) {
	const owner = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	const hash = "d3bd3d00681ef7da9d157961477304ff86086ba6002354dbf6a3dc5ea3f7266a"
	npub, err := nip19.EncodePublicKey(owner)
	if err != nil {
		t.Fatal(err)
	}
	got := currentProofDetails(hash, owner, time.Date(2028, 9, 14, 11, 57, 0, 0, time.Local))
	want := []ui.KeyValue{
		{Key: "Nostr npub", Value: npub},
		{Key: "Signing certificate hash", Value: hash},
		{Key: "Expires", Value: "2028-09-14 11:57"},
	}
	if len(got) != len(want) {
		t.Fatalf("currentProofDetails() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("currentProofDetails()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestActiveProofForCertificateHash(t *testing.T) {
	secret := gonostr.GeneratePrivateKey()
	owner, err := gonostr.GetPublicKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	const certificateHash = "d3bd3d00681ef7da9d157961477304ff86086ba6002354dbf6a3dc5ea3f7266a"
	event := &gonostr.Event{
		Kind:      30509,
		PubKey:    owner,
		CreatedAt: gonostr.Now(),
		Tags: gonostr.Tags{
			{"d", certificateHash},
			{"signature", "c2ln"},
			{"expiry", fmt.Sprintf("%d", time.Now().Add(91*24*time.Hour).Unix())},
			{"delegation", strings.Repeat("a", 64)},
		},
	}
	if err := event.Sign(secret); err != nil {
		t.Fatal(err)
	}

	got, proof := activeProofForCertificateHash([]*gonostr.Event{event}, certificateHash)
	if got != event || proof == nil {
		t.Fatalf("activeProofForCertificateHash() = (%#v, %#v), want event and proof", got, proof)
	}
	if delegate := proofDelegate(got); delegate != strings.Repeat("a", 64) {
		t.Fatalf("proofDelegate() = %q", delegate)
	}
	state := wizardExistingIdentity([]*gonostr.Event{event}, certificateHash)
	if state == nil || state.owner != owner || state.delegate != strings.Repeat("a", 64) {
		t.Fatalf("wizardExistingIdentity() = %#v, want current proof owner and delegate", state)
	}
	event.Tags[2][1] = fmt.Sprintf("%d", time.Now().Add(89*24*time.Hour).Unix())
	if err := event.Sign(secret); err != nil {
		t.Fatal(err)
	}
	if state := wizardExistingIdentity([]*gonostr.Event{event}, certificateHash); state != nil {
		t.Fatalf("wizardExistingIdentity() = %#v, want nil for proof within renewal window", state)
	}
	if got, proof := activeProofForCertificateHash([]*gonostr.Event{event}, strings.Repeat("b", 64)); got != nil || proof != nil {
		t.Fatalf("mismatched certificate hash = (%#v, %#v), want nil", got, proof)
	}
}
