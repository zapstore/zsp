//go:build release && !race

package release

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zapstore/zsp/tests/integration/fixture"
)

const (
	appID        = "dev.zapstore.releasetest"
	keystorePass = "storepass"
)

func buildUnsignedAPK(t *testing.T, directory string, versionCode int) string {
	t.Helper()
	return buildAppAPK(t, directory, appID, "Release Test", versionCode)
}

func buildAppAPK(t *testing.T, directory, id, label string, versionCode int) string {
	t.Helper()
	sdk := androidSDKRoot(t)
	aapt := androidTool(t, filepath.Join(sdk, "build-tools", "*", "aapt"))
	androidJar := androidTool(t, filepath.Join(sdk, "platforms", "android-*", "android.jar"))

	buildDirectory := filepath.Join(directory, strings.ReplaceAll(id, ".", "_"), strconv.Itoa(versionCode))
	if err := os.MkdirAll(buildDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(buildDirectory, "AndroidManifest.xml")
	manifestXML := fmt.Sprintf(
		`<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="%s" android:versionCode="%d" android:versionName="%d"><uses-sdk android:minSdkVersion="23" android:targetSdkVersion="35"/><application android:label="%s"/></manifest>`,
		id, versionCode, versionCode, label,
	)
	if err := os.WriteFile(manifest, []byte(manifestXML), 0o600); err != nil {
		t.Fatal(err)
	}
	libDir := filepath.Join(buildDirectory, "lib", "arm64-v8a")
	if err := os.MkdirAll(libDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libe2e.so"), []byte("e2e"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsigned := filepath.Join(buildDirectory, "app-release-unsigned.apk")
	run(t, buildDirectory, aapt, "package", "-f", "-M", manifest, "-I", androidJar, "-F", unsigned)
	run(t, buildDirectory, aapt, "add", "-f", unsigned, "lib/arm64-v8a/libe2e.so")
	return unsigned
}

func makeKeystore(t *testing.T, path string) {
	t.Helper()
	run(t, "", "keytool", "-genkeypair", "-alias", "release", "-keystore", path,
		"-storetype", "JKS", "-storepass", keystorePass, "-keypass", keystorePass,
		"-dname", "CN=zsp-release-test", "-keyalg", "RSA", "-keysize", "2048", "-validity", "1", "-noprompt")
}

func certificateDigest(t *testing.T, apk string) string {
	t.Helper()
	apksigner := androidTool(t, filepath.Join(androidSDKRoot(t), "build-tools", "*", "apksigner"))
	cmd := exec.Command(apksigner, "verify", "--print-certs", apk)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apksigner verify %s: %v\n%s", apk, err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if _, value, ok := strings.Cut(line, "certificate SHA-256 digest: "); ok {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("apksigner verify %s printed no certificate digest:\n%s", apk, out)
	return ""
}

func signWithKeystore(t *testing.T, keystore, unsigned, out string) {
	t.Helper()
	apksigner := androidTool(t, filepath.Join(androidSDKRoot(t), "build-tools", "*", "apksigner"))
	run(t, "", apksigner, "sign", "--ks", keystore, "--ks-key-alias", "release",
		"--ks-pass", "pass:"+keystorePass, "--key-pass", "pass:"+keystorePass, "--out", out, unsigned)
}

func androidSDKRoot(t *testing.T) string {
	t.Helper()
	root, err := fixture.AndroidSDKRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func androidTool(t *testing.T, pattern string) string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	for index := len(matches) - 1; index >= 0; index-- {
		if info, err := os.Stat(matches[index]); err == nil && !info.IsDir() {
			return matches[index]
		}
	}
	t.Fatalf("required Android tool not found: %s", pattern)
	return ""
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", filepath.Base(name), err, out)
	}
}
