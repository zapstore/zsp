// Package artifact provides a unified abstraction for parsing software assets
// across different formats (APK, ELF, Mach-O). It detects the format from file
// headers and routes to the appropriate parser, producing a common AssetInfo
// that downstream code (events, workflow, upload) consumes.
package artifact

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// NIP-82 MIME types for supported asset formats.
const (
	MIMEAndroidAPK        = "application/vnd.android.package-archive"
	MIMELinuxExecutable   = "application/x-executable"
	MIMEMachOBinary       = "application/x-mach-binary"
	MIMELinuxAppImage     = "application/vnd.appimage"
	MIMEMacOSDiskImage    = "application/x-apple-diskimage"
	MIMEMacOSInstaller    = "application/vnd.apple.installer+xml"
	MIMEWindowsExecutable = "application/vnd.microsoft.portable-executable"
)

// AssetInfo contains parsed metadata from any supported asset format.
// Fields that cannot be determined from the file alone (Identifier, Version, Name)
// are left empty by parsers and must be set by the caller from release/config data.
type AssetInfo struct {
	// Identifier is the package/application ID.
	// For APKs: package name from AndroidManifest (e.g., "com.example.app").
	// For native executables: set by caller from config or derived from filename.
	Identifier string

	// Name is the human-readable application name.
	// For APKs: label from AndroidManifest resources.
	// For native executables: set by caller from config.
	Name string

	// Version string (e.g., "1.2.3").
	// For APKs: versionName from AndroidManifest.
	// For native executables: set by caller from release tag.
	Version string

	// File metadata.
	FilePath string
	FileSize int64
	SHA256   string

	// NIP-82 classification.
	MIMEType  string   // MIME type from Appendix C (e.g., "application/x-executable").
	Platforms []string // Platform identifiers from Appendix A (e.g., ["linux-x86_64"]).

	// Icon PNG bytes, nil if not available.
	// APKs may embed an icon; native executables typically do not.
	Icon []byte

	// Android-specific metadata. Nil for non-APK assets.
	APK *APKMeta
}

// APKMeta contains Android-specific asset metadata.
type APKMeta struct {
	VersionCode     int64
	MinSDK          int32
	TargetSDK       int32
	CertFingerprint string
	Architectures   []string // Raw ABI names (e.g., "arm64-v8a", "armeabi-v7a").
	Permissions     []string
}

// IsAPK returns true if this asset is an Android APK.
func (a *AssetInfo) IsAPK() bool {
	return a.APK != nil
}

// IsNativeExecutable returns true if this asset is a native executable (ELF or Mach-O).
func (a *AssetInfo) IsNativeExecutable() bool {
	return a.MIMEType == MIMELinuxExecutable || a.MIMEType == MIMEMachOBinary
}

// PlatformOS returns the OS component of the first platform identifier,
// or empty string if no platforms are set.
func (a *AssetInfo) PlatformOS() string {
	if len(a.Platforms) == 0 {
		return ""
	}
	os, _ := SplitPlatform(a.Platforms[0])
	return os
}

// PlatformArch returns the architecture component of the first platform identifier,
// or empty string if no platforms are set.
func (a *AssetInfo) PlatformArch() string {
	if len(a.Platforms) == 0 {
		return ""
	}
	_, arch := SplitPlatform(a.Platforms[0])
	return arch
}

// Parser extracts metadata from a supported asset format.
type Parser interface {
	// Parse extracts metadata from the file at the given path.
	// Computes SHA256 hash, file size, MIME type, and platform identifiers.
	// Format-specific metadata (APKMeta, etc.) is populated where applicable.
	//
	// Fields that cannot be determined from the file alone
	// (Identifier, Version, Name for native executables) are left empty.
	Parse(path string) (*AssetInfo, error)
}

// ---------------------------------------------------------------------------
// Format detection
// ---------------------------------------------------------------------------

// Magic bytes for format identification.
var (
	elfMagic  = []byte{0x7f, 'E', 'L', 'F'}
	mach64LE  = []byte{0xcf, 0xfa, 0xed, 0xfe} // Mach-O 64-bit little-endian
	mach32LE  = []byte{0xce, 0xfa, 0xed, 0xfe} // Mach-O 32-bit little-endian
	machFatBE = []byte{0xca, 0xfe, 0xba, 0xbe} // Mach-O universal (fat) big-endian
	zipMagic  = []byte{'P', 'K', 0x03, 0x04}
)

