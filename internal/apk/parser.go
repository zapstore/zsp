// Package apk handles APK parsing, metadata extraction, and signature verification.
package apk

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/avast/apkparser"
	"github.com/avast/apkverifier"
	"github.com/shogo82148/androidbinary"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"

	"github.com/zapstore/zsp/internal/media"
)

const (
	// maxAPKResourceSize bounds individual manifest resources and icons read
	// into memory. APKs can be large, but these auxiliary resources should be
	// small.
	maxAPKResourceSize = 16 * 1024 * 1024 // 16MB

	// maxResourceTableSize bounds resources.arsc, which is read whole. Large
	// browsers ship tables of tens of megabytes.
	maxResourceTableSize = 128 * 1024 * 1024 // 128MB

	hashChunkSize = 1024 * 1024
)

// APKInfo contains extracted metadata from an APK file.
type APKInfo struct {
	// Package identifier (e.g., "com.example.app")
	PackageID string

	// Version information
	VersionName string
	VersionCode int64

	// SDK versions
	MinSDK    int32
	TargetSDK int32

	// App metadata
	Label string // App name

	// Native architectures (e.g., ["arm64-v8a", "armeabi-v7a"]).
	// Empty when no ABI can be detected. Publication still emits an f tag.
	Architectures []string

	// Required device features declared by the manifest.
	Features []string

	// Certificate is the verified current signing certificate.
	Certificate *x509.Certificate

	// Certificate SHA-256 fingerprint (hex encoded, lowercase) of the
	// current signing certificate.
	CertFingerprint string

	// SigningAncestors holds the SHA-256 fingerprints (hex encoded, lowercase)
	// of validated v3 signing-certificate-rotation ancestors, ordered oldest
	// to newest. The current certificate (CertFingerprint) is not repeated
	// here. It is nil unless the APK's v3/v3.1 signing block carries a
	// verified rotation lineage.
	SigningAncestors []string

	// certificates is the verified chain, oldest first, ending at Certificate.
	certificates []*x509.Certificate

	// Icon PNG bytes (nil if not found or extraction failed)
	Icon []byte

	// File information
	FilePath string
	FileSize int64
	SHA256   string
}

