package release

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zapstore/zsp/internal/apk"
)

// FindUnsigned walks dir and returns every valid unsigned APK, in walk order.
// A signed APK and a file that is not a valid APK are omitted.
func FindUnsigned(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	var found []string
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".apk") {
			return nil
		}
		unsigned, err := apk.Unsigned(path)
		if err != nil || !unsigned {
			return nil
		}
		found = append(found, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}
