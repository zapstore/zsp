package apk

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadZipFileRejectsOversizedResource(t *testing.T) {
	file := &zip.File{FileHeader: zip.FileHeader{
		Name:               "res/mipmap/icon.png",
		UncompressedSize64: maxAPKResourceSize + 1,
	}}
	if _, err := readZipFile(file); err == nil {
		t.Fatal("readZipFile() error = nil, want size-limit rejection")
	}
}

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

// certFingerprintFromPEM reads an x509 PEM certificate and returns its
// lowercase hex SHA-256 fingerprint, matching APKInfo.CertFingerprint's
// format. Fixtures under testdata/*.x509.pem come from the AOSP apksig
// project's test resources (Apache 2.0), used upstream by apkverifier's own
// test suite (https://android.googlesource.com/platform/tools/apksig).
func certFingerprintFromPEM(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("decode PEM %s: no block found", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate %s: %v", path, err)
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func TestParseRejectsMultipleCurrentSigners(t *testing.T) {
	// two-signers.apk is v1/v2 signed by two independent signers (rsa-2048
	// and ec-p256). apkverifier itself accepts this as valid; zsp must
	// additionally reject it because it has more than one current signer.
	path := filepath.Join("testdata", "two-signers.apk")

	if _, err := Parse(path); err == nil {
		t.Fatal("Parse() succeeded, want error for multiple current signers")
	} else if !strings.Contains(err.Error(), "current signer") {
		t.Errorf("Parse() error = %q, want it to mention multiple current signers", err.Error())
	}

	if _, err := ExtractCertificate(path); err == nil {
		t.Fatal("ExtractCertificate() succeeded, want error for multiple current signers")
	} else if !strings.Contains(err.Error(), "current signer") {
		t.Errorf("ExtractCertificate() error = %q, want it to mention multiple current signers", err.Error())
	}
}

func TestParseRejectsMalformedSignatures(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"v2 signature does not verify", "v2-only-with-rsa-pkcs1-sha256-2048-sig-does-not-verify.apk"},
		{"v3 signature does not verify", "v3-only-with-rsa-pkcs1-sha256-3072-sig-does-not-verify.apk"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join("testdata", tt.file)

			if _, err := Parse(path); err == nil {
				t.Fatal("Parse() succeeded, want error for malformed signature")
			}

			if _, err := ExtractCertificate(path); err == nil {
				t.Fatal("ExtractCertificate() succeeded, want error for malformed signature")
			}
		})
	}
}

func TestParseExposesValidatedV3SigningAncestors(t *testing.T) {
	// v1v2v3-with-rsa-2048-lineage-3-signers.apk rotates through three
	// certificates: rsa-2048 (oldest) -> rsa-2048_2 -> rsa-2048_3 (current).
	path := filepath.Join("testdata", "v1v2v3-with-rsa-2048-lineage-3-signers.apk")

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	current := certFingerprintFromPEM(t, filepath.Join("testdata", "rsa-2048_3.x509.pem"))
	oldest := certFingerprintFromPEM(t, filepath.Join("testdata", "rsa-2048.x509.pem"))
	middle := certFingerprintFromPEM(t, filepath.Join("testdata", "rsa-2048_2.x509.pem"))

	if info.CertFingerprint != current {
		t.Errorf("CertFingerprint = %q, want current signer %q", info.CertFingerprint, current)
	}

	wantAncestors := []string{oldest, middle}
	if len(info.SigningAncestors) != len(wantAncestors) {
		t.Fatalf("SigningAncestors = %v, want %v", info.SigningAncestors, wantAncestors)
	}
	for i, want := range wantAncestors {
		if info.SigningAncestors[i] != want {
			t.Errorf("SigningAncestors[%d] = %q, want %q", i, info.SigningAncestors[i], want)
		}
	}

	// The current certificate must not be repeated in the ancestor lineage.
	for _, ancestor := range info.SigningAncestors {
		if ancestor == info.CertFingerprint {
			t.Errorf("SigningAncestors contains the current certificate %q", ancestor)
		}
	}

	cert, err := ExtractCertificate(path)
	if err != nil {
		t.Fatalf("ExtractCertificate() error = %v", err)
	}
	sum := sha256.Sum256(cert.Raw)
	if got := hex.EncodeToString(sum[:]); got != current {
		t.Errorf("ExtractCertificate() cert fingerprint = %q, want %q", got, current)
	}
}

func TestParseNoRotationHasNoSigningAncestors(t *testing.T) {
	// None of the non-rotated fixtures should report ancestors; current
	// certificate semantics (CertFingerprint) must be unaffected.
	testdataDir := filepath.Join("..", "..", "testdata", "apks")
	path := filepath.Join(testdataDir, "sample.apk")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("test APK not found: %s", path)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if info.SigningAncestors != nil {
		t.Errorf("SigningAncestors = %v, want nil for a non-rotated APK", info.SigningAncestors)
	}
	if info.CertFingerprint == "" {
		t.Error("CertFingerprint is empty")
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
