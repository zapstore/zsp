package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const zspModule = "github.com/zapstore/zsp/v2"

// zspModuleRoot is the directory containing this zsp module's go.mod and cmd/zsp.
func zspModuleRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if isZSPModuleRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("zsp module root not found from %s", wd)
		}
		dir = parent
	}
}

func isZSPModuleRoot(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	first := strings.TrimSpace(strings.Split(string(data), "\n")[0])
	if first != "module "+zspModule {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "cmd", "zsp"))
	return err == nil && info.IsDir()
}
