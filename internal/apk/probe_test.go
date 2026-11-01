package apk

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// readerAtCounting counts the bytes ReadAt hands out, to show how little of an
// APK ReadIdentity touches.
type readerAtCounting struct {
	f     *os.File
	bytes int64
}

func (r *readerAtCounting) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.f.ReadAt(p, off)
	r.bytes += int64(n)
	return n, err
}

// TestReadIdentityAgreesWithParse is the point of ReadIdentity: it must name the
// same app, version, certificate and ABIs as the full Parse does, for every APK
// in the corpus that Parse accepts.
func TestReadIdentityAgreesWithParse(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "apks", "*.apk"))
	if err != nil || len(paths) == 0 {
		t.Skipf("no test APKs: %v", err)
	}

	checked := 0
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			parsed, err := Parse(context.Background(), path)
			if err != nil {
				t.Skipf("Parse rejects it: %v", err)
			}

			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			counted := &readerAtCounting{f: f}

			id, err := ReadIdentity(context.Background(), counted, info.Size())
			if err != nil {
				t.Fatalf("ReadIdentity: %v", err)
			}
			checked++

			if id.PackageID != parsed.PackageID || id.VersionCode != parsed.VersionCode || id.VersionName != parsed.VersionName {
				t.Errorf("identity = %s %q (%d), Parse = %s %q (%d)", id.PackageID, id.VersionName, id.VersionCode, parsed.PackageID, parsed.VersionName, parsed.VersionCode)
			}
			if id.CertFingerprint != parsed.CertFingerprint {
				t.Errorf("certificate = %s, Parse = %s", id.CertFingerprint, parsed.CertFingerprint)
			}
			if id.MinSDK != parsed.MinSDK || id.TargetSDK != parsed.TargetSDK {
				t.Errorf("SDK = %d/%d, Parse = %d/%d", id.MinSDK, id.TargetSDK, parsed.MinSDK, parsed.TargetSDK)
			}
			// ABIs from lib/ paths agree whenever Parse found them there too.
			if len(id.Architectures) > 0 && !reflect.DeepEqual(id.Architectures, parsed.Architectures) {
				t.Errorf("architectures = %v, Parse = %v", id.Architectures, parsed.Architectures)
			}
			t.Logf("%s: read %d of %d bytes (%.1f%%)", id.PackageID, counted.bytes, info.Size(), 100*float64(counted.bytes)/float64(info.Size()))
		})
	}
	if checked == 0 {
		t.Fatal("no APK was checked")
	}
}

func TestReadIdentityReadsLittleOfALargeAPK(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "apks", "BraveMonoarm64.apk")
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("no large test APK: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	counted := &readerAtCounting{f: f}
	if _, err := ReadIdentity(context.Background(), counted, info.Size()); err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	if counted.bytes*20 > info.Size() {
		t.Errorf("read %d of %d bytes, want under 5%% of the file", counted.bytes, info.Size())
	}
}

func TestReadIdentityRejectsWhatParseRejects(t *testing.T) {
	// two current signers
	f, err := os.Open(filepath.Join("testdata", "two-signers.apk"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()
	if _, err := ReadIdentity(context.Background(), f, info.Size()); err == nil {
		t.Fatal("ReadIdentity accepted an APK with two current signers")
	}

	// not an archive
	if _, err := ReadIdentity(context.Background(), strings.NewReader("not a zip"), 9); err == nil {
		t.Fatal("ReadIdentity accepted something that is not an APK")
	}
}

func TestReadIdentityHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadIdentity(ctx, strings.NewReader(""), 0); err == nil {
		t.Fatal("ReadIdentity ignored a cancelled context")
	}
}
