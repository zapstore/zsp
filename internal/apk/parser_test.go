package apk

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/zapstore/zsp/v2/internal/media"
)

func TestReadZipFileRejectsOversizedResource(t *testing.T) {
	file := &zip.File{FileHeader: zip.FileHeader{
		Name:               "res/mipmap/icon.png",
		UncompressedSize64: maxAPKResourceSize + 1,
	}}
	if _, err := readZipFile(file, maxAPKResourceSize); err == nil {
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

			info, err := Parse(t.Context(), path)
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
				info, err := Parse(t.Context(), path)
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

	info, err := Parse(t.Context(), path)
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

	info, err := Parse(t.Context(), path)
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

// Resource-shrunk APKs hide their density variants behind obfuscated names, so
// these only reach full size through the resource table's density configs.
func TestParseIconWidth(t *testing.T) {
	tests := []struct {
		apkFile  string
		minWidth int
	}{
		{"ironfox-152.0.6-arm64-v8a.apk", 192},
		{"misty-breez-breez-signed.apk", 192},
		{"exiferaser.apk", 512},
		{"mempal-v1.5.3.apk", 512},
	}
	for _, tt := range tests {
		t.Run(tt.apkFile, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "apks", tt.apkFile)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("test APK not found: %s", path)
			}
			info, err := Parse(t.Context(), path)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			config, _, err := image.DecodeConfig(bytes.NewReader(info.Icon))
			if err != nil {
				t.Fatalf("decode icon: %v", err)
			}
			if config.Width < tt.minWidth {
				t.Errorf("icon width = %d, want at least %d", config.Width, tt.minWidth)
			}
		})
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

	if _, err := Parse(t.Context(), path); err == nil {
		t.Fatal("Parse() succeeded, want error for multiple current signers")
	} else if !strings.Contains(err.Error(), "current signer") {
		t.Errorf("Parse() error = %q, want it to mention multiple current signers", err.Error())
	}

	if _, err := ExtractCertificate(t.Context(), path); err == nil {
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

			if _, err := Parse(t.Context(), path); err == nil {
				t.Fatal("Parse() succeeded, want error for malformed signature")
			}

			if _, err := ExtractCertificate(t.Context(), path); err == nil {
				t.Fatal("ExtractCertificate() succeeded, want error for malformed signature")
			}
		})
	}
}

func TestParseExposesValidatedV3SigningAncestors(t *testing.T) {
	// v1v2v3-with-rsa-2048-lineage-3-signers.apk rotates through three
	// certificates: rsa-2048 (oldest) -> rsa-2048_2 -> rsa-2048_3 (current).
	path := filepath.Join("testdata", "v1v2v3-with-rsa-2048-lineage-3-signers.apk")

	info, err := Parse(t.Context(), path)
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

	cert, err := ExtractCertificate(t.Context(), path)
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

	info, err := Parse(t.Context(), path)
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

	hash, err := hashFile(t.Context(), tmpFile)
	if err != nil {
		t.Fatalf("hashFile() error: %v", err)
	}

	// SHA256 of "hello world"
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if hash != expected {
		t.Errorf("hashFile() = %q, want %q", hash, expected)
	}
}