// Parse extracts metadata from an APK file. It rejects an APK that is
// malformed, has an invalid version code or SDK level, or fails signature
// verification. ctx is checked between phases.
func Parse(ctx context.Context, apkPath string) (info *APKInfo, err error) {
	defer func() {
		if r := recover(); r != nil {
			info, err = nil, malformedInput(r)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	fi, err := os.Stat(apkPath)
	if err != nil {
		return nil, fmt.Errorf("stat APK: %w", err)
	}
	sha256Hash, err := hashFile(ctx, apkPath)
	if err != nil {
		return nil, fmt.Errorf("hash APK: %w", err)
	}

	zr, err := zip.OpenReader(apkPath)
	if err != nil {
		return nil, fmt.Errorf("open APK: %w", err)
	}
	defer zr.Close()
	archive := newAPKArchive(zr.File)

	// An unreadable resource table only leaves references unresolved: the
	// label falls back and the icon is found by name.
	archive.table, _ = archive.loadResourceTable()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manifest, err := archive.parseManifest()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	info = &APKInfo{
		PackageID:     manifest.PackageID,
		VersionName:   manifest.VersionName,
		VersionCode:   manifest.VersionCode,
		MinSDK:        manifest.MinSDK,
		TargetSDK:     manifest.TargetSDK,
		Label:         manifest.Label,
		Features:      manifest.Features,
		Architectures: extractArchitectures(zr.File),
		FilePath:      apkPath,
		FileSize:      fi.Size(),
		SHA256:        sha256Hash,
	}

	// Verify v1/v2/v3 signatures, reject malformed signatures and multiple
	// current signers, and extract the current certificate fingerprint plus
	// any validated signing-rotation ancestors.
	sig, err := verifySignature(apkPath)
	if err != nil {
		return nil, err
	}
	info.Certificate = sig.cert
	info.CertFingerprint = sig.fingerprint()
	info.SigningAncestors = sig.ancestors
	info.certificates = sig.chain
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Icon extraction failure is not fatal.
	if icon, err := archive.extractIcon(manifest); err == nil {
		info.Icon = icon
	}
	return info, nil
}

func malformedInput(recovered any) error {
	return fmt.Errorf("parse APK: malformed input: %v", recovered)
}

type manifestInfo struct {
	PackageID   string
	VersionName string
	VersionCode int64
	MinSDK      int32
	TargetSDK   int32
	Label       string
	// Icon is the icon resource resolved through the resource table, and
	// IconRef the same icon as an unresolved "@<id>" reference. Either may be
	// empty.
	Icon     string
	IconRef  string
	Features []string
}

// manifestCollector records the fields zsp needs from an Android manifest.
// apkparser supplies already-decoded XML tokens and resolves string resources
// when resources.arsc can be parsed. Numeric fields stay raw until result so
// a missing value is told apart from a malformed one.
type manifestCollector struct {
	raw struct {
		packageID, versionName, versionCode string
		minSDK, targetSDK                   string
		label, icon                         string
	}
	features []string
}

func (c *manifestCollector) EncodeToken(token xml.Token) error {
	start, ok := token.(xml.StartElement)
	if !ok {
		return nil
	}

	switch start.Name.Local {
	case "manifest":
		c.raw.packageID = attribute(start, "package")
		c.raw.versionName = attribute(start, "versionName")
		c.raw.versionCode = attribute(start, "versionCode")
	case "uses-sdk":
		c.raw.minSDK = attribute(start, "minSdkVersion")
		c.raw.targetSDK = attribute(start, "targetSdkVersion")
	case "application":
		c.raw.label = attribute(start, "label")
		c.raw.icon = attribute(start, "icon")
	case "uses-feature":
		if feature := attribute(start, "name"); feature != "" {
			c.features = append(c.features, feature)
		}
	}

	return nil
}

func (c *manifestCollector) Flush() error {
	return nil
}

// result validates the collected fields. Label may still be an unresolved
// resource reference.
func (c *manifestCollector) result() (manifestInfo, error) {
	if c.raw.packageID == "" {
		return manifestInfo{}, errors.New("package is missing")
	}
	versionCode, err := parseVersionCode(c.raw.versionCode)
	if err != nil {
		return manifestInfo{}, err
	}
	minSDK, err := parseSDKLevel("minSdkVersion", c.raw.minSDK)
	if err != nil {
		return manifestInfo{}, err
	}
	targetSDK, err := parseSDKLevel("targetSdkVersion", c.raw.targetSDK)
	if err != nil {
		return manifestInfo{}, err
	}
	return manifestInfo{
		PackageID:   c.raw.packageID,
		VersionName: c.raw.versionName,
		VersionCode: versionCode,
		MinSDK:      minSDK,
		TargetSDK:   targetSDK,
		Label:       c.raw.label,
		Icon:        c.raw.icon,
		Features:    c.features,
	}, nil
}

func parseVersionCode(value string) (int64, error) {
	if value == "" {
		return 0, errors.New("versionCode is missing")
	}
	code, err := strconv.ParseInt(value, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("versionCode %q is not a number", value)
	}
	if code <= 0 {
		return 0, fmt.Errorf("versionCode %d must be positive", code)
	}
	return code, nil
}

// parseSDKLevel returns 0 for an absent level. A level that is not a number,
// such as a preview codename, is also 0: it names no usable API level.
func parseSDKLevel(name, value string) (int32, error) {
	if value == "" {
		return 0, nil
	}
	level, err := strconv.ParseInt(value, 0, 64)
	if errors.Is(err, strconv.ErrRange) {
		return 0, fmt.Errorf("%s %q is out of range", name, value)
	}
	if err != nil {
		return 0, nil
	}
	if level < 0 || level > math.MaxInt32 {
		return 0, fmt.Errorf("%s %d is out of range", name, level)
	}
	return int32(level), nil
}

// apkArchive is an opened APK shared by the manifest and icon readers.
type apkArchive struct {
	files  []*zip.File
	byName map[string]*zip.File
	// table is nil when resources.arsc is missing or unreadable.
	table *apkparser.ResourceTable
	// tableData is resources.arsc as read, kept for the density lookup.
	tableData []byte
	// densityTable is parsed on first use by icon extraction; it stays nil
	// when resources.arsc is missing or unreadable.
	densityTable     *androidbinary.TableFile
	densityTableDone bool
}

// newAPKArchive indexes entries by name. Names that could escape a directory
// are left out, and the first entry wins for a duplicated name.
func newAPKArchive(files []*zip.File) *apkArchive {
	byName := make(map[string]*zip.File, len(files))
	for _, f := range files {
		if !isValidZipEntryPath(f.Name) {
			continue
		}
		if _, duplicate := byName[f.Name]; !duplicate {
			byName[f.Name] = f
		}
	}
	return &apkArchive{files: files, byName: byName}
}

func (a *apkArchive) readEntry(name string, limit int64) ([]byte, error) {
	f := a.byName[name]
	if f == nil {
		return nil, fmt.Errorf("%s is missing", name)
	}
	return readZipFile(f, limit)
}

func (a *apkArchive) loadResourceTable() (*apkparser.ResourceTable, error) {
	data, err := a.readEntry("resources.arsc", maxResourceTableSize)
	if err != nil {
		return nil, err
	}
	table, err := apkparser.ParseResourceTable(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse resources.arsc: %w", err)
	}
	a.tableData = data
	return table, nil
}

func (a *apkArchive) parseManifest() (manifestInfo, error) {
	data, err := a.readEntry("AndroidManifest.xml", maxAPKResourceSize)
	if err != nil {
		return manifestInfo{}, fmt.Errorf("read manifest: %w", err)
	}
	collector := &manifestCollector{}
	if err := apkparser.ParseXml(bytes.NewReader(data), collector, a.table); err != nil {
		return manifestInfo{}, fmt.Errorf("parse AndroidManifest.xml: %w", err)
	}
	info, err := collector.result()
	if err != nil {
		return manifestInfo{}, fmt.Errorf("parse AndroidManifest.xml: %w", err)
	}
	if isResourceReference(info.Label) {
		label, err := a.resolveResourceString(info.Label)
		if err != nil || isResourceReference(label) {
			label = ""
		}
		info.Label = label
	}
	info.IconRef = a.iconReference(data)
	return info, nil
}

// iconReference parses the manifest without the resource table, which leaves
// the icon as an "@<id>" reference. The table resolves it to a single config,
// so the ID is needed to ask for the others.
func (a *apkArchive) iconReference(manifest []byte) string {
	if a.table == nil {
		return ""
	}
	collector := &manifestCollector{}
	if err := apkparser.ParseXml(bytes.NewReader(manifest), collector, nil); err != nil {
		return ""
	}
	if !isResourceReference(collector.raw.icon) {
		return ""
	}
	return collector.raw.icon
}

// isResourceReference reports whether a manifest value looks like an Android
// resource ID. apkparser may leave these unresolved when the resource table
// uses a format it cannot decode.
func isResourceReference(value string) bool {
	return strings.HasPrefix(value, "@")
}

// resolveResourceString resolves a resource reference through resources.arsc.
// Resource aliases are common in current Android applications, for example
// app_name -> app_name_release -> "Amber".
func (a *apkArchive) resolveResourceString(value string) (string, error) {
	resourceID, err := parseResourceID(value)
	if err != nil {
		return "", err
	}
	return a.resolveResource(resourceID, apkparser.ConfigFirst)
}

// resolveResource follows a resource and its aliases to a string using the
// given config of the first entry.
func (a *apkArchive) resolveResource(resourceID uint32, config apkparser.ResourceConfigOption) (string, error) {
	if a.table == nil {
		return "", errors.New("resources.arsc is unavailable")
	}
	for range 16 {
		entry, err := a.table.GetResourceEntryEx(resourceID, config)
		if err != nil {
			return "", fmt.Errorf("get resource %08x: %w", resourceID, err)
		}
		value, err := entry.GetValue().Data()
		if err != nil {
			return "", fmt.Errorf("read resource %08x: %w", resourceID, err)
		}
		switch value := value.(type) {
		case string:
			return value, nil
		case uint32:
			resourceID = value
		default:
			return "", fmt.Errorf("resource %08x is not a string", resourceID)
		}
	}
	return "", errors.New("resource reference chain is too deep")
}

func attribute(start xml.StartElement, name string) string {
	for _, attr := range start.Attr {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func parseResourceID(value string) (uint32, error) {
	value = strings.TrimPrefix(value, "@")
	resourceID, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("parse resource ID %q: %w", value, err)
	}
	return uint32(resourceID), nil
}

// hashFile calculates the SHA256 hash of a file, checking ctx between chunks.
func hashFile(ctx context.Context, name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, hashChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read: %w", err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ELF identification and e_machine values used to name an Android ABI when
// a .so is not stored under lib/<abi>/.
const (
	elfClass32  = 1
	elfClass64  = 2
	elfData2LSB = 1
	elfData2MSB = 2

	em386     = 3
	emMIPS    = 8
	emARM     = 40
	emX86_64  = 62
	emAARCH64 = 183
	emRISCV   = 243
)

// extractArchitectures returns the ABIs of native code in an APK.
// Android installs libraries from lib/<abi>/. A .so stored elsewhere is
// classified from its ELF header. Nil means no ABI could be detected,
// including a pure Java/Kotlin APK.
func extractArchitectures(files []*zip.File) []string {
	archSet := make(map[string]struct{})
	var elfCandidates []*zip.File
	for _, f := range files {
		// Zip names must use slashes. Reject the normalized name so a
		// backslash traversal cannot be treated as a library path.
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if !isValidZipEntryPath(name) {
			continue
		}
		if abi, ok := abiFromLibPath(name); ok {
			archSet[abi] = struct{}{}
			continue
		}
		if hasSharedObjectSuffix(name) {
			elfCandidates = append(elfCandidates, f)
		}
	}
	// lib/<abi>/ is the install ABI. Consult ELF only when that path
	// named nothing, so an unrelated .so in assets does not add an ABI.
	if len(archSet) == 0 {
		for _, f := range elfCandidates {
			if abi, ok := abiFromELF(f); ok {
				archSet[abi] = struct{}{}
			}
		}
	}
	if len(archSet) == 0 {
		return nil
	}

	archs := make([]string, 0, len(archSet))
	for arch := range archSet {
		archs = append(archs, arch)
	}
	sort.Strings(archs)
	return archs
}

// abiFromLibPath reports the Android ABI of a file stored at lib/<abi>/….
func abiFromLibPath(name string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.ToLower(name), "lib/")
	if !ok {
		return "", false
	}
	abi, file, found := strings.Cut(rest, "/")
	if !found || file == "" {
		return "", false
	}
	return canonicalABI(abi)
}

func canonicalABI(name string) (string, bool) {
	switch name {
	case "arm64-v8a", "arm64_v8a", "arm64", "aarch64":
		return "arm64-v8a", true
	case "armeabi-v7a", "armeabi_v7a":
		return "armeabi-v7a", true
	case "armeabi":
		return "armeabi", true
	case "x86":
		return "x86", true
	case "x86_64", "x86-64", "amd64":
		return "x86_64", true
	case "mips":
		return "mips", true
	case "mips64":
		return "mips64", true
	case "riscv64":
		return "riscv64", true
	default:
		return "", false
	}
}

func hasSharedObjectSuffix(name string) bool {
	return len(name) >= 3 && strings.EqualFold(name[len(name)-3:], ".so")
}

// abiFromELF reads the ELF class and e_machine. It does not read past the
// header, so a large stored library is not loaded.
func abiFromELF(f *zip.File) (string, bool) {
	rc, err := f.Open()
	if err != nil {
		return "", false
	}
	defer rc.Close()

	var hdr [20]byte
	if _, err := io.ReadFull(rc, hdr[:]); err != nil {
		return "", false
	}
	if hdr[0] != 0x7f || hdr[1] != 'E' || hdr[2] != 'L' || hdr[3] != 'F' {
		return "", false
	}
	class := hdr[4]
	var machine uint16
	switch hdr[5] {
	case elfData2LSB:
		machine = binary.LittleEndian.Uint16(hdr[18:20])
	case elfData2MSB:
		machine = binary.BigEndian.Uint16(hdr[18:20])
	default:
		return "", false
	}
	switch machine {
	case emAARCH64:
		if class == elfClass64 {
			return "arm64-v8a", true
		}
	case emARM:
		if class == elfClass32 {
			return "armeabi-v7a", true
		}
	case em386:
		if class == elfClass32 {
			return "x86", true
		}
	case emX86_64:
		if class == elfClass64 {
			return "x86_64", true
		}
	case emMIPS:
		switch class {
		case elfClass64:
			return "mips64", true
		case elfClass32:
			return "mips", true
		}
	case emRISCV:
		if class == elfClass64 {
			return "riscv64", true
		}
	}
	return "", false
}

// isValidZipEntryPath validates a zip entry path to prevent zip slip attacks.
// Returns false if the path contains directory traversal sequences or is absolute.
func isValidZipEntryPath(name string) bool {
	// Reject paths with directory traversal
	if strings.Contains(name, "..") {
		return false
	}
	// Reject absolute paths
	if strings.HasPrefix(name, "/") {
		return false
	}
	// Reject paths that clean to something different (indicates traversal).
	// Zip names use slashes on every OS, so path.Clean applies, not filepath.
	cleaned := path.Clean(name)
	if cleaned != name && cleaned+"/" != name {
		// Allow trailing slash difference but nothing else
		if !strings.HasSuffix(name, "/") || cleaned != strings.TrimSuffix(name, "/") {
			return false
		}
	}
	return true
}

// verifiedSignature is the outcome of a robust v1/v2/v3 APK signature
// verification: exactly one current signing certificate, plus any validated
// v3 signing-certificate-rotation lineage ending at that certificate.
type verifiedSignature struct {
	cert      *x509.Certificate
	ancestors []string            // SHA-256 hex fingerprints, oldest to newest, excluding cert
	chain     []*x509.Certificate // oldest first, ending at cert
}

func (s *verifiedSignature) fingerprint() string {
	sum := sha256.Sum256(s.cert.Raw)
	return hex.EncodeToString(sum[:])
}

// verifySignature verifies the APK's v1, v2, and v3 signatures using the full
// supported SDK range. It rejects malformed signatures (any error surfaced by
// the underlying verifier, including missing/mismatched certificates, digest
// mismatches, and downgrade-attack detection) and rejects APKs with more than
// one current signer, since zsp's identity model requires a single current
// signing certificate.
// Unsigned reports whether path has no APK signature. A present but invalid
// signature is an error, not an unsigned file.
func Unsigned(path string) (bool, error) {
	_, err := verifySignature(path)
	if err == nil {
		return false, nil
	}
	if unsignedVerification(err) {
		return true, nil
	}
	return false, err
}

func unsignedVerification(err error) bool {
	message := err.Error()
	return strings.Contains(message, "No signatures") || strings.Contains(message, "not signed") || strings.Contains(message, "no valid certificate")
}

func verifySignature(apkPath string) (*verifiedSignature, error) {
	res, err := apkverifier.Verify(apkPath, nil)
	if err != nil {
		return nil, fmt.Errorf("verify signature: %w", err)
	}

	if len(res.SignerCerts) == 0 {
		return nil, errors.New("verify signature: no valid certificate found")
	}
	if len(res.SignerCerts) > 1 {
		return nil, fmt.Errorf("verify signature: found %d current signers, expected exactly one", len(res.SignerCerts))
	}

	// Pick the best certificate from the (single) signer's chain.
	_, cert := apkverifier.PickBestApkCert(res.SignerCerts)
	if cert == nil {
		return nil, errors.New("verify signature: no valid certificate found")
	}

	ancestors, err := signingAncestors(res, cert)
	if err != nil {
		return nil, fmt.Errorf("verify signature: %w", err)
	}

	return &verifiedSignature{cert: cert, ancestors: ancestors, chain: signingChain(res, cert)}, nil
}

// signingChain is the verified lineage, oldest first, ending at current.
// A file with no rotation is a chain of one.
func signingChain(res apkverifier.Result, current *x509.Certificate) []*x509.Certificate {
	if res.SigningBlockResult == nil || res.SigningBlockResult.SigningLineage == nil {
		return []*x509.Certificate{current}
	}
	nodes := res.SigningBlockResult.SigningLineage.Nodes
	if len(nodes) <= 1 {
		return []*x509.Certificate{current}
	}
	chain := make([]*x509.Certificate, 0, len(nodes))
	for _, node := range nodes {
		if node.SigningCert != nil {
			chain = append(chain, node.SigningCert)
		}
	}
	if len(chain) == 0 {
		return []*x509.Certificate{current}
	}
	return chain
}

// signingAncestors returns the validated v3 signing-certificate-rotation
// lineage, oldest to newest, excluding the current certificate. It returns
// nil when the APK carries no v3/v3.1 rotation lineage. The underlying
// verifier cryptographically validates every lineage link and confirms the
// current certificate terminates the chain before Verify returns success, so
// any mismatch found here indicates an inconsistency between schemes and is
// treated as a verification failure rather than silently ignored.
func signingAncestors(res apkverifier.Result, current *x509.Certificate) ([]string, error) {
	if res.SigningBlockResult == nil || res.SigningBlockResult.SigningLineage == nil {
		return nil, nil
	}

	nodes := res.SigningBlockResult.SigningLineage.Nodes
	if len(nodes) == 0 {
		return nil, nil
	}

	last := nodes[len(nodes)-1]
	if last.SigningCert == nil || !last.SigningCert.Equal(current) {
		return nil, errors.New("signing certificate lineage does not end at the current signing certificate")
	}

	if len(nodes) == 1 {
		// Lineage of one is just the current certificate; no rotation happened.
		return nil, nil
	}

	ancestors := make([]string, 0, len(nodes)-1)
	for _, node := range nodes[:len(nodes)-1] {
		sum := sha256.Sum256(node.SigningCert.Raw)
		ancestors = append(ancestors, hex.EncodeToString(sum[:]))
	}
	return ancestors, nil
}

// Certificates returns the verified signing chain, oldest first, ending at
// the current signer.
// CertificateHashes is the current signer and its ancestors, signer first.
func (info *APKInfo) CertificateHashes() []string {
	if info == nil {
		return nil
	}
	out := make([]string, 0, 1+len(info.SigningAncestors))
	if info.CertFingerprint != "" {
		out = append(out, info.CertFingerprint)
	}
	return append(out, info.SigningAncestors...)
}

func (info *APKInfo) Certificates() []*x509.Certificate {
	if info == nil {
		return nil
	}
	if len(info.certificates) == 0 && info.Certificate != nil {
		return []*x509.Certificate{info.Certificate}
	}
	return info.certificates
}

// SigningCertificates verifies path and returns its signing chain, oldest
// first, ending at the current signer.
func SigningCertificates(ctx context.Context, apkPath string) (chain []*x509.Certificate, err error) {
	defer func() {
		if r := recover(); r != nil {
			chain, err = nil, malformedInput(r)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sig, err := verifySignature(apkPath)
	if err != nil {
		return nil, err
	}
	return sig.chain, nil
}

// ExtractCertificate extracts the current signing certificate from an APK
// file, applying the same robust v1/v2/v3 verification as Parse: malformed
// signatures and multiple current signers are rejected.
func ExtractCertificate(ctx context.Context, apkPath string) (cert *x509.Certificate, err error) {
	defer func() {
		if r := recover(); r != nil {
			cert, err = nil, malformedInput(r)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sig, err := verifySignature(apkPath)
	if err != nil {
		return nil, err
	}
	return sig.cert, nil
}

// extractIcon extracts the app icon from the APK as PNG bytes. A malformed
// icon fails only this call, never the APK, so a panic becomes an error.
func (a *apkArchive) extractIcon(manifest manifestInfo) (icon []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			icon, err = nil, malformedInput(r)
		}
	}()
	if icon, err := a.iconFromResource(manifest.Icon, manifest.IconRef); err == nil {
		return icon, nil
	}
	return a.extractIconManually()
}

// iconFromResource reads the manifest's icon resource. apkparser resolves it
// to a single config and cannot list the others, so the density table adds
// the rest. The widest bitmap wins unless an adaptive icon renders wider.
func (a *apkArchive) iconFromResource(icon, iconRef string) ([]byte, error) {
	paths := a.iconPaths(icon, iconRef)
	if len(paths) == 0 {
		return nil, errors.New("application icon resource is missing")
	}
	var bitmaps, layers []string
	for _, name := range paths {
		if strings.EqualFold(path.Ext(name), ".xml") {
			layers = append(layers, name)
		} else {
			bitmaps = append(bitmaps, name)
		}
	}
	bitmap, bitmapWidth, bitmapErr := a.widestBitmap(bitmaps)
	for _, name := range layers {
		adaptive, err := a.renderAdaptiveIcon(name)
		if err != nil {
			continue
		}
		if config, _, err := media.ConfigBounded(adaptive); err == nil && config.Width > bitmapWidth {
			return adaptive, nil
		}
	}
	if bitmap == nil {
		if bitmapErr == nil {
			bitmapErr = errors.New("application icon has no bitmap")
		}
		return nil, bitmapErr
	}
	return toPNG(bitmap)
}

// iconPaths returns the resolved icon, the last and first configs of the icon
// resource, and every density config of it, without duplicates.
func (a *apkArchive) iconPaths(icon, iconRef string) []string {
	var paths []string
	add := func(name string, err error) {
		if err == nil && name != "" && !slices.Contains(paths, name) {
			paths = append(paths, name)
		}
	}
	if isResourceReference(icon) {
		add(a.resolveResourceString(icon))
	} else {
		add(icon, nil)
	}
	if resourceID, err := parseResourceID(iconRef); err == nil {
		add(a.resolveResource(resourceID, apkparser.ConfigLast))
		add(a.resolveResource(resourceID, apkparser.ConfigFirst))
		for _, name := range a.densityPaths(resourceID) {
			add(name, nil)
		}
	}
	return paths
}

// densityPaths returns the file each density config of a resource points to,
// then the default config's. Obfuscated names make these the only way to find
// the sibling densities.
func (a *apkArchive) densityPaths(resourceID uint32) []string {
	table := a.loadDensityTable()
	if table == nil {
		return nil
	}
	configs := []*androidbinary.ResTableConfig{nil}
	// xxxhdpi, xxhdpi, xhdpi, hdpi, mdpi
	for _, density := range []uint16{640, 480, 320, 240, 160} {
		configs = append(configs, &androidbinary.ResTableConfig{Density: density})
	}
	var paths []string
	for _, config := range configs {
		if name, ok := densityPath(table, resourceID, config); ok && !slices.Contains(paths, name) {
			paths = append(paths, name)
		}
	}
	return paths
}

// loadDensityTable parses resources.arsc once for densityPaths. It returns
// nil when the table cannot be parsed.
func (a *apkArchive) loadDensityTable() (table *androidbinary.TableFile) {
	if a.densityTableDone {
		return a.densityTable
	}
	a.densityTableDone = true
	if a.tableData == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			table = nil
		}
	}()
	table, err := androidbinary.NewTableFile(bytes.NewReader(a.tableData))
	if err != nil {
		return nil
	}
	a.densityTable = table
	return table
}

// densityPath follows resource references to a string value. A panic on a
// malformed table skips this config only.
func densityPath(table *androidbinary.TableFile, resourceID uint32, config *androidbinary.ResTableConfig) (name string, ok bool) {
	defer func() {
		if recover() != nil {
			name, ok = "", false
		}
	}()
	for range 8 {
		value, err := table.GetResource(androidbinary.ResID(resourceID), config)
		if err != nil {
			return "", false
		}
		switch value := value.(type) {
		case string:
			return value, value != ""
		case uint32:
			resourceID = value
		default:
			return "", false
		}
	}
	return "", false
}

func (a *apkArchive) renderAdaptiveIcon(name string) ([]byte, error) {
	data, err := a.readEntry(name, maxAPKResourceSize)
	if err != nil {
		return nil, fmt.Errorf("read adaptive icon: %w", err)
	}
	adaptive := adaptiveIconCollector{}
	if err := apkparser.ParseXml(bytes.NewReader(data), &adaptive, a.table); err != nil {
		return nil, fmt.Errorf("parse adaptive icon %q: %w", name, err)
	}
	if adaptive.foreground == "" {
		return nil, fmt.Errorf("adaptive icon %q has no foreground", name)
	}
	return a.renderVectorResource(adaptive.foreground, adaptive.background)
}

// widestBitmap returns the widest of the named bitmaps as stored, with its
// width. Widths come from the header, so only the winner is read in full by
// the caller. Names that fail to read or decode are skipped; the first error
// is returned only when none works.
func (a *apkArchive) widestBitmap(names []string) (best []byte, bestWidth int, err error) {
	for _, name := range names {
		data, readErr := a.readEntry(name, maxAPKResourceSize)
		if readErr == nil {
			var config image.Config
			if config, _, readErr = media.ConfigBounded(data); readErr == nil && config.Width > bestWidth {
				best, bestWidth = data, config.Width
			}
		}
		if readErr != nil && err == nil {
			err = fmt.Errorf("read icon %q: %w", name, readErr)
		}
	}
	if best != nil {
		err = nil
	}
	return best, bestWidth, err
}

// readBitmap returns the named bitmap as PNG.
func (a *apkArchive) readBitmap(name string) ([]byte, error) {
	data, _, err := a.widestBitmap([]string{name})
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("icon %q has no bitmap", name)
	}
	return toPNG(data)
}

func toPNG(data []byte) ([]byte, error) {
	_, format, err := media.ConfigBounded(data)
	if err != nil {
		return nil, err
	}
	if format == "png" {
		return data, nil
	}
	img, _, err := media.DecodeBounded(data)
	if err != nil {
		return nil, err
	}
	return encodePNG(img)
}

type adaptiveIconCollector struct {
	foreground string
	background string
}

func (c *adaptiveIconCollector) EncodeToken(token xml.Token) error {
	start, ok := token.(xml.StartElement)
	if !ok {
		return nil
	}
	switch start.Name.Local {
	case "foreground":
		c.foreground = attribute(start, "drawable")
	case "background":
		c.background = attribute(start, "drawable")
	}
	return nil
}

func (c *adaptiveIconCollector) Flush() error {
	return nil
}

func (a *apkArchive) renderVectorResource(resourcePath, background string) ([]byte, error) {
	if isResourceReference(resourcePath) {
		resolved, err := a.resolveResourceString(resourcePath)
		if err != nil {
			return nil, fmt.Errorf("resolve vector resource: %w", err)
		}
		resourcePath = resolved
	}
	if !strings.EqualFold(path.Ext(resourcePath), ".xml") {
		return a.readBitmap(resourcePath)
	}

	data, err := a.readEntry(resourcePath, maxAPKResourceSize)
	if err != nil {
		return nil, fmt.Errorf("read vector resource: %w", err)
	}
	collector := vectorSVGCollector{}
	if err := apkparser.ParseXml(bytes.NewReader(data), &collector, a.table); err != nil {
		return nil, fmt.Errorf("parse vector resource %q: %w", resourcePath, err)
	}
	svg, err := collector.svg()
	if err != nil {
		return nil, err
	}
	icon, err := oksvg.ReadIconStream(strings.NewReader(svg))
	if err != nil {
		return nil, fmt.Errorf("read vector SVG: %w", err)
	}

	const iconSize = 512
	img := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: adaptiveIconBackground(background)}, image.Point{}, draw.Src)
	icon.SetTarget(0, 0, iconSize, iconSize)
	scanner := rasterx.NewScannerGV(iconSize, iconSize, img, img.Bounds())
	icon.Draw(rasterx.NewDasher(iconSize, iconSize, scanner), 1)
	// Gradients and theme colors render as nothing, which would hide a usable
	// bitmap icon behind a blank square.
	if !hasForeground(img) {
		return nil, errors.New("vector icon rendered blank")
	}
	return encodePNG(img)
}

