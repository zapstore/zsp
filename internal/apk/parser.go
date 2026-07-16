// Package apk handles APK parsing, metadata extraction, and signature verification.
package apk

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/avast/apkparser"
	"github.com/avast/apkverifier"
	"github.com/shogo82148/androidbinary"
	"github.com/shogo82148/androidbinary/apk"
	"golang.org/x/image/webp"
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

	// Android permissions (e.g., ["android.permission.INTERNET", "android.permission.CAMERA"])
	Permissions []string

	// Required device features declared by the manifest.
	Features []string

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

	// Parse the manifest with apkparser. Unlike androidbinary, it supports
	// manifests produced by current Android build tools.
	manifest, err := parseManifest(path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse APK manifest: %w", err)
	}

	info := &APKInfo{
		PackageID:   manifest.PackageID,
		VersionName: manifest.VersionName,
		VersionCode: manifest.VersionCode,
		MinSDK:      manifest.MinSDK,
		TargetSDK:   manifest.TargetSDK,
		Label:       manifest.Label,
		Permissions: manifest.Permissions,
		Features:    manifest.Features,
		FilePath:    path,
		FileSize:    fi.Size(),
		SHA256:      sha256Hash,
	}

	// Extract native architectures from lib/ directory
	info.Architectures = extractArchitectures(path)

	// Verify signature and extract certificate fingerprint
	certFingerprint, err := verifyCertificate(path)
	if err != nil {
		return nil, fmt.Errorf("signature verification failed: %w", err)
	}
	info.CertFingerprint = certFingerprint

	// Extract icon. Icon extraction failure is not fatal.
	icon, err := extractIcon(path)
	if err == nil {
		info.Icon = icon
	}

	return info, nil
}

type manifestInfo struct {
	PackageID   string
	VersionName string
	VersionCode int64
	MinSDK      int32
	TargetSDK   int32
	Label       string
	Permissions []string
	Features    []string
}

// manifestCollector records the fields zsp needs from an Android manifest.
// apkparser supplies already-decoded XML tokens and resolves string resources
// when resources.arsc can be parsed.
type manifestCollector struct {
	info manifestInfo
}

func (c *manifestCollector) EncodeToken(token xml.Token) error {
	start, ok := token.(xml.StartElement)
	if !ok {
		return nil
	}

	switch start.Name.Local {
	case "manifest":
		c.info.PackageID = attribute(start, "package")
		c.info.VersionName = attribute(start, "versionName")
		c.info.VersionCode = parseManifestInt(attribute(start, "versionCode"))
	case "uses-sdk":
		c.info.MinSDK = int32(parseManifestInt(attribute(start, "minSdkVersion")))
		c.info.TargetSDK = int32(parseManifestInt(attribute(start, "targetSdkVersion")))
	case "application":
		c.info.Label = attribute(start, "label")
	case "uses-permission", "uses-permission-sdk-23", "uses-permission-sdk-m":
		if permission := attribute(start, "name"); permission != "" {
			c.info.Permissions = append(c.info.Permissions, permission)
		}
	case "uses-feature":
		if feature := attribute(start, "name"); feature != "" {
			c.info.Features = append(c.info.Features, feature)
		}
	}

	return nil
}

func (c *manifestCollector) Flush() error {
	return nil
}

func parseManifest(path string) (manifestInfo, error) {
	collector := &manifestCollector{}
	zipErr, _, manifestErr := apkparser.ParseApk(path, collector)
	if zipErr != nil {
		return manifestInfo{}, fmt.Errorf("open APK: %w", zipErr)
	}
	if manifestErr != nil {
		return manifestInfo{}, fmt.Errorf("parse AndroidManifest.xml: %w", manifestErr)
	}
	if collector.info.PackageID == "" {
		return manifestInfo{}, fmt.Errorf("parse AndroidManifest.xml: package is missing")
	}
	if isResourceReference(collector.info.Label) {
		if label, err := resolveLabel(path); err == nil && label != "" {
			collector.info.Label = label
		}
	}
	return collector.info, nil
}

// isResourceReference reports whether a manifest value looks like an Android
// resource ID. apkparser may leave these unresolved when the resource table
// uses a format it cannot decode.
func isResourceReference(value string) bool {
	return strings.HasPrefix(strings.ToLower(value), "@")
}

