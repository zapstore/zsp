package artifact

import (
	"fmt"

	"github.com/zapstore/zsp/internal/apk"
)

// APKParser wraps the existing internal/apk package to conform to the Parser interface.
// It delegates all parsing to apk.Parse and converts the result to AssetInfo.
type APKParser struct{}

// Parse extracts metadata from an Android APK file.
// Unlike ELF/Mach-O parsers, APKs embed rich metadata (package ID, version, name, icon)
// so these fields are populated directly.
func (p *APKParser) Parse(path string) (*AssetInfo, error) {
	apkInfo, err := apk.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse APK: %w", err)
	}

	return FromAPKInfo(apkInfo), nil
}

// FromAPKInfo converts an existing apk.APKInfo to an AssetInfo.
// This allows callers that already have an APKInfo to produce an AssetInfo
// without re-parsing the file.
func FromAPKInfo(info *apk.APKInfo) *AssetInfo {
	// Convert Android ABI names to NIP-82 platform identifiers.
	platforms := make([]string, 0, len(info.Architectures))
	for _, arch := range info.Architectures {
		platforms = append(platforms, androidABIToPlatform(arch))
	}

	// If no native libraries, the APK is architecture-independent (pure Java/Kotlin).
	// Per convention, list all Android platforms.
	if len(platforms) == 0 {
		platforms = []string{
			"android-arm64-v8a",
			"android-armeabi-v7a",
			"android-x86",
			"android-x86_64",
		}
	}

	return &AssetInfo{
		Identifier: info.PackageID,
		Name:       info.Label,
		Version:    info.VersionName,
		FilePath:   info.FilePath,
		FileSize:   info.FileSize,
		SHA256:     info.SHA256,
		MIMEType:   MIMEAndroidAPK,
		Platforms:  platforms,
		Icon:       info.Icon,
		APK: &APKMeta{
			VersionCode:     info.VersionCode,
			MinSDK:          info.MinSDK,
			TargetSDK:       info.TargetSDK,
			CertFingerprint: info.CertFingerprint,
			Architectures:   info.Architectures,
			Permissions:     info.Permissions,
		},
	}
}

// ToAPKInfo converts an AssetInfo back to apk.APKInfo.
// This is useful for code paths that still expect the legacy APKInfo type
// during the migration period.
func ToAPKInfo(info *AssetInfo) *apk.APKInfo {
	if !info.IsAPK() {
		return nil
	}

	return &apk.APKInfo{
		PackageID:       info.Identifier,
		VersionName:     info.Version,
		VersionCode:     info.APK.VersionCode,
		MinSDK:          info.APK.MinSDK,
		TargetSDK:       info.APK.TargetSDK,
		Label:           info.Name,
		Architectures:   info.APK.Architectures,
		Permissions:     info.APK.Permissions,
		CertFingerprint: info.APK.CertFingerprint,
		Icon:            info.Icon,
		FilePath:        info.FilePath,
		FileSize:        info.FileSize,
		SHA256:          info.SHA256,
	}
}

// androidABIToPlatform converts Android ABI names to NIP-82 platform identifiers.
func androidABIToPlatform(abi string) string {
	switch abi {
	case "arm64-v8a":
		return "android-arm64-v8a"
	case "armeabi-v7a":
		return "android-armeabi-v7a"
	case "x86":
		return "android-x86"
	case "x86_64":
		return "android-x86_64"
	default:
		return "android-" + abi
	}
}
