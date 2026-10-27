package fixture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Hotbox returns a hotbox executable. HOTBOX_BINARY skips the build; otherwise
// HOTBOX_DIR must point at the hotbox repository root.
//
// The executable embeds token/pkcs11.module. go build uses whatever file is
// there, and a cross-compiled tree may hold a Linux module. Compile the
// module for this process, build, then put the previous file back.
func Hotbox() (path string, cleanup func(), err error) {
	if path = strings.TrimSpace(os.Getenv("HOTBOX_BINARY")); path != "" {
		return path, func() {}, nil
	}
	dir := strings.TrimSpace(os.Getenv("HOTBOX_DIR"))
	if dir == "" {
		return "", func() {}, fmt.Errorf("set HOTBOX_BINARY to an executable or HOTBOX_DIR to the repository root")
	}
	restore, err := compilePKCS11Module(dir)
	if err != nil {
		return "", func() {}, err
	}
	path, cleanup, err = binaryFromRepo("HOTBOX", "hotbox", "", ".")
	if err != nil {
		restore()
		return "", func() {}, err
	}
	prev := cleanup
	return path, func() {
		restore()
		prev()
	}, nil
}

// compilePKCS11Module replaces token/pkcs11.module with a library for this
// OS and architecture. The returned function writes the previous bytes back.
func compilePKCS11Module(dir string) (func(), error) {
	module := filepath.Join(dir, "token", "pkcs11.module")
	original, err := os.ReadFile(module)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	temporary := module + ".zsp-build"
	name, args := pkcs11Compile(temporary, filepath.Join(dir, "pkcs11", "hotbox.c"))
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(temporary)
		return nil, fmt.Errorf("compile hotbox pkcs11 module: %w\n%s", err, output)
	}
	if err := os.Rename(temporary, module); err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	return func() {
		if !existed {
			_ = os.Remove(module)
			return
		}
		_ = os.WriteFile(module, original, 0o755)
	}, nil
}

func pkcs11Compile(out, src string) (string, []string) {
	if runtime.GOOS == "darwin" {
		return "cc", []string{
			"-dynamiclib", "-O2", "-Wall", "-Wextra", "-Wno-cast-function-type-mismatch",
			"-Wl,-install_name,@rpath/libhotbox-pkcs11.dylib",
			"-o", out, src,
		}
	}
	target := "x86_64-linux-gnu"
	if runtime.GOARCH == "arm64" {
		target = "aarch64-linux-gnu"
	}
	return "zig", []string{
		"cc", "-target", target, "-shared", "-fPIC", "-O2", "-Wall", "-Wextra",
		"-Wno-cast-function-type-mismatch", "-o", out, src,
	}
}
