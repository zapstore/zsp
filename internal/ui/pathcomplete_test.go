package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPathCompletions(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, "release.jks")
	writeFile(t, "release.p12")
	writeFile(t, ".hidden")
	if err := os.Mkdir("testdata", 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join("testdata", "key.pem"))

	sep := string(filepath.Separator)

	tests := []struct {
		name  string
		typed string
		want  []string
	}{
		{
			name:  "unique file prefix",
			typed: "testdata" + sep + "k",
			want:  []string{filepath.Join("testdata", "key.pem")},
		},
		{
			name:  "directory gets trailing separator",
			typed: "testd",
			want:  []string{"testdata" + sep},
		},
		{
			name:  "dot-slash prefix is preserved",
			typed: "./testd",
			want:  []string{"./testdata" + sep},
		},
		{
			name:  "trailing separator lists directory",
			typed: "testdata" + sep,
			want:  []string{filepath.Join("testdata", "key.pem")},
		},
		{
			name:  "common prefix comes first when several match",
			typed: "rel",
			want:  []string{"release.", "release.jks", "release.p12"},
		},
		{
			name:  "hidden files stay hidden unless typed",
			typed: "",
			want:  []string{"release.jks", "release.p12", "testdata" + sep},
		},
		{
			name:  "dot prefix includes hidden files",
			typed: ".",
			want:  []string{".hidden"},
		},
		{
			name:  "missing directory yields nothing",
			typed: "missing" + sep + "file",
			want:  nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := pathCompletions(test.typed)
			if test.typed == "" || test.typed == "." {
				if !containsAll(got, test.want) {
					t.Fatalf("pathCompletions(%q) = %v, want to include %v", test.typed, got, test.want)
				}
				if slices.Contains(got, ".hidden") != (test.typed == ".") {
					t.Fatalf("pathCompletions(%q) hidden handling = %v", test.typed, got)
				}
				return
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("pathCompletions(%q) = %v, want %v", test.typed, got, test.want)
			}
		})
	}
}

func TestPathCompletions_HomePrefix(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) == 0 {
		t.Skip("home is unreadable or empty")
	}
	var visible string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		visible = entry.Name()
		break
	}
	if visible == "" {
		t.Skip("no visible home entries")
	}

	got := pathCompletions("~/" + visible[:1])
	wantPrefix := "~/" + visible[:1]
	found := false
	for _, match := range got {
		if strings.HasPrefix(match, wantPrefix) && strings.HasPrefix(match, "~/") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("pathCompletions(%q) = %v, want a ~/ suggestion", wantPrefix, got)
	}
}

func TestCommonPrefix(t *testing.T) {
	if got := commonPrefix([]string{"release.jks", "release.p12"}); got != "release." {
		t.Fatalf("commonPrefix() = %q, want release.", got)
	}
	if got := commonPrefix([]string{"alpha", "beta"}); got != "" {
		t.Fatalf("commonPrefix() = %q, want empty", got)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsAll(got, want []string) bool {
	for _, item := range want {
		if !slices.Contains(got, item) {
			return false
		}
	}
	return true
}