// Detect identifies the asset format from file headers and returns the appropriate parser.
// Returns an error if the format is not recognized or not supported.
func Detect(path string) (Parser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return nil, fmt.Errorf("failed to read file header: %w", err)
	}

	switch {
	case bytes.Equal(magic, elfMagic):
		return &ELFParser{}, nil

	case bytes.Equal(magic, mach64LE),
		bytes.Equal(magic, mach32LE),
		bytes.Equal(magic, machFatBE):
		return &MachOParser{}, nil

	case bytes.Equal(magic, zipMagic):
		// ZIP files might be APKs — check for AndroidManifest.xml
		if isAPKFile(path) {
			return &APKParser{}, nil
		}
		return nil, fmt.Errorf("ZIP archive is not a supported asset format; only APK ZIPs are supported per NIP-82")

	default:
		return nil, fmt.Errorf("unrecognized file format (magic bytes: %02x)", magic)
	}
}

// DetectMIMEType returns the NIP-82 MIME type for a file based on its magic bytes.
// Returns empty string and an error if the format is not recognized.
func DetectMIMEType(path string) (string, error) {
	p, err := Detect(path)
	if err != nil {
		return "", err
	}

	switch p.(type) {
	case *ELFParser:
		return MIMELinuxExecutable, nil
	case *MachOParser:
		return MIMEMachOBinary, nil
	case *APKParser:
		return MIMEAndroidAPK, nil
	default:
		return "", fmt.Errorf("unknown parser type")
	}
}

// isAPKFile checks whether a ZIP file is an Android APK by looking for AndroidManifest.xml.
func isAPKFile(path string) bool {
	r, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name == "AndroidManifest.xml" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Platform helpers
// ---------------------------------------------------------------------------

// HostPlatform returns the NIP-82 platform identifier for the current host.
// Examples: "darwin-arm64", "linux-x86_64", "linux-aarch64".
// Returns empty string if the platform is not recognized.
func HostPlatform() string {
	return PlatformID(runtime.GOOS, runtime.GOARCH)
}

// HostOS returns the NIP-82 OS component for the current host.
func HostOS() string {
	os, _ := SplitPlatform(HostPlatform())
	return os
}

// HostArch returns the NIP-82 architecture component for the current host.
func HostArch() string {
	_, arch := SplitPlatform(HostPlatform())
	return arch
}

// PlatformID maps Go's GOOS/GOARCH to a NIP-82 platform identifier.
// NIP-82 identifiers are based on `uname -sm`, so architecture naming
// differs by OS (e.g., darwin uses "arm64", linux uses "aarch64").
func PlatformID(goos, goarch string) string {
	switch goos {
	case "darwin":
		switch goarch {
		case "arm64":
			return "darwin-arm64"
		case "amd64":
			return "darwin-x86_64"
		}
	case "linux":
		switch goarch {
		case "arm64":
			return "linux-aarch64"
		case "amd64":
			return "linux-x86_64"
		case "arm":
			return "linux-armv7l"
		case "riscv64":
			return "linux-riscv64"
		}
	case "windows":
		switch goarch {
		case "arm64":
			return "windows-aarch64"
		case "amd64":
			return "windows-x86_64"
		}
	case "freebsd":
		switch goarch {
		case "arm64":
			return "freebsd-aarch64"
		case "amd64":
			return "freebsd-x86_64"
		}
	case "android":
		switch goarch {
		case "arm64":
			return "android-arm64-v8a"
		case "arm":
			return "android-armeabi-v7a"
		case "amd64":
			return "android-x86_64"
		case "386":
			return "android-x86"
		}
	}
	return ""
}

// SplitPlatform splits a NIP-82 platform identifier into OS and architecture parts.
// For Android's multi-part architecture identifiers (e.g., "android-arm64-v8a"),
// the OS is "android" and the arch is "arm64-v8a".
func SplitPlatform(platform string) (os, arch string) {
	if platform == "" {
		return "", ""
	}

	// Android has multi-segment arch names (arm64-v8a, armeabi-v7a).
	// The OS is always the first segment.
	idx := strings.Index(platform, "-")
	if idx < 0 {
		return platform, ""
	}

	os = platform[:idx]
	arch = platform[idx+1:]
	return os, arch
}

// MatchesPlatform returns true if the asset's platforms include the given platform.
// If the asset has no platform restrictions, it matches everything for its OS.
func MatchesPlatform(assetPlatforms []string, targetPlatform string) bool {
	if len(assetPlatforms) == 0 {
		return true // No platform restrictions
	}

	for _, p := range assetPlatforms {
		if p == targetPlatform {
			return true
		}
	}
	return false
}

// PlatformOSFromFilename attempts to detect the target OS from an asset filename.
// Returns empty string if no OS indicator is found.
func PlatformOSFromFilename(filename string) string {
	lower := strings.ToLower(filename)

	switch {
	case strings.Contains(lower, "linux"):
		return "linux"
	case strings.Contains(lower, "darwin") || strings.Contains(lower, "macos") || strings.Contains(lower, "apple"):
		return "darwin"
	case strings.Contains(lower, "windows") || strings.Contains(lower, "win64") || strings.Contains(lower, "win32"):
		return "windows"
	case strings.Contains(lower, "freebsd"):
		return "freebsd"
	case strings.Contains(lower, "android"):
		return "android"
	}

	// Check file extensions that imply a platform
	switch {
	case strings.HasSuffix(lower, ".apk"):
		return "android"
	case strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".msi"):
		return "windows"
	case strings.HasSuffix(lower, ".dmg") || strings.HasSuffix(lower, ".pkg"):
		return "darwin"
	case strings.HasSuffix(lower, ".appimage"):
		return "linux"
	}

	return ""
}