// hasForeground reports whether any pixel differs from the top-left one.
func hasForeground(img *image.RGBA) bool {
	return !bytes.Equal(img.Pix, bytes.Repeat(img.Pix[:4], len(img.Pix)/4))
}

// parseColor parses an Android color literal: #RGB, #ARGB, #RRGGBB, or
// #AARRGGBB.
func parseColor(value string) (color.NRGBA, bool) {
	digits, ok := strings.CutPrefix(value, "#")
	if !ok {
		return color.NRGBA{}, false
	}
	switch len(digits) {
	case 3, 4:
		var expanded strings.Builder
		for _, digit := range digits {
			expanded.WriteRune(digit)
			expanded.WriteRune(digit)
		}
		digits = expanded.String()
	case 6, 8:
	default:
		return color.NRGBA{}, false
	}
	if len(digits) == 6 {
		digits = "ff" + digits
	}
	parsed, err := strconv.ParseUint(digits, 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: uint8(parsed >> 16), G: uint8(parsed >> 8), B: uint8(parsed), A: uint8(parsed >> 24)}, true
}

// adaptiveIconBackground returns the opaque background color, white when the
// value is not a color literal.
func adaptiveIconBackground(value string) color.Color {
	parsed, ok := parseColor(value)
	if !ok {
		return color.White
	}
	return color.RGBA{R: parsed.R, G: parsed.G, B: parsed.B, A: 0xff}
}

