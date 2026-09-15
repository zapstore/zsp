package ui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const pathCompletionLimit = 256

// pathCompletions returns filesystem paths that share the typed prefix so Huh
// can offer tab completion. Suggestions keep the caller's path form (./, ../,
// ~/) so they remain a prefix of the current input.
func pathCompletions(typed string) []string {
	if strings.ContainsAny(typed, "\n\x00") {
		return nil
	}
	dir, base := splitTypedPath(typed)
	entries, err := os.ReadDir(expandHome(dir))
	if err != nil {
		return nil
	}
	showHidden := strings.HasPrefix(base, ".")
	matches := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		matches = append(matches, completeTypedPath(typed, name, entry.IsDir()))
	}
	sort.Strings(matches)
	if len(matches) > pathCompletionLimit {
		matches = matches[:pathCompletionLimit]
	}
	if common := commonPrefix(matches); common != "" && common != typed && len(matches) > 1 {
		matches = append([]string{common}, matches...)
	}
	return matches
}

func splitTypedPath(typed string) (dir, base string) {
	if typed == "" || typed == "~" {
		if typed == "~" {
			return "~/", ""
		}
		return ".", ""
	}
	sep := strings.LastIndexAny(typed, `/\`)
	if sep < 0 {
		return ".", typed
	}
	dir = typed[:sep+1]
	if dir == "" {
		dir = "/"
	}
	return dir, typed[sep+1:]
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		if path == "" {
			return "."
		}
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func completeTypedPath(typed, name string, isDir bool) string {
	var completed string
	switch {
	case typed == "~":
		completed = "~/" + name
	case strings.HasSuffix(typed, "/") || strings.HasSuffix(typed, `\`):
		completed = typed + name
	default:
		if sep := strings.LastIndexAny(typed, `/\`); sep >= 0 {
			completed = typed[:sep+1] + name
		} else {
			completed = name
		}
	}
	if isDir && !strings.HasSuffix(completed, "/") && !strings.HasSuffix(completed, `\`) {
		completed += string(filepath.Separator)
	}
	return completed
}

func commonPrefix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	prefix := values[0]
	for _, value := range values[1:] {
		for !strings.HasPrefix(value, prefix) {
			if prefix == "" {
				return ""
			}
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
}