func TestExtractArchitectures(t *testing.T) {
	arm64ELF := elfBytes(elfClass64, emAARCH64, false)
	x86ELF := elfBytes(elfClass32, em386, false)

	tests := []struct {
		name  string
		files map[string][]byte
		want  []string
	}{
		{
			name:  "lib directory names the abi even when the library is not elf",
			files: map[string][]byte{"lib/arm64-v8a/libe2e.so": []byte("e2e")},
			want:  []string{"arm64-v8a"},
		},
		{
			name:  "abi directory is case insensitive",
			files: map[string][]byte{"lib/ARM64-V8A/libe2e.so": []byte("e2e")},
			want:  []string{"arm64-v8a"},
		},
		{
			name:  "split config alias",
			files: map[string][]byte{"lib/arm64_v8a/libe2e.so": []byte("e2e")},
			want:  []string{"arm64-v8a"},
		},
		{
			name: "several lib directories",
			files: map[string][]byte{
				"lib/x86_64/liba.so":      []byte("x"),
				"lib/armeabi-v7a/libb.so": []byte("a"),
			},
			want: []string{"armeabi-v7a", "x86_64"},
		},
		{
			name:  "elf header when the path names no abi",
			files: map[string][]byte{"lib/libapp.so": arm64ELF},
			want:  []string{"arm64-v8a"},
		},
		{
			name:  "elf header of a library outside lib",
			files: map[string][]byte{"assets/plugin.so": x86ELF},
			want:  []string{"x86"},
		},
		{
			name: "elf fallback collects every machine",
			files: map[string][]byte{
				"lib/liba.so":    arm64ELF,
				"assets/libb.so": x86ELF,
			},
			want: []string{"arm64-v8a", "x86"},
		},
		{
			name: "lib abi is the install set and other elf blobs are ignored",
			files: map[string][]byte{
				"lib/arm64-v8a/liba.so": []byte("not elf"),
				"assets/plugin.so":      x86ELF,
			},
			want: []string{"arm64-v8a"},
		},
		{
			name:  "unknown directory falls back to the elf machine",
			files: map[string][]byte{"lib/unknown/libfoo.so": arm64ELF},
			want:  []string{"arm64-v8a"},
		},
		{
			name:  "big endian elf",
			files: map[string][]byte{"jni/libapp.so": elfBytes(elfClass64, emAARCH64, true)},
			want:  []string{"arm64-v8a"},
		},
		{
			name:  "elf class must match the machine",
			files: map[string][]byte{"lib/libapp.so": elfBytes(elfClass32, emAARCH64, false)},
		},
		{
			name:  "pure java apk",
			files: map[string][]byte{"classes.dex": []byte("dex")},
		},
		{
			name:  "empty abi directory is not an abi",
			files: map[string][]byte{"lib/arm64-v8a/": nil},
		},
		{
			name:  "non library file under lib is not an abi",
			files: map[string][]byte{"lib/kotlin/readme.txt": []byte("hi")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractArchitectures(openZipFiles(t, writeABIZip(t, tt.files)))
			if !slices.Equal(got, tt.want) {
				t.Errorf("extractArchitectures() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractArchitecturesBackslashPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abi.apk")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: `lib\arm64-v8a\libe2e.so`, Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("e2e")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got := extractArchitectures(openZipFiles(t, path))
	want := []string{"arm64-v8a"}
	if !slices.Equal(got, want) {
		t.Errorf("extractArchitectures() = %v, want %v", got, want)
	}
}

func openZipFiles(t *testing.T, path string) []*zip.File {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r.File
}

func writeABIZip(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "abi.apk")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func elfBytes(class byte, machine uint16, big bool) []byte {
	hdr := make([]byte, 20)
	hdr[0], hdr[1], hdr[2], hdr[3] = 0x7f, 'E', 'L', 'F'
	hdr[4] = class
	if big {
		hdr[5] = elfData2MSB
		hdr[18] = byte(machine >> 8)
		hdr[19] = byte(machine)
		return hdr
	}
	hdr[5] = elfData2LSB
	hdr[18] = byte(machine)
	hdr[19] = byte(machine >> 8)
	return hdr
}

func manifestTokens(t *testing.T, attrs map[string]map[string]string) *manifestCollector {
	t.Helper()
	collector := &manifestCollector{}
	for _, element := range []string{"manifest", "uses-sdk", "application"} {
		start := xml.StartElement{Name: xml.Name{Local: element}}
		for name, value := range attrs[element] {
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: name}, Value: value})
		}
		if err := collector.EncodeToken(start); err != nil {
			t.Fatal(err)
		}
	}
	return collector
}

func TestManifestCollectorResult(t *testing.T) {
	tests := []struct {
		name     string
		manifest map[string]string
		sdk      map[string]string
		wantCode int64
		wantMin  int32
		wantErr  string
	}{
		{name: "valid", manifest: map[string]string{"versionCode": "12"}, sdk: map[string]string{"minSdkVersion": "24", "targetSdkVersion": "35"}, wantCode: 12, wantMin: 24},
		{name: "hex version code", manifest: map[string]string{"versionCode": "0x10"}, wantCode: 16},
		{name: "missing version code", manifest: map[string]string{}, wantErr: "versionCode is missing"},
		{name: "non-numeric version code", manifest: map[string]string{"versionCode": "abc"}, wantErr: "not a number"},
		{name: "zero version code", manifest: map[string]string{"versionCode": "0"}, wantErr: "must be positive"},
		{name: "negative version code", manifest: map[string]string{"versionCode": "-5"}, wantErr: "must be positive"},
		{name: "no uses-sdk", manifest: map[string]string{"versionCode": "1"}, wantCode: 1},
		{name: "negative minSdk", manifest: map[string]string{"versionCode": "1"}, sdk: map[string]string{"minSdkVersion": "-1"}, wantErr: "minSdkVersion"},
		{name: "negative targetSdk", manifest: map[string]string{"versionCode": "1"}, sdk: map[string]string{"targetSdkVersion": "-30"}, wantErr: "targetSdkVersion"},
		{name: "minSdk over int32", manifest: map[string]string{"versionCode": "1"}, sdk: map[string]string{"minSdkVersion": "4294967296"}, wantErr: "minSdkVersion"},
		{name: "targetSdk over int64", manifest: map[string]string{"versionCode": "1"}, sdk: map[string]string{"targetSdkVersion": "99999999999999999999"}, wantErr: "targetSdkVersion"},
		{name: "preview codename is not a level", manifest: map[string]string{"versionCode": "1"}, sdk: map[string]string{"minSdkVersion": "VanillaIceCream"}, wantCode: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.manifest["package"] = "dev.example"
			collector := manifestTokens(t, map[string]map[string]string{"manifest": tt.manifest, "uses-sdk": tt.sdk})
			got, err := collector.result()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("result() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("result() error = %v", err)
			}
			if got.VersionCode != tt.wantCode || got.MinSDK != tt.wantMin {
				t.Errorf("result() = code %d min %d, want code %d min %d", got.VersionCode, got.MinSDK, tt.wantCode, tt.wantMin)
			}
		})
	}
}