type vectorSVGCollector struct {
	body           strings.Builder
	viewportWidth  string
	viewportHeight string
	depth          int
}

func (c *vectorSVGCollector) EncodeToken(token xml.Token) error {
	switch token := token.(type) {
	case xml.StartElement:
		switch token.Name.Local {
		case "vector":
			c.viewportWidth = attribute(token, "viewportWidth")
			c.viewportHeight = attribute(token, "viewportHeight")
		case "group":
			fmt.Fprintf(&c.body, `<g transform="translate(%s %s) scale(%s %s)">`,
				svgAttribute(attribute(token, "translateX"), "0"),
				svgAttribute(attribute(token, "translateY"), "0"),
				svgAttribute(attribute(token, "scaleX"), "1"),
				svgAttribute(attribute(token, "scaleY"), "1"))
			c.depth++
		case "path":
			fmt.Fprintf(&c.body, `<path d="%s" %s/>`,
				svgAttribute(attribute(token, "pathData"), ""),
				svgFill(attribute(token, "fillColor")))
		}
	case xml.EndElement:
		if token.Name.Local == "group" && c.depth > 0 {
			c.body.WriteString("</g>")
			c.depth--
		}
	}
	return nil
}

func (c *vectorSVGCollector) Flush() error {
	return nil
}

