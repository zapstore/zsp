// Package apk handles APK parsing, metadata extraction, and signature verification.
package apk

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/avast/apkverifier"
	"github.com/shogo82148/androidbinary"
	"github.com/shogo82148/androidbinary/apk"
)

// maxZipFileSize is the maximum size for reading individual files from APK archives.
// This prevents memory exhaustion from malicious or corrupted APKs.
const maxZipFileSize = 650 * 1024 * 1024 // 650MB

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

	// Native architectures (e.g., ["arm64-v8a", "armeabi-v7a"])
	Architectures []string

	// Certificate SHA-256 fingerprint (hex encoded, lowercase)
	CertFingerprint string

	// Icon PNG bytes (nil if not found or extraction failed)
	Icon []byte

	// File information
	FilePath string
	FileSize int64
	SHA256   string
}

// Parse extracts metadata from an APK file.
func Parse(path string) (*APKInfo, error) {
	// Get file info
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat APK: %w", err)
	}

	// Calculate SHA256
	sha256Hash, err := hashFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to hash APK: %w", err)
	}

	// Open APK for manifest parsing
	pkg, err := apk.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open APK: %w", err)
	}
	defer pkg.Close()

	manifest := pkg.Manifest()

	info := &APKInfo{
		PackageID:   manifest.Package.MustString(),
		VersionName: manifest.VersionName.MustString(),
		VersionCode: int64(manifest.VersionCode.MustInt32()),
		FilePath:    path,
		FileSize:    fi.Size(),
		SHA256:      sha256Hash,
	}

	// Extract SDK versions
	info.MinSDK = manifest.SDK.Min.MustInt32()
	info.TargetSDK = manifest.SDK.Target.MustInt32()

	// Extract app label
	info.Label = extractLabel(pkg, path)

	// Extract native architectures from lib/ directory
	info.Architectures = extractArchitectures(path)

	// Verify signature and extract certificate fingerprint
	certFingerprint, err := verifyCertificate(path)
	if err != nil {
		return nil, fmt.Errorf("signature verification failed: %w", err)
	}
	info.CertFingerprint = certFingerprint

	// Extract icon
	icon, err := extractIcon(pkg, path)
	if err == nil {
		info.Icon = icon
	}
	// Icon extraction failure is not fatal

	return info, nil
}

// extractLabel extracts the app label from an APK.
// It handles nested resource references that the standard library doesn't resolve.
func extractLabel(pkg *apk.Apk, path string) string {
	// First try the library's built-in method
	label, err := pkg.Label(nil)
	if err == nil && label != "" {
		return label
	}

	// Fall back to our custom extraction that handles nested references
	return extractLabelWithReferences(path)
}

// extractLabelWithReferences extracts the label by manually resolving resource references.
// This handles cases where the label points to another string resource (nested references).
func extractLabelWithReferences(path string) string {
	r, err := zip.OpenReader(path)
	if err != nil {
		return ""
	}
	defer r.Close()

	// Read resources.arsc
	var resData []byte
	for _, f := range r.File {
		if f.Name == "resources.arsc" {
			if f.UncompressedSize64 > maxZipFileSize {
				return ""
			}
			rc, err := f.Open()
			if err != nil {
				return ""
			}
			resData, err = io.ReadAll(io.LimitReader(rc, int64(maxZipFileSize)))
			rc.Close()
			if err != nil {
				return ""
			}
			break
		}
	}
	if resData == nil {
		return ""
	}

	// Read AndroidManifest.xml
	var manifestData []byte
	for _, f := range r.File {
		if f.Name == "AndroidManifest.xml" {
			if f.UncompressedSize64 > maxZipFileSize {
				return ""
			}
			rc, err := f.Open()
			if err != nil {
				return ""
			}
			manifestData, err = io.ReadAll(io.LimitReader(rc, int64(maxZipFileSize)))
			rc.Close()
			if err != nil {
				return ""
			}
			break
		}
	}
	if manifestData == nil {
		return ""
	}

	// Parse the resource table
	table, err := androidbinary.NewTableFile(bytes.NewReader(resData))
	if err != nil {
		return ""
	}

	// Parse manifest to get the label resource ID
	xmlFile, err := androidbinary.NewXMLFile(bytes.NewReader(manifestData))
	if err != nil {
		return ""
	}

	// Find the application label attribute in the XML
	labelResID := findLabelResourceID(xmlFile, manifestData)
	if labelResID == 0 {
		return ""
	}

	// Resolve the resource, following references
	return resolveStringResource(table, androidbinary.ResID(labelResID), nil, 10)
}

