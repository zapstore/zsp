package release

import (
	"path/filepath"
	"strings"
	"testing"
)

const keepChangelog = `# Changelog

All notable changes are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [1.1.0] - 2023-03-05

### Added

- New feature

### Fixed

- A bug

## [1.0.0] - 2019-12-18 [YANKED]

### Removed

- Old thing

[unreleased]: https://example.com/compare/v1.1.0...HEAD
[1.1.0]: https://example.com/compare/v1.0.0...v1.1.0
[1.0.0]: https://example.com/compare/v0.1.0...v1.0.0
`

func TestChangelogSection(t *testing.T) {
	fenced := "## [1.0.0] - 2024-01-01\n\n### Added\n\n" +
		"```\n## [0.1.0]\nnot a heading\n```\n\n- real item\n\n" +
		"## [0.9.0] - 2023-01-01\n\n### Fixed\n\n- old\n"
	tests := []struct {
		name       string
		text       string
		versions   []string
		want       string
		changelog  bool
		found      bool
	}{
		{
			name: "plain notes",
			text: "Bug fixes\n",
			want: "Bug fixes\n",
		},
		{
			name: "markdown without versions",
			text: "## Fixes\n\n- crash\n",
			want: "## Fixes\n\n- crash\n",
		},
		{
			name:      "matching release",
			text:      keepChangelog,
			versions:  []string{"1.1.0"},
			want:      "### Added\n\n- New feature\n\n### Fixed\n\n- A bug",
			changelog: true,
			found:     true,
		},
		{
			name:      "leading v",
			text:      keepChangelog,
			versions:  []string{"v1.1.0"},
			want:      "### Added\n\n- New feature\n\n### Fixed\n\n- A bug",
			changelog: true,
			found:     true,
		},
		{
			name:      "older yanked release drops link refs",
			text:      keepChangelog,
			versions:  []string{"1.0.0"},
			want:      "### Removed\n\n- Old thing",
			changelog: true,
			found:     true,
		},
		{
			name:      "crlf",
			text:      strings.ReplaceAll(keepChangelog, "\n", "\r\n"),
			versions:  []string{"1.1.0"},
			want:      "### Added\n\n- New feature\n\n### Fixed\n\n- A bug",
			changelog: true,
			found:     true,
		},
		{
			name:      "version in the heading",
			text:      "## [v1.1.0] - 2023-03-05\n\n- item\n",
			versions:  []string{"1.1.0"},
			want:      "- item",
			changelog: true,
			found:     true,
		},
		{
			name:      "no date",
			text:      "# Changelog\n\n## [1.0.0-rc2]\n\n- Feature: status\n\n## [1.0.0-rc1]\n\n- older\n",
			versions:  []string{"1.0.0-rc2"},
			want:      "- Feature: status",
			changelog: true,
			found:     true,
		},
		{
			name:      "inline link",
			text:      "## [1.1.0](https://example.com/compare/v1.0.0...v1.1.0) - 2023-03-05\n\n### Fixed\n\n- crash\n",
			versions:  []string{"1.1.0"},
			want:      "### Fixed\n\n- crash",
			changelog: true,
			found:     true,
		},
		{
			name:      "date in parentheses",
			text:      "## [1.1.0](https://example.com/compare/v1.0.0...v1.1.0) (2023-03-05)\n\n### Fixed\n\n- crash\n",
			versions:  []string{"1.1.0"},
			want:      "### Fixed\n\n- crash",
			changelog: true,
			found:     true,
		},
		{
			name:      "four part version",
			text:      "## [1.2.3.4] - 2024-01-01\n\n- build\n",
			versions:  []string{"1.2.3.4"},
			want:      "- build",
			changelog: true,
			found:     true,
		},
		{
			name:      "does not prefix match",
			text:      "## [1.0.0] - 2024-01-01\n\n- exact\n\n## [1.0] - 2023-01-01\n\n- short\n\n## [1.0.0-rc1] - 2023-06-01\n\n- pre\n",
			versions:  []string{"1.0"},
			want:      "- short",
			changelog: true,
			found:     true,
		},
		{
			name:      "prerelease stays distinct",
			text:      "## [1.0.0] - 2024-01-01\n\n- exact\n\n## [1.0.0-rc1] - 2023-06-01\n\n- pre\n",
			versions:  []string{"1.0.0-rc1"},
			want:      "- pre",
			changelog: true,
			found:     true,
		},
		{
			name:      "fenced heading is not a section",
			text:      fenced,
			versions:  []string{"1.0.0"},
			want:      "### Added\n\n```\n## [0.1.0]\nnot a heading\n```\n\n- real item",
			changelog: true,
			found:     true,
		},
		{
			name:      "second version is used when the first misses",
			text:      "## [1.0.8] - 2026-10-17\n\n### Fixed\n\n- crash\n",
			versions:  []string{"nightly", "1.0.8"},
			want:      "### Fixed\n\n- crash",
			changelog: true,
			found:     true,
		},
		{
			name:      "missing section",
			text:      keepChangelog,
			versions:  []string{"9.9.9"},
			changelog: true,
		},
		{
			name:      "empty unreleased section",
			text:      keepChangelog,
			versions:  []string{"Unreleased"},
			changelog: true,
			found:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changelog, found := changelogSection(tt.text, tt.versions...)
			if got != tt.want || changelog != tt.changelog || found != tt.found {
				t.Fatalf("changelogSection(%v) = (%q, %v, %v), want (%q, %v, %v)", tt.versions, got, changelog, found, tt.want, tt.changelog, tt.found)
			}
		})
	}
}

func TestReleaseNotesFromChangelogWarnsWhenSectionMissing(t *testing.T) {
	notes, warning := releaseNotesFromChangelog(keepChangelog, "1.0.0-beta", "2.0.0")
	if notes != "" || warning != "no changelog section for 2.0.0 or 1.0.0-beta" {
		t.Fatalf("releaseNotesFromChangelog() = %q, %q", notes, warning)
	}
	notes, warning = releaseNotesFromChangelog(keepChangelog, "1.2.3", "v1.2.3")
	if notes != "" || warning != "no changelog section for v1.2.3" {
		t.Fatalf("duplicate versions: notes %q warning %q", notes, warning)
	}
	notes, warning = releaseNotesFromChangelog("just notes", "1.0.0", "v9")
	if notes != "just notes" || warning != "" {
		t.Fatalf("plain notes = %q, %q", notes, warning)
	}
}

func TestLoadReleaseNotesExtractsChangelogSection(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "CHANGELOG.md")
	writeTestFile(t, path, "# Changelog\n\n## [1.0.0] - 2024-01-01\n\n### Fixed\n\n- crash\n\n## [0.9.0] - 2023-01-01\n\n### Added\n\n- start\n")
	got, warning, err := loadReleaseNotes(t.Context(), base, path, "1.0.0", "v1.0.0")
	if err != nil || warning != "" {
		t.Fatalf("loadReleaseNotes() = %q, warning %q, err %v", got, warning, err)
	}
	want := "### Fixed\n\n- crash"
	if got != want {
		t.Fatalf("loadReleaseNotes() = %q, want %q", got, want)
	}
}

func TestLoadReleaseNotesWarnsWhenChangelogSectionMissing(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "CHANGELOG.md")
	writeTestFile(t, path, "## [1.0.0] - 2024-01-01\n\n- notes\n")
	got, warning, err := loadReleaseNotes(t.Context(), base, path, "2.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("loadReleaseNotes() = %q, want empty", got)
	}
	if warning != "no changelog section for 2.0.0" {
		t.Fatalf("warning = %q", warning)
	}
}
