package config

import "testing"

func TestForgeRepoPathNormalization(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantGitHub string
		wantGitea  string
		wantGitLab string
	}{
		{"plain", "https://example.com/owner/repo", "", "owner/repo", "owner/repo"},
		{"git suffix", "https://example.com/owner/repo.git", "", "owner/repo", "owner/repo"},
		{"git suffix with trailing slash", "https://example.com/owner/repo.git/", "", "owner/repo", "owner/repo"},
		{"empty repo segment", "https://example.com/owner/", "", "", ""},
		{"only owner", "https://example.com/owner", "", "", ""},
		{"empty owner segment", "https://example.com//repo", "", "", ""},
		{"repo is only a git suffix", "https://example.com/owner/.git", "", "", ""},
		{"github host", "https://github.com/owner/repo.git", "owner/repo", "owner/repo", "owner/repo"},
		{"github empty repo", "https://github.com/owner/", "", "", ""},
		{"gitlab subgroup with git suffix", "https://example.com/group/sub/repo.git", "", "group/sub", "group/sub/repo"},
		{"gitlab release page", "https://example.com/group/repo.git/-/releases", "", "group/repo", "group/repo"},
		{"gitlab empty middle segment", "https://example.com/group//repo", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetGitHubRepo(tt.url); got != tt.wantGitHub {
				t.Errorf("GetGitHubRepo(%q) = %q, want %q", tt.url, got, tt.wantGitHub)
			}
			if _, got := GetGiteaRepo(tt.url); got != tt.wantGitea {
				t.Errorf("GetGiteaRepo(%q) repoPath = %q, want %q", tt.url, got, tt.wantGitea)
			}
			if _, got := GetGitLabRepoWithBase(tt.url); got != tt.wantGitLab {
				t.Errorf("GetGitLabRepoWithBase(%q) repoPath = %q, want %q", tt.url, got, tt.wantGitLab)
			}
		})
	}
}