// findLabelResourceID finds the label resource ID from the binary XML manifest.
// It parses the binary AXML format to find the application element's label attribute.
func findLabelResourceID(xmlFile *androidbinary.XMLFile, data []byte) uint32 {
	reader := bytes.NewReader(data)

	// Read the main AXML header
	var mainChunkType uint16
	var mainHeaderSize uint16
	var mainChunkSize uint32
	binary.Read(reader, binary.LittleEndian, &mainChunkType)
	binary.Read(reader, binary.LittleEndian, &mainHeaderSize)
	binary.Read(reader, binary.LittleEndian, &mainChunkSize)

	// Start parsing after the main header
	offset := int64(mainHeaderSize)
	fileSize := int64(len(data))

	for offset < fileSize {
		if offset+8 > fileSize {
			break
		}

		reader.Seek(offset, io.SeekStart)
		var chunkType uint16
		var headerSize uint16
		var chunkSize uint32
		binary.Read(reader, binary.LittleEndian, &chunkType)
		binary.Read(reader, binary.LittleEndian, &headerSize)
		binary.Read(reader, binary.LittleEndian, &chunkSize)

		if chunkSize == 0 || chunkSize > uint32(fileSize) {
			break
		}

		// START_ELEMENT = 0x0102
		if chunkType == 0x0102 {
			// Read ResXMLTreeNode header (skip lineNumber and comment)
			reader.Seek(offset+int64(headerSize), io.SeekStart)

			// Read ResXMLTreeAttrExt
			var ns uint32
			var name uint32
			binary.Read(reader, binary.LittleEndian, &ns)
			binary.Read(reader, binary.LittleEndian, &name)

			elemName := xmlFile.GetString(androidbinary.ResStringPoolRef(name))

			var attrStart uint16
			var attrSize uint16
			var attrCount uint16
			binary.Read(reader, binary.LittleEndian, &attrStart)
			binary.Read(reader, binary.LittleEndian, &attrSize)
			binary.Read(reader, binary.LittleEndian, &attrCount)

			// Skip idIndex, classIndex, styleIndex
			reader.Seek(6, io.SeekCurrent)

			// Only process application element
			if elemName == "application" {
				for i := uint16(0); i < attrCount; i++ {
					var attrNsIdx uint32
					var attrNameIdx uint32
					var attrRawValue uint32
					var attrTypedValueSize uint16
					var attrTypedValueRes0 uint8
					var attrTypedValueDataType uint8
					var attrTypedValueData uint32

					binary.Read(reader, binary.LittleEndian, &attrNsIdx)
					binary.Read(reader, binary.LittleEndian, &attrNameIdx)
					binary.Read(reader, binary.LittleEndian, &attrRawValue)
					binary.Read(reader, binary.LittleEndian, &attrTypedValueSize)
					binary.Read(reader, binary.LittleEndian, &attrTypedValueRes0)
					binary.Read(reader, binary.LittleEndian, &attrTypedValueDataType)
					binary.Read(reader, binary.LittleEndian, &attrTypedValueData)

					attrName := xmlFile.GetString(androidbinary.ResStringPoolRef(attrNameIdx))

					// Check if this is a label attribute with a reference value
					if attrName == "label" && attrTypedValueDataType == 0x01 && attrTypedValueData != 0 {
						return attrTypedValueData
					}
				}
			}
		}

		offset += int64(chunkSize)
	}

	return 0
}

