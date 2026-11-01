package apk

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/avast/apkverifier"
)

// Identity is what an APK says about itself, read from its archive index, its
// manifest and its signing block alone.
//
// Nothing is verified. Parse hashes the whole file and checks every signature
// digest; Identity does neither, so it is the APK's claim, not a proof, and only
// fit to decide which APK is worth downloading.
type Identity struct {
	PackageID   string
	VersionName string
	VersionCode int64
	MinSDK      int32
	TargetSDK   int32

	// Architectures are the ABIs of lib/<abi>/ entries. Unlike Parse, it never
	// reads inside a library, so an APK that keeps native code elsewhere has none.
	Architectures []string

	// CertFingerprint is the SHA-256 (hex, lowercase) of the signing certificate
	// the APK names, chosen as Parse chooses it.
	CertFingerprint string
}

// ReadIdentity reads the identity of the APK in r, size bytes long. It touches
// the end of the file for the archive index and the signing block, and the
// entry holding the manifest, so over a [io.ReaderAt] that fetches on demand it
// costs a small part of the file.
//
// An APK with no signature, or with more than one current signer, is an error,
// as it is for Parse.
func ReadIdentity(ctx context.Context, r io.ReaderAt, size int64) (id *Identity, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			id, err = nil, malformedInput(recovered)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("open APK: %w", err)
	}
	manifest, err := newAPKArchive(zr.File).parseManifest()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	chains, err := apkverifier.ExtractCertsReader(io.NewSectionReader(r, 0, size), nil)
	if err != nil && len(chains) == 0 {
		return nil, fmt.Errorf("read signing certificate: %w", err)
	}
	switch {
	case len(chains) == 0:
		return nil, errors.New("read signing certificate: no signature found")
	case len(chains) > 1:
		return nil, fmt.Errorf("read signing certificate: found %d current signers, expected exactly one", len(chains))
	}
	_, cert := apkverifier.PickBestApkCert(chains)
	if cert == nil {
		return nil, errors.New("read signing certificate: no certificate found")
	}
	sum := sha256.Sum256(cert.Raw)

	return &Identity{
		PackageID:       manifest.PackageID,
		VersionName:     manifest.VersionName,
		VersionCode:     manifest.VersionCode,
		MinSDK:          manifest.MinSDK,
		TargetSDK:       manifest.TargetSDK,
		Architectures:   libraryABIs(zr.File),
		CertFingerprint: hex.EncodeToString(sum[:]),
	}, nil
}

// libraryABIs returns the ABIs named by lib/<abi>/ entries, sorted.
func libraryABIs(files []*zip.File) []string {
	seen := make(map[string]struct{})
	for _, f := range files {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if !isValidZipEntryPath(name) {
			continue
		}
		if abi, ok := abiFromLibPath(name); ok {
			seen[abi] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	abis := make([]string, 0, len(seen))
	for abi := range seen {
		abis = append(abis, abi)
	}
	sort.Strings(abis)
	return abis
}
