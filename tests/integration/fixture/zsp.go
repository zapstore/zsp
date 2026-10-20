package fixture

import (
	"os"
	"strings"
)

// ZSP returns a zsp executable. ZSP_BINARY skips the build. ZSP_DIR overrides
// the repository root. When neither is set, the zsp module root is found by
// walking up from the working directory (the usual case when developing zsp).
func ZSP() (path string, cleanup func(), err error) {
	defaultDir := ""
	if strings.TrimSpace(os.Getenv("ZSP_DIR")) == "" && strings.TrimSpace(os.Getenv("ZSP_BINARY")) == "" {
		defaultDir, err = zspModuleRoot()
		if err != nil {
			return "", func() {}, err
		}
	}
	return binaryFromRepo("ZSP", "zsp", defaultDir, "./cmd/zsp", "-trimpath")
}
