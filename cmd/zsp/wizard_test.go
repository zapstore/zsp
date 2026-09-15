package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	publiczsp "github.com/zapstore/zsp"
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

	cfg := publiczsp.Config{FetchConfig: publiczsp.FetchConfig{
		Repository: "https://github.com/example/app",
	}}
	candidate := &publiczsp.APK{
		SourceURL:       "https://github.com/example/app/releases/download/v1/app.apk",
		CertificateHash: strings.Repeat("a", 64),
	}
	if got := wizardDiscover(t.Context(), cfg, candidate); got != server.URL {
		t.Fatalf("wizardDiscover() = %q, want %q", got, server.URL)
	}

	status = http.StatusBadRequest
	if got := wizardDiscover(t.Context(), cfg, candidate); got != "" {
		t.Fatalf("wizardDiscover() after rejection = %q, want empty", got)
	}
}

func TestWizardConfigFromSource(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		repository    string
		releaseSource string
		wantError     bool
	}{
		{
			name:       "repository without scheme",
			source:     "github.com/example/app",
			repository: "https://github.com/example/app",
		},
		{
			name:          "release source",
			source:        "https://downloads.example.com/app/releases",
			releaseSource: "https://downloads.example.com/app/releases",
		},
		{
			name:      "invalid source",
			source:    "http://example.com/releases",
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := wizardConfigFromSource(test.source)
			if test.wantError {
				if err == nil {
					t.Fatal("wizardConfigFromSource() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("wizardConfigFromSource() error = %v", err)
			}
			if got.config.Repository != test.repository {
				t.Errorf("Repository = %q, want %q", got.config.Repository, test.repository)
			}
			var releaseSource string
			if got.config.ReleaseSource != nil {
				releaseSource = got.config.ReleaseSource.URL
			}
			if releaseSource != test.releaseSource {
				t.Errorf("ReleaseSource.URL = %q, want %q", releaseSource, test.releaseSource)
			}
		})
	}
}

func TestWizardConfigFromSourceAcceptsRelativeLocalAPK(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "app.apk")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	got, err := wizardConfigFromSource("app.apk")
	if err != nil {
		t.Fatalf("wizardConfigFromSource() error = %v", err)
	}
	if got.config.ReleaseSource == nil {
		t.Fatal("ReleaseSource is nil")
	}
	gotInfo, err := os.Stat(got.config.ReleaseSource.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("ReleaseSource = %#v, want local path %q", got.config.ReleaseSource, path)
	}
}

func TestSaveWizardYAMLPreservesUneditedFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "zapstore.yaml")
	if err := os.WriteFile(path, []byte("repository: https://github.com/example/app\nrelease_filter: ^v\nlicense: MIT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, document, err := loadWizardYAML(path, &publiczsp.APK{Name: "Example"})
	if err != nil {
		t.Fatal(err)
	}
	metadata.name = "Example"
	if err := saveWizardYAML(path, document, metadata, publiczsp.Config{
		FetchConfig: publiczsp.FetchConfig{Repository: "https://github.com/example/app"},
	}, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"repository: https://github.com/example/app", "release_filter: ^v", "license: MIT", "name: Example"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("saved configuration does not contain %q:\n%s", want, data)
		}
	}
}

func TestWizardFailureSummaryDoesNotExposeInternalErrors(t *testing.T) {
	got := wizardFailureSummary("Configuration setup stopped")
	want := "Couldn't use zapstore.yaml. Fix its configuration and try again; it was left unchanged."
	if got != want {
		t.Errorf("wizardFailureSummary() = %q, want %q", got, want)
	}
}

func TestWizardCancellationSummaryStatesWhatChanged(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"App discovery stopped", "App discovery was cancelled. No changes were made."},
		{"Configuration update stopped", "Configuration update was cancelled. The previous zapstore.yaml was left unchanged."},
		{"Publication stopped", "Publishing setup was cancelled. Your saved configuration was left unchanged."},
	}
	for _, test := range tests {
		t.Run(test.title, func(t *testing.T) {
			if got := wizardCancellationSummary(test.title); got != test.want {
				t.Fatalf("wizardCancellationSummary(%q) = %q, want %q", test.title, got, test.want)
			}
		})
	}
}