// resolveStringResource resolves a resource ID to a string, following references.
func resolveStringResource(table *androidbinary.TableFile, id androidbinary.ResID, config *androidbinary.ResTableConfig, maxDepth int) string {
	if maxDepth <= 0 {
		return ""
	}

	val, err := table.GetResource(id, config)
	if err != nil {
		return ""
	}

	switch v := val.(type) {
	case string:
		return v
	case uint32:
		// This is likely a reference to another resource
		// Check if it looks like a valid resource ID (0x7fXXXXXX pattern)
		if v&0xFF000000 == 0x7F000000 {
			return resolveStringResource(table, androidbinary.ResID(v), config, maxDepth-1)
		}
		return ""
	default:
		return ""
	}
}

// hashFile calculates SHA256 hash of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractArchitectures scans the APK's lib/ directory for native libraries.
func extractArchitectures(path string) []string {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil
	}
	defer r.Close()

	archSet := make(map[string]struct{})
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "lib/") {
			parts := strings.Split(f.Name, "/")
			if len(parts) >= 2 && parts[1] != "" {
				archSet[parts[1]] = struct{}{}
			}
		}
	}

	archs := make([]string, 0, len(archSet))
	for arch := range archSet {
		archs = append(archs, arch)
	}
	return archs
}

// verifyCertificate verifies the APK signature and returns the certificate fingerprint.
func verifyCertificate(path string) (string, error) {
	res, err := apkverifier.Verify(path, nil)
	if err != nil {
		return "", fmt.Errorf("APK verification failed: %w", err)
	}

	// Pick the best certificate (prefers v3 > v2 > v1)
	_, cert := apkverifier.PickBestApkCert(res.SignerCerts)
	if cert == nil {
		return "", fmt.Errorf("failed to extract certificate: no valid certificate found")
	}

	// Calculate SHA256 fingerprint of the certificate
	fingerprint := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(fingerprint[:]), nil
}

// extractIcon extracts the app icon from the APK as PNG bytes.
// It tries to get the highest resolution icon available by requesting different densities.
func extractIcon(pkg *apk.Apk, path string) ([]byte, error) {
	// Android density values from highest to lowest
	// xxxhdpi=640, xxhdpi=480, xhdpi=320, hdpi=240, mdpi=160
	densities := []uint16{640, 480, 320, 240, 160}

	var bestIcon image.Image
	var bestWidth int

	for _, density := range densities {
		config := &androidbinary.ResTableConfig{
			Density: density,
		}
		icon, err := pkg.Icon(config)
		if err != nil || icon == nil {
			continue
		}
		width := icon.Bounds().Dx()
		if width > bestWidth {
			bestIcon = icon
			bestWidth = width
		}
	}

	// Also try with nil config in case it returns something different
	if nilIcon, err := pkg.Icon(nil); err == nil && nilIcon != nil {
		width := nilIcon.Bounds().Dx()
		if width > bestWidth {
			bestIcon = nilIcon
			bestWidth = width
		}
	}

	if bestIcon != nil {
		return encodePNG(bestIcon)
	}

	// Fallback: manually search for icon in the APK
	return extractIconManually(path)
}