// PlatformArchFromFilename attempts to detect the target architecture from an asset filename.
// Returns empty string if no architecture indicator is found.
func PlatformArchFromFilename(filename string) string {
	lower := strings.ToLower(filename)

	// Order matters: check longer patterns first to avoid partial matches.
	switch {
	case strings.Contains(lower, "x86_64") || strings.Contains(lower, "x86-64") || strings.Contains(lower, "amd64"):
		return "x86_64"
	case strings.Contains(lower, "aarch64") || strings.Contains(lower, "arm64"):
		return "arm64" // Caller should adjust to aarch64 for linux
	case strings.Contains(lower, "arm64-v8a"):
		return "arm64-v8a"
	case strings.Contains(lower, "armeabi-v7a"):
		return "armeabi-v7a"
	case strings.Contains(lower, "armv7") || strings.Contains(lower, "armhf"):
		return "armv7l"
	case strings.Contains(lower, "riscv64"):
		return "riscv64"
	// i686/i386/x86 (32-bit) — check AFTER x86_64 to avoid false matches
	case strings.Contains(lower, "i686") || strings.Contains(lower, "i386"):
		return "x86"
	}

	return ""
}

// PlatformFromFilename attempts to detect a full NIP-82 platform identifier from a filename.
// Returns empty string if not enough information is available.
func PlatformFromFilename(filename string) string {
	os := PlatformOSFromFilename(filename)
	arch := PlatformArchFromFilename(filename)

	if os == "" || arch == "" {
		return ""
	}

	// Normalize arch per NIP-82 conventions (OS-specific names).
	switch os {
	case "linux":
		if arch == "arm64" {
			arch = "aarch64"
		}
	case "darwin":
		// darwin uses "arm64" as-is
	case "windows":
		if arch == "arm64" {
			arch = "aarch64"
		}
	case "freebsd":
		if arch == "arm64" {
			arch = "aarch64"
		}
	case "android":
		// Android uses its own arch names; already handled.
	}

	return os + "-" + arch
}

// IsSupportedAssetFilename returns true if the filename looks like a supported
// native executable asset (not an archive, checksum, or signature file).
func IsSupportedAssetFilename(filename string) bool {
	lower := strings.ToLower(filename)

	// Reject archives (not supported per NIP-82)
	archiveExts := []string{".tar.gz", ".tgz", ".tar.bz2", ".tar.xz", ".tar.zst",
		".zip", ".gz", ".bz2", ".xz", ".zst", ".7z", ".rar",
		".deb", ".rpm", ".snap"}
	for _, ext := range archiveExts {
		if strings.HasSuffix(lower, ext) {
			return false
		}
	}

	// Reject checksum/signature files
	checksumExts := []string{".sha256", ".sha256sum", ".sha512", ".sha512sum",
		".md5", ".md5sum", ".sig", ".asc", ".gpg", ".minisig",
		".pem", ".cert", ".sbom", ".json", ".yaml", ".yml",
		".txt", ".md"}
	for _, ext := range checksumExts {
		if strings.HasSuffix(lower, ext) {
			return false
		}
	}

	return true
}