func (c *vectorSVGCollector) svg() (string, error) {
	if c.viewportWidth == "" || c.viewportHeight == "" {
		return "", errors.New("vector viewport is missing")
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %s %s">%s</svg>`,
		svgAttribute(c.viewportWidth, ""),
		svgAttribute(c.viewportHeight, ""),
		c.body.String()), nil
}

func svgAttribute(value, fallback string) string {
	if value == "" {
		return fallback
	}
	var escaped strings.Builder
	xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}

// svgFill renders an Android fillColor as SVG fill attributes. SVG 1.1 has no
// #AARRGGBB, so alpha becomes fill-opacity. A decimal value is an opaque
// color: apkparser prints integer-typed colors without their alpha.
func svgFill(value string) string {
	if parsed, ok := parseColor(value); ok {
		fill := fmt.Sprintf(`fill="#%02x%02x%02x"`, parsed.R, parsed.G, parsed.B)
		if parsed.A != 0xff {
			fill += fmt.Sprintf(` fill-opacity="%.3f"`, float64(parsed.A)/255)
		}
		return fill
	}
	decimal, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return `fill="none"`
	}
	return fmt.Sprintf(`fill="#%06x"`, uint32(decimal)&0x00ffffff)
}

// extractIconManually searches for the icon in common locations within the APK.
// It handles both traditional PNG icons and modern adaptive icons.
func (a *apkArchive) extractIconManually() ([]byte, error) {
	// Priority order for icon paths (highest density first)
	var iconPaths []string
	densities := []string{"xxxhdpi", "xxhdpi", "xhdpi", "hdpi", "mdpi"}
	iconNames := []string{"ic_launcher", "ic_launcher_round"}
	iconExts := []string{"png", "webp"}
	for _, density := range densities {
		for _, iconName := range iconNames {
			for _, ext := range iconExts {
				iconPaths = append(iconPaths,
					fmt.Sprintf("res/mipmap-%s-v4/%s.%s", density, iconName, ext),
					fmt.Sprintf("res/drawable-%s-v4/%s.%s", density, iconName, ext),
				)
			}
		}
	}

	// Try exact paths first
	for _, iconPath := range iconPaths {
		if f, ok := a.byName[iconPath]; ok {
			return a.readIcon(f)
		}
	}

	// Try to find the foreground of adaptive icons
	// These are typically in ic_launcher_foreground.png
	var foregroundPaths []string
	for _, density := range densities {
		for _, ext := range iconExts {
			foregroundPaths = append(foregroundPaths,
				fmt.Sprintf("res/mipmap-%s-v4/ic_launcher_foreground.%s", density, ext),
				fmt.Sprintf("res/drawable-%s-v4/ic_launcher_foreground.%s", density, ext),
			)
		}
	}

	for _, iconPath := range foregroundPaths {
		if f, ok := a.byName[iconPath]; ok {
			return a.readIcon(f)
		}
	}

	// Fallback: find any PNG that looks like an icon in res/ directory
	// Try to find the largest PNG with "launcher" or "icon" in the name
	var bestIcon *zip.File
	var bestSize uint64

	for _, f := range a.files {
		name := path.Base(f.Name)
		if strings.HasPrefix(f.Name, "res/") &&
			(strings.Contains(name, "ic_launcher") || strings.Contains(name, "launcher") ||
				(strings.Contains(name, "icon") && !strings.Contains(name, "notification"))) &&
			(strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".webp")) &&
			!strings.HasSuffix(name, ".9.png") { // Skip 9-patch images
			if f.UncompressedSize64 > bestSize {
				bestIcon = f
				bestSize = f.UncompressedSize64
			}
		}
	}

	if bestIcon != nil {
		return a.readIcon(bestIcon)
	}

	// Last resort: look for any reasonably sized PNG in res/ that might be an icon
	// (larger than 1KB, suggesting it's not a tiny UI element)
	for _, f := range a.files {
		if strings.HasPrefix(f.Name, "res/") &&
			(strings.HasSuffix(f.Name, ".png") || strings.HasSuffix(f.Name, ".webp")) &&
			!strings.HasSuffix(f.Name, ".9.png") &&
			f.UncompressedSize64 > 1024 &&
			f.UncompressedSize64 > bestSize {
			bestIcon = f
			bestSize = f.UncompressedSize64
		}
	}

	if bestIcon != nil {
		return a.readIcon(bestIcon)
	}

	return nil, errors.New("no icon found")
}