// extractIconManually searches for the icon in common locations within the APK.
// It handles both traditional PNG icons and modern adaptive icons.
func extractIconManually(path string) ([]byte, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	// Build a map of files for faster lookups
	files := make(map[string]*zip.File)
	for _, f := range r.File {
		files[f.Name] = f
	}

	// Priority order for icon paths (highest density first)
	iconPaths := []string{
		"res/mipmap-xxxhdpi-v4/ic_launcher.png",
		"res/mipmap-xxhdpi-v4/ic_launcher.png",
		"res/mipmap-xhdpi-v4/ic_launcher.png",
		"res/mipmap-hdpi-v4/ic_launcher.png",
		"res/mipmap-mdpi-v4/ic_launcher.png",
		"res/drawable-xxxhdpi-v4/ic_launcher.png",
		"res/drawable-xxhdpi-v4/ic_launcher.png",
		"res/drawable-xhdpi-v4/ic_launcher.png",
		"res/drawable-hdpi-v4/ic_launcher.png",
		"res/drawable-mdpi-v4/ic_launcher.png",
	}

	// Try exact paths first
	for _, iconPath := range iconPaths {
		if f, ok := files[iconPath]; ok {
			return readZipFile(f)
		}
	}

	// Try to find the foreground of adaptive icons
	// These are typically in ic_launcher_foreground.png
	foregroundPaths := []string{
		"res/mipmap-xxxhdpi-v4/ic_launcher_foreground.png",
		"res/mipmap-xxhdpi-v4/ic_launcher_foreground.png",
		"res/mipmap-xhdpi-v4/ic_launcher_foreground.png",
		"res/mipmap-hdpi-v4/ic_launcher_foreground.png",
		"res/drawable-xxxhdpi-v4/ic_launcher_foreground.png",
		"res/drawable-xxhdpi-v4/ic_launcher_foreground.png",
		"res/drawable-xhdpi-v4/ic_launcher_foreground.png",
		"res/drawable-hdpi-v4/ic_launcher_foreground.png",
	}

	for _, iconPath := range foregroundPaths {
		if f, ok := files[iconPath]; ok {
			return readZipFile(f)
		}
	}

	// Fallback: find any PNG that looks like an icon in res/ directory
	// Try to find the largest PNG with "launcher" or "icon" in the name
	var bestIcon *zip.File
	var bestSize uint64

	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if strings.HasPrefix(f.Name, "res/") &&
			(strings.Contains(name, "ic_launcher") || strings.Contains(name, "launcher") ||
				(strings.Contains(name, "icon") && !strings.Contains(name, "notification"))) &&
			strings.HasSuffix(name, ".png") &&
			!strings.HasSuffix(name, ".9.png") { // Skip 9-patch images
			if f.UncompressedSize64 > bestSize {
				bestIcon = f
				bestSize = f.UncompressedSize64
			}
		}
	}

	if bestIcon != nil {
		return readZipFile(bestIcon)
	}

	// Last resort: look for any reasonably sized PNG in res/ that might be an icon
	// (larger than 1KB, suggesting it's not a tiny UI element)
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "res/") &&
			strings.HasSuffix(f.Name, ".png") &&
			!strings.HasSuffix(f.Name, ".9.png") &&
			f.UncompressedSize64 > 1024 &&
			f.UncompressedSize64 > bestSize {
			bestIcon = f
			bestSize = f.UncompressedSize64
		}
	}

	if bestIcon != nil {
		return readZipFile(bestIcon)
	}

	return nil, fmt.Errorf("no icon found")
}

// readZipFile reads the contents of a file within a zip archive.
// Returns an error if the uncompressed size exceeds maxZipFileSize.
func readZipFile(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxZipFileSize {
		return nil, fmt.Errorf("file %s too large: %d bytes (max %d)", f.Name, f.UncompressedSize64, maxZipFileSize)
	}

	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	// Use LimitReader as defense-in-depth against incorrect UncompressedSize64
	return io.ReadAll(io.LimitReader(rc, int64(maxZipFileSize)))
}

// encodePNG encodes an image to PNG format.
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
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

// HasGoogleDependency checks if the APK might have Google Play dependencies
// based on the package ID patterns.
func (a *APKInfo) HasGoogleDependency() bool {
	lower := strings.ToLower(a.PackageID)
	return strings.Contains(lower, "gms") ||
		strings.Contains(lower, "google") ||
		strings.Contains(lower, "play")
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
	fmt.Fprintf(&buf, "Size: %d bytes\n", a.FileSize)
	fmt.Fprintf(&buf, "SHA256: %s\n", a.SHA256)
	if a.Icon != nil {
		fmt.Fprintf(&buf, "Icon: %d bytes\n", len(a.Icon))
	}
	return buf.String()
}

// ResConfig represents a resource configuration for localization.
type ResConfig = androidbinary.ResTableConfig