func TestManifestCollectorRequiresPackage(t *testing.T) {
	collector := manifestTokens(t, map[string]map[string]string{"manifest": {"versionCode": "1"}})
	if _, err := collector.result(); err == nil {
		t.Fatal("result() error = nil, want missing package")
	}
}

func TestParseRejectsMalformedArchives(t *testing.T) {
	tests := []struct {
		name  string
		files map[string][]byte
	}{
		{"missing manifest", map[string][]byte{"classes.dex": []byte("dex")}},
		{"garbage manifest", map[string][]byte{"AndroidManifest.xml": []byte("\x03\x00\x08\x00\xff\xff\xff\xff garbage")}},
		{"truncated manifest header", map[string][]byte{"AndroidManifest.xml": {0x03, 0x00}}},
		{"garbage resource table", map[string][]byte{
			"AndroidManifest.xml": []byte("\x03\x00\x08\x00\x10\x00\x00\x00 garbage garbage"),
			"resources.arsc":      bytes.Repeat([]byte{0xff}, 256),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeABIZip(t, tt.files)
			info, err := Parse(t.Context(), path)
			if err == nil {
				t.Fatalf("Parse(%s) = %+v, want error", tt.name, info)
			}
		})
	}
}

func TestParseHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	path := filepath.Join("..", "..", "testdata", "apks", "sample.apk")
	if _, err := Parse(ctx, path); !errors.Is(err, context.Canceled) {
		t.Errorf("Parse(cancelled) error = %v, want context.Canceled", err)
	}
	if _, err := ExtractCertificate(ctx, path); !errors.Is(err, context.Canceled) {
		t.Errorf("ExtractCertificate(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestParseReturnsSigningCertificate(t *testing.T) {
	path := filepath.Join("testdata", "v1v2v3-with-rsa-2048-lineage-3-signers.apk")
	info, err := Parse(t.Context(), path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if info.Certificate == nil {
		t.Fatal("Parse() Certificate = nil")
	}
	sum := sha256.Sum256(info.Certificate.Raw)
	if got := hex.EncodeToString(sum[:]); got != info.CertFingerprint {
		t.Errorf("Certificate fingerprint = %s, want %s", got, info.CertFingerprint)
	}
}

func TestIsValidZipEntryPath(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"res/mipmap-hdpi/ic_launcher.png", true},
		{"lib/arm64-v8a/", true},
		{"AndroidManifest.xml", true},
		{"../evil", false},
		{"res/../../evil", false},
		{"/etc/passwd", false},
		{"res//double.png", false},
		{"res/./dot.png", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidZipEntryPath(tt.name); got != tt.want {
				t.Errorf("isValidZipEntryPath(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestParseColor(t *testing.T) {
	tests := []struct {
		in     string
		want   color.NRGBA
		wantOK bool
	}{
		{"#112233", color.NRGBA{0x11, 0x22, 0x33, 0xff}, true},
		{"#80112233", color.NRGBA{0x11, 0x22, 0x33, 0x80}, true},
		{"#fff", color.NRGBA{0xff, 0xff, 0xff, 0xff}, true},
		{"#8f00", color.NRGBA{0xff, 0x00, 0x00, 0x88}, true},
		{"112233", color.NRGBA{}, false},
		{"#12345", color.NRGBA{}, false},
		{"#gggggg", color.NRGBA{}, false},
		{"", color.NRGBA{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := parseColor(tt.in)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("parseColor(%q) = %v, %v, want %v, %v", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestSVGPaint(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		alpha float64
		want  string
	}{
		{"fill", "#112233", 1, `fill="#112233"`},
		{"fill", "#ff112233", 1, `fill="#112233"`},
		{"fill", "#80112233", 1, `fill="#112233" fill-opacity="0.502"`},
		{"fill", "#00112233", 1, `fill="#112233" fill-opacity="0.000"`},
		{"fill", "#112233", 0.5, `fill="#112233" fill-opacity="0.500"`},
		{"fill", "-65536", 1, `fill="#ff0000"`},
		{"fill", "16711680", 1, `fill="#ff0000" fill-opacity="0.000"`},
		{"fill", "?attr/colorPrimary", 1, `fill="none"`},
		{"fill", "", 1, `fill="none"`},
		{"stroke", "#112233", 1, `stroke="#112233"`},
	}
	for _, tt := range tests {
		t.Run(tt.name+tt.in, func(t *testing.T) {
			if got := svgPaint(tt.name, tt.in, tt.alpha); got != tt.want {
				t.Errorf("svgPaint(%q, %q, %g) = %s, want %s", tt.name, tt.in, tt.alpha, got, tt.want)
			}
		})
	}
}

func TestAdaptiveLayerColorIsOpaque(t *testing.T) {
	layer, err := (&apkArchive{}).adaptiveLayer("#80112233")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := layer.At(0, 0), (color.RGBA{0x11, 0x22, 0x33, 0xff}); got != want {
		t.Errorf("adaptiveLayer() color = %v, want %v", got, want)
	}
	if _, ok := (&apkArchive{}).resolveColor("@7f010000"); ok {
		t.Error("resolveColor(reference) without a table = ok, want failure")
	}
}

func TestComposeAdaptiveIconCropsToMask(t *testing.T) {
	background := image.NewUniform(color.RGBA{0x10, 0x0b, 0x15, 0xff})
	foreground := image.NewNRGBA(image.Rect(0, 0, 108, 108))
	// Red sits in the 18-pixel margin launchers mask off, blue in the center.
	foreground.SetNRGBA(5, 5, color.NRGBA{R: 0xff, A: 0xff})
	for y := 44; y < 64; y++ {
		for x := 44; x < 64; x++ {
			foreground.SetNRGBA(x, y, color.NRGBA{B: 0xff, A: 0xff})
		}
	}
	img := composeAdaptiveIcon([]image.Image{background, foreground})
	if got := img.Bounds().Dx(); got != 72 {
		t.Fatalf("width = %d, want 72", got)
	}
	if got, want := img.RGBAAt(0, 0), (color.RGBA{0x10, 0x0b, 0x15, 0xff}); got != want {
		t.Errorf("corner = %v, want background %v", got, want)
	}
	if got := img.RGBAAt(36, 36); got.B < 0xf0 || got.R > 0x10 {
		t.Errorf("center = %v, want foreground blue", got)
	}
}

// IronFox fills its fox with a red-to-yellow gradient resource, whose
// coordinates are in the vector's 32-unit viewport, not pixels.
func TestParseVectorGradient(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "apks", "ironfox-152.0.6-arm64-v8a.apk")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("test APK not found: %s", path)
	}
	info, err := Parse(t.Context(), path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	icon, _, err := image.Decode(bytes.NewReader(info.Icon))
	if err != nil {
		t.Fatalf("decode icon: %v", err)
	}
	upperLeft := color.NRGBAModel.Convert(icon.At(130, 200)).(color.NRGBA)
	lowerRight := color.NRGBAModel.Convert(icon.At(360, 330)).(color.NRGBA)
	if int(lowerRight.G)-int(upperLeft.G) < 40 {
		t.Errorf("gradient green %d -> %d, want red at upper left turning yellow at lower right", upperLeft.G, lowerRight.G)
	}
}

// Adaptive icons must keep their declared background, whether a color
// resource or a color behind a bitmap foreground.
func TestParseAdaptiveIconBackground(t *testing.T) {
	tests := []struct {
		apkFile  string
		minWidth int
		corner   color.RGBA
	}{
		{"exiferaser.apk", 512, color.RGBA{0x4d, 0xb6, 0xac, 0xff}},
		{"Armada-v0.66.5.apk", 193, color.RGBA{0x10, 0x0b, 0x15, 0xff}},
		{"mempal-v1.5.3.apk", 512, color.RGBA{0x0f, 0x21, 0x35, 0xff}},
		{"ironfox-152.0.6-arm64-v8a.apk", 512, color.RGBA{0xff, 0xff, 0xff, 0xff}},
	}
	for _, tt := range tests {
		t.Run(tt.apkFile, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "apks", tt.apkFile)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("test APK not found: %s", path)
			}
			info, err := Parse(t.Context(), path)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			icon, _, err := image.Decode(bytes.NewReader(info.Icon))
			if err != nil {
				t.Fatalf("decode icon: %v", err)
			}
			if got := icon.Bounds().Dx(); got < tt.minWidth {
				t.Errorf("icon width = %d, want at least %d", got, tt.minWidth)
			}
			if got := color.RGBAModel.Convert(icon.At(0, 0)); got != tt.corner {
				t.Errorf("icon corner = %v, want %v", got, tt.corner)
			}
		})
	}
}

func TestToPNGBoundsDecoding(t *testing.T) {
	var small bytes.Buffer
	if err := png.Encode(&small, image.NewNRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	if got, err := toPNG(small.Bytes()); err != nil || !bytes.Equal(got, small.Bytes()) {
		t.Fatalf("toPNG(PNG) = %d bytes, %v, want the input unchanged", len(got), err)
	}
	var huge bytes.Buffer
	if err := png.Encode(&huge, image.NewGray(image.Rect(0, 0, 7000, 7000))); err != nil {
		t.Fatal(err)
	}
	if _, err := toPNG(huge.Bytes()); !errors.Is(err, media.ErrInvalidImage) {
		t.Fatalf("toPNG(7000x7000) error = %v, want media.ErrInvalidImage", err)
	}
	if _, err := toPNG([]byte("not an image")); !errors.Is(err, media.ErrInvalidImage) {
		t.Fatalf("toPNG(junk) error = %v, want media.ErrInvalidImage", err)
	}
}

func TestReadZipFileEnforcesLimitOnActualBytes(t *testing.T) {
	path := writeABIZip(t, map[string][]byte{"big.bin": bytes.Repeat([]byte{1}, 2048)})
	files := openZipFiles(t, path)
	if _, err := readZipFile(files[0], 1024); err == nil {
		t.Fatal("readZipFile() error = nil, want size-limit rejection")
	}
	if data, err := readZipFile(files[0], 2048); err != nil || len(data) != 2048 {
		t.Fatalf("readZipFile() = %d bytes, %v, want 2048 bytes", len(data), err)
	}
}