func (a *apkArchive) readIcon(f *zip.File) ([]byte, error) {
	data, err := readZipFile(f, maxAPKResourceSize)
	if err != nil {
		return nil, err
	}
	return toPNG(data)
}

// readZipFile reads at most limit bytes of an archive entry. The declared size
// is checked first and the read is capped again because ZIP metadata can lie.
func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("file %s too large: %d bytes (max %d)", f.Name, f.UncompressedSize64, limit)
	}

	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", f.Name, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.Name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file %s exceeds maximum size of %d bytes", f.Name, limit)
	}
	return data, nil
}

// encodePNG encodes an image to PNG format.
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// IsArm64 returns true if the APK supports arm64-v8a architecture.
func (a *APKInfo) IsArm64() bool {
	for _, arch := range a.Architectures {
		if arch == "arm64-v8a" {
			return true
		}
	}
	// If no native libs, assume it's architecture-independent (pure Java/Kotlin)
	return len(a.Architectures) == 0
}

// IsWatch returns true if the APK declares the standard Android watch device
// feature used by Wear OS applications.
func (a *APKInfo) IsWatch() bool {
	for _, feature := range a.Features {
		if feature == "android.hardware.type.watch" {
			return true
		}
	}
	return false
}

// String returns a human-readable summary of the APK.
func (a *APKInfo) String() string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Package: %s\n", a.PackageID)
	fmt.Fprintf(&buf, "Version: %s (%d)\n", a.VersionName, a.VersionCode)
	fmt.Fprintf(&buf, "Label: %s\n", a.Label)
	fmt.Fprintf(&buf, "Min SDK: %d, Target SDK: %d\n", a.MinSDK, a.TargetSDK)
	fmt.Fprintf(&buf, "Architectures: %v\n", a.Architectures)
	fmt.Fprintf(&buf, "Certificate: %s\n", a.CertFingerprint)
	if len(a.SigningAncestors) > 0 {
		fmt.Fprintf(&buf, "Signing ancestors: %v\n", a.SigningAncestors)
	}
	fmt.Fprintf(&buf, "Size: %d bytes\n", a.FileSize)
	fmt.Fprintf(&buf, "SHA256: %s\n", a.SHA256)
	if a.Icon != nil {
		fmt.Fprintf(&buf, "Icon: %d bytes\n", len(a.Icon))
	}
	return buf.String()
}
