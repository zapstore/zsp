package fixture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func binaryFromRepo(prefix, name, defaultDir, pkg string, extraBuildArgs ...string) (path string, cleanup func(), err error) {
	cleanup = func() {}
	binaryEnv := prefix + "_BINARY"
	dirEnv := prefix + "_DIR"
	if path = strings.TrimSpace(os.Getenv(binaryEnv)); path != "" {
		return path, cleanup, nil
	}
	dir := strings.TrimSpace(os.Getenv(dirEnv))
	if dir == "" {
		dir = defaultDir
	}
	if dir == "" {
		return "", cleanup, fmt.Errorf("set %s to an executable or %s to the repository root", binaryEnv, dirEnv)
	}
	tmp, err := os.MkdirTemp("", "zsp-"+name+"-bin-*")
	if err != nil {
		return "", cleanup, err
	}
	out := filepath.Join(tmp, name)
	args := append([]string{"build", "-buildvcs=false"}, extraBuildArgs...)
	args = append(args, "-o", out, pkg)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if runtime.GOOS == "darwin" {
		cmd.Env = append(cmd.Env, "CGO_LDFLAGS=-Wl,-no_warn_duplicate_libraries")
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(tmp)
		return "", cleanup, fmt.Errorf("build %s from %s=%q: %w", name, dirEnv, dir, err)
	}
	return out, func() { os.RemoveAll(tmp) }, nil
}
