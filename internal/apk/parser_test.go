package apk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	// Find testdata directory
	testdataDir := filepath.Join("..", "..", "testdata", "apks")

	tests := []struct {
		name        string
		apkFile     string
		wantPackage string
		wantArm64   bool
		wantErr     bool
	}{
		{
			name:        "sample apk",
			apkFile:     "sample.apk",
			wantPackage: "", // Will be set based on actual APK content
			wantArm64:   true,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(testdataDir, tt.apkFile)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("test APK not found: %s", path)
			}

			info, err := Parse(path)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			// Basic validation
			if info.PackageID == "" {
				t.Error("Parse() PackageID is empty")
			}
			if info.VersionName == "" {
				t.Error("Parse() VersionName is empty")
			}
			if info.CertFingerprint == "" {
				t.Error("Parse() CertFingerprint is empty")
			}
			if len(info.CertFingerprint) != 64 {
				t.Errorf("Parse() CertFingerprint has wrong length: %d", len(info.CertFingerprint))
			}
			if info.SHA256 == "" {
				t.Error("Parse() SHA256 is empty")
			}
			if len(info.SHA256) != 64 {
				t.Errorf("Parse() SHA256 has wrong length: %d", len(info.SHA256))
			}
			if info.FileSize == 0 {
				t.Error("Parse() FileSize is 0")
			}

			t.Logf("Parsed APK:\n%s", info.String())
		})
	}
}

func TestParseAllTestAPKs(t *testing.T) {
	testdataDir := filepath.Join("..", "..", "testdata", "apks")

	entries, err := os.ReadDir(testdataDir)
	if err != nil {
		t.Skipf("cannot read testdata directory: %v", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".apk" {
			t.Run(entry.Name(), func(t *testing.T) {
				path := filepath.Join(testdataDir, entry.Name())
				info, err := Parse(path)
				if err != nil {
					t.Errorf("Parse(%s) failed: %v", entry.Name(), err)
					return
				}

				// Basic sanity checks
				if info.PackageID == "" {
					t.Error("PackageID is empty")
				}
				if info.CertFingerprint == "" {
					t.Error("CertFingerprint is empty")
				}

				ver := info.VersionName
				if ver != "" && !strings.HasPrefix(ver, "v") {
					ver = "v" + ver
				}
				t.Logf("%s: %s %s (%d) - archs: %v - label: %q",
					entry.Name(), info.PackageID, ver, info.VersionCode, info.Architectures, info.Label)
			})
		}
	}
}

func TestIsArm64(t *testing.T) {
	tests := []struct {
		name  string
		archs []string
		want  bool
	}{
		{"arm64 only", []string{"arm64-v8a"}, true},
		{"multiple including arm64", []string{"armeabi-v7a", "arm64-v8a", "x86_64"}, true},
		{"x86 only", []string{"x86", "x86_64"}, false},
		{"armeabi-v7a only", []string{"armeabi-v7a"}, false},
		{"no native libs (pure Java)", []string{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &APKInfo{Architectures: tt.archs}
			if got := info.IsArm64(); got != tt.want {
				t.Errorf("IsArm64() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHashFile(t *testing.T) {
	// Create a temporary file with known content
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("hello world")
	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	hash, err := hashFile(tmpFile)
	if err != nil {
		t.Fatalf("hashFile() error: %v", err)
	}

	// SHA256 of "hello world"
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if hash != expected {
		t.Errorf("hashFile() = %q, want %q", hash, expected)
	}
}
