package apk

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
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

				t.Logf("%s: %s v%s (%d) - archs: %v - label: %q",
					entry.Name(), info.PackageID, info.VersionName, info.VersionCode, info.Architectures, info.Label)
			})
		}
	}
}

func TestParseBraveAPI37Manifest(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "apks", "BraveMonoarm64.apk")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("test APK not found: %s", path)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if info.PackageID != "com.brave.browser" {
		t.Errorf("PackageID = %q, want %q", info.PackageID, "com.brave.browser")
	}
	if info.VersionName != "1.92.140" {
		t.Errorf("VersionName = %q, want %q", info.VersionName, "1.92.140")
	}
	if info.VersionCode != 429214004 {
		t.Errorf("VersionCode = %d, want %d", info.VersionCode, 429214004)
	}
	if info.TargetSDK != 36 {
		t.Errorf("TargetSDK = %d, want %d", info.TargetSDK, 36)
	}
	if info.Label != "Brave" {
		t.Errorf("Label = %q, want %q", info.Label, "Brave")
	}
	if !info.IsArm64() {
		t.Errorf("IsArm64() = false, want true; architectures = %v", info.Architectures)
	}
}

func TestParseAmberAdaptiveIcon(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "apks", "amber-arm64-v8a-v6.3.0.apk")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("test APK not found: %s", path)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if info.Label != "Amber" {
		t.Errorf("Label = %q, want %q", info.Label, "Amber")
	}

	icon, _, err := image.Decode(bytes.NewReader(info.Icon))
	if err != nil {
		t.Fatalf("decode icon: %v", err)
	}
	if got, want := icon.Bounds().Dx(), 512; got != want {
		t.Errorf("icon width = %d, want %d", got, want)
	}
	red, green, blue, _ := icon.At(256, 256).RGBA()
	if red <= green || green <= blue {
		t.Errorf("icon center color = (%d, %d, %d), want Amber's yellow foreground", red, green, blue)
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

func TestIsWatch(t *testing.T) {
	tests := []struct {
		name     string
		features []string
		want     bool
	}{
		{"Wear OS watch", []string{"android.hardware.type.watch"}, true},
		{"phone", []string{"android.hardware.camera"}, false},
		{"no declared features", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &APKInfo{Features: tt.features}
			if got := info.IsWatch(); got != tt.want {
				t.Errorf("IsWatch() = %v, want %v", got, tt.want)
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