// resolveLabel resolves the application label through androidbinary's
// resource table support. This is best-effort because the primary manifest
// parser supports APKs that androidbinary may not be able to open.
func resolveLabel(path string) (string, error) {
	pkg, err := apk.OpenFile(path)
	if err != nil {
		return "", fmt.Errorf("open APK for label resolution: %w", err)
	}
	defer pkg.Close()

	label, err := pkg.Label(nil)
	if err != nil {
		return "", fmt.Errorf("resolve application label: %w", err)
	}
	return label, nil
}

func attribute(start xml.StartElement, name string) string {
	for _, attr := range start.Attr {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func parseManifestInt(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 0, 64)
	if err != nil {
		return 0
	}
	return parsed
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
		// Security: Validate zip entry path to prevent zip slip attacks.
		// Malicious APKs could contain paths like "../../../etc/passwd".
		// We only read metadata here (not extracting files), but validate anyway
		// to prevent any future misuse of this code pattern.
		if !isValidZipEntryPath(f.Name) {
			continue
		}
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

// isValidZipEntryPath validates a zip entry path to prevent zip slip attacks.
// Returns false if the path contains directory traversal sequences or is absolute.
func isValidZipEntryPath(path string) bool {
	// Reject paths with directory traversal
	if strings.Contains(path, "..") {
		return false
	}
	// Reject absolute paths
	if strings.HasPrefix(path, "/") {
		return false
	}
	// Reject paths that clean to something different (indicates traversal)
	cleaned := filepath.Clean(path)
	if cleaned != path && cleaned+"/" != path {
		// Allow trailing slash difference but nothing else
		if !strings.HasSuffix(path, "/") || cleaned != strings.TrimSuffix(path, "/") {
			return false
		}
	}
	return true
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

// ExtractCertificate extracts the signing certificate from an APK file.
// Returns the x509 certificate used to sign the APK.
func ExtractCertificate(path string) (*x509.Certificate, error) {
	res, err := apkverifier.Verify(path, nil)
	if err != nil {
		return nil, fmt.Errorf("APK verification failed: %w", err)
	}

	// Pick the best certificate (prefers v3 > v2 > v1)
	_, cert := apkverifier.PickBestApkCert(res.SignerCerts)
	if cert == nil {
		return nil, fmt.Errorf("failed to extract certificate: no valid certificate found")
	}

	return cert, nil
}

// extractIcon extracts the app icon from the APK as PNG bytes.
// It tries to get the highest resolution icon available by requesting different densities.
func extractIcon(path string) ([]byte, error) {
	pkg, err := apk.OpenFile(path)
	if err != nil {
		return extractIconManually(path)
	}
	defer pkg.Close()

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
	// Security: Validate zip entry paths to prevent zip slip attacks
	files := make(map[string]*zip.File)
	for _, f := range r.File {
		if !isValidZipEntryPath(f.Name) {
			continue
		}
		files[f.Name] = f
	}

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
		if f, ok := files[iconPath]; ok {
			return readZipIcon(f)
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
		if f, ok := files[iconPath]; ok {
			return readZipIcon(f)
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
			(strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".webp")) &&
			!strings.HasSuffix(name, ".9.png") { // Skip 9-patch images
			if f.UncompressedSize64 > bestSize {
				bestIcon = f
				bestSize = f.UncompressedSize64
			}
		}
	}

	if bestIcon != nil {
		return readZipIcon(bestIcon)
	}

	// Last resort: look for any reasonably sized PNG in res/ that might be an icon
	// (larger than 1KB, suggesting it's not a tiny UI element)
	for _, f := range r.File {
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
		return readZipIcon(bestIcon)
	}

	return nil, fmt.Errorf("no icon found")
}

// readZipIcon reads a potential icon and converts WebP to PNG.
func readZipIcon(f *zip.File) ([]byte, error) {
	data, err := readZipFile(f)
	if err != nil {
		return nil, err
	}

	if strings.EqualFold(filepath.Ext(f.Name), ".webp") {
		img, err := webp.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return encodePNG(img)
	}

	return data, nil
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
