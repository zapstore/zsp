// Package picker implements asset selection using a KNN-based scoring algorithm.
// It ranks release assets (APKs, native executables, etc.) by quality and
// platform relevance, selecting the best candidate for the host platform.
package picker

import (
	_ "embed"
	"encoding/csv"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/zapstore/zsp/internal/artifact"
	"github.com/zapstore/zsp/internal/source"
)

//go:embed training.csv
var trainingData string

// Feature represents a binary feature extracted from a filename and platform context.
type Feature int

const (
	// --- APK quality features (kept at original indices for compatibility) ---

	FeatureArm64      Feature = iota // arm64, arm64-v8a in filename
	FeatureFDroid                    // fdroid
	FeatureFoss                      // foss
	FeatureLibre                     // libre
	FeatureOss                       // oss
	FeatureRelease                   // release
	FeatureUniversal                 // universal
	FeatureGoogle                    // google
	FeaturePlaystore                 // playstore
	FeatureGms                       // gms
	FeatureDebug                     // debug
	FeatureBeta                      // beta
	FeatureAlpha                     // alpha
	FeatureRC                        // rc
	FeatureX86                       // x86 (32-bit)
	FeatureX86_64                    // x86_64
	FeatureArmeabi                   // armeabi (32-bit ARM)
	FeatureArmeabiV7a                // armeabi-v7a

	// --- Platform OS detection features ---

	FeatureLinux   // "linux" in filename
	FeatureDarwin  // "darwin" or "macos" or "apple" in filename
	FeatureWindows // "windows" or "win64"/"win32" in filename
	FeatureFreeBSD // "freebsd" in filename
	FeatureAndroid // "android" in filename or .apk extension

	// --- Architecture detection features (beyond APK-specific) ---

	FeatureAmd64   // "amd64" in filename (alias for x86_64)
	FeatureAarch64 // "aarch64" in filename (ARM64 on Linux)

	// --- Native executable quality features ---

	FeatureMusl   // "musl" — statically linked C library (preferred for Linux)
	FeatureStatic // "static" — explicitly static build
	FeatureGnu    // "gnu" — dynamically linked glibc (less portable)

	// --- Negative format features (unsupported per NIP-82) ---

	FeatureArchive   // .tar.gz, .tar.xz, .zip, .tgz, etc.
	FeatureChecksum  // .sha256, .sha512, .md5, checksums.txt, etc.
	FeatureSignature // .sig, .asc, .gpg, .minisig
	FeatureDeb       // .deb package
	FeatureRpm       // .rpm package

	// --- Asset type features ---

	FeatureAPKFile   // .apk extension
	FeatureAppImage  // .AppImage extension
	FeatureNightly   // "nightly" in filename
	FeaturePrereleaseTag // "pre", "preview", "dev" in filename

	// --- Dynamic match features (computed against host platform at runtime) ---

	FeatureMatchesHostOS   // detected OS matches host OS
	FeatureMatchesHostArch // detected arch matches host arch

	NumFeatures
)

// featurePatterns maps features to their regex patterns for filename matching.
var featurePatterns = map[Feature]*regexp.Regexp{
	// APK features
	FeatureArm64:      regexp.MustCompile(`(?i)(arm64|arm64-v8a)`),
	FeatureFDroid:     regexp.MustCompile(`(?i)fdroid`),
	FeatureFoss:       regexp.MustCompile(`(?i)foss`),
	FeatureLibre:      regexp.MustCompile(`(?i)libre`),
	FeatureOss:        regexp.MustCompile(`(?i)\boss\b`),
	FeatureRelease:    regexp.MustCompile(`(?i)release`),
	FeatureUniversal:  regexp.MustCompile(`(?i)universal`),
	FeatureGoogle:     regexp.MustCompile(`(?i)google`),
	FeaturePlaystore:  regexp.MustCompile(`(?i)playstore`),
	FeatureGms:        regexp.MustCompile(`(?i)gms`),
	FeatureDebug:      regexp.MustCompile(`(?i)debug`),
	FeatureBeta:       regexp.MustCompile(`(?i)beta`),
	FeatureAlpha:      regexp.MustCompile(`(?i)alpha`),
	FeatureRC:         regexp.MustCompile(`(?i)\brc\b`),
	FeatureX86:        regexp.MustCompile(`(?i)\bx86\b`),
	FeatureX86_64:     regexp.MustCompile(`(?i)x86_64`),
	FeatureArmeabi:    regexp.MustCompile(`(?i)\barmeabi\b`),
	FeatureArmeabiV7a: regexp.MustCompile(`(?i)armeabi-v7a`),

	// Platform OS detection
	FeatureLinux:   regexp.MustCompile(`(?i)\blinux\b`),
	FeatureDarwin:  regexp.MustCompile(`(?i)(darwin|macos|\bapple\b)`),
	FeatureWindows: regexp.MustCompile(`(?i)(windows|win64|win32|\bwin\b)`),
	FeatureFreeBSD: regexp.MustCompile(`(?i)freebsd`),
	FeatureAndroid: regexp.MustCompile(`(?i)(android|\.apk$)`),

	// Architecture detection
	FeatureAmd64:   regexp.MustCompile(`(?i)\bamd64\b`),
	FeatureAarch64: regexp.MustCompile(`(?i)\baarch64\b`),

	// Build quality
	FeatureMusl:   regexp.MustCompile(`(?i)\bmusl\b`),
	FeatureStatic: regexp.MustCompile(`(?i)\bstatic\b`),
	FeatureGnu:    regexp.MustCompile(`(?i)\bgnu\b`),

	// Negative format features
	FeatureArchive:   regexp.MustCompile(`(?i)\.(tar\.gz|tar\.xz|tar\.bz2|tar\.zst|tgz|zip|gz|xz|bz2|zst|7z|rar)$`),
	FeatureChecksum:  regexp.MustCompile(`(?i)(\.(sha256|sha512|sha256sum|sha512sum|md5|md5sum)$|checksums|SHASUMS)`),
	FeatureSignature: regexp.MustCompile(`(?i)\.(sig|asc|gpg|minisig|pem|cert)$`),
	FeatureDeb:       regexp.MustCompile(`(?i)\.deb$`),
	FeatureRpm:       regexp.MustCompile(`(?i)\.rpm$`),

	// Asset type
	FeatureAPKFile:  regexp.MustCompile(`(?i)\.apk$`),
	FeatureAppImage: regexp.MustCompile(`(?i)\.appimage$`),
	FeatureNightly:  regexp.MustCompile(`(?i)nightly`),
	FeaturePrereleaseTag: regexp.MustCompile(`(?i)(\bpre\b|\bpreview\b|\bdev\b)`),
}

// featureWeights assigns weights to features based on their importance.
// Used by ScoreWithWeights as a fallback when KNN is not available.
// Positive weights favor selection, negative weights disfavor.
var featureWeights = map[Feature]float64{
	// APK preferences
	FeatureArm64:      2.0,
	FeatureFDroid:     1.5,
	FeatureFoss:       1.5,
	FeatureLibre:      1.5,
	FeatureOss:        1.5,
	FeatureRelease:    1.0,
	FeatureUniversal:  -0.5,
	FeatureGoogle:     -2.0,
	FeaturePlaystore:  -2.0,
	FeatureGms:        -2.0,
	FeatureDebug:      -3.0,
	FeatureBeta:       -1.5,
	FeatureAlpha:      -2.0,
	FeatureRC:         -1.0,
	FeatureX86:        -2.0,
	FeatureX86_64:     -2.0,
	FeatureArmeabi:    -1.5,
	FeatureArmeabiV7a: -1.0,

	// Native executable preferences
	FeatureMusl:   1.5, // musl = static, portable
	FeatureStatic: 1.0, // explicitly static
	FeatureGnu:    -0.5, // glibc = less portable

	// Negative format features (strong penalties)
	FeatureArchive:   -5.0,
	FeatureChecksum:  -5.0,
	FeatureSignature: -5.0,
	FeatureDeb:       -5.0,
	FeatureRpm:       -5.0,

	// Pre-release
	FeatureNightly:       -2.0,
	FeaturePrereleaseTag: -1.5,

	// Note: FeatureMatchesHostOS and FeatureMatchesHostArch are intentionally
	// omitted from weights. They are used by the KNN model (where they interact
	// with training data) but not by the simple weighted scorer, where they
	// would add a constant bias within a single platform context.
}

// Sample represents a training sample with features and label.
type Sample struct {
	Filename     string
	HostPlatform string    // Platform context for dynamic features
	Features     []float64
	Label        float64 // -1, 0, or 1
}

// Model represents a trained KNN model for asset ranking.
type Model struct {
	samples []Sample
	k       int
}

// ScoredAsset represents an asset with its computed score.
type ScoredAsset struct {
	Asset *source.Asset
	Score float64
}

// DefaultModel is the model trained from embedded training data.
var DefaultModel *Model

func init() {
	var err error
	DefaultModel, err = TrainFromCSV(trainingData)
	if err != nil {
		// This should never happen with valid embedded data.
		panic(fmt.Sprintf("failed to train default model: %v", err))
	}
}

// TrainFromCSV trains a model from CSV data.
// Supports two formats:
//   - 2 columns: filename,label (legacy; platform defaults to android-arm64-v8a for .apk files)
//   - 3 columns: filename,platform,label
func TrainFromCSV(data string) (*Model, error) {
	// Pre-process: strip comment lines and blank lines before CSV parsing.
	var cleanLines []string
	for _, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cleanLines = append(cleanLines, line)
	}
	cleanData := strings.Join(cleanLines, "\n")

	reader := csv.NewReader(strings.NewReader(cleanData))
	reader.FieldsPerRecord = -1 // Allow variable field count
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to parse CSV: %w", err)
	}

	if len(records) < 2 {
		return nil, fmt.Errorf("CSV must have header and at least one data row")
	}

	// Detect format from header.
	header := records[0]
	hasPlatformCol := len(header) >= 3

	// Skip header.
	records = records[1:]

	samples := make([]Sample, 0, len(records))
	for _, record := range records {
		if len(record) < 2 {
			continue
		}

		filename := strings.TrimSpace(record[0])
		if filename == "" {
			continue
		}

		var hostPlatform string
		var labelStr string

		if hasPlatformCol && len(record) >= 3 {
			hostPlatform = strings.TrimSpace(record[1])
			labelStr = strings.TrimSpace(record[2])
		} else {
			labelStr = strings.TrimSpace(record[1])
		}

		// Default platform based on filename if not specified.
		if hostPlatform == "" {
			if strings.HasSuffix(strings.ToLower(filename), ".apk") {
				hostPlatform = "android-arm64-v8a"
			} else {
				hostPlatform = artifact.HostPlatform()
			}
		}

		var label float64
		switch labelStr {
		case "1":
			label = 1
		case "-1":
			label = -1
		default:
			label = 0
		}

		samples = append(samples, Sample{
			Filename:     filename,
			HostPlatform: hostPlatform,
			Features:     ExtractFeaturesForPlatform(filename, hostPlatform),
			Label:        label,
		})
	}

	return &Model{
		samples: samples,
		k:       5, // Use 5 nearest neighbors
	}, nil
}

// ExtractFeatures extracts a feature vector from a filename using the host platform.
// This is the backward-compatible version that defaults to android-arm64-v8a for APK
// filenames and the current host platform for everything else.
func ExtractFeatures(filename string) []float64 {
	hostPlatform := artifact.HostPlatform()
	if strings.HasSuffix(strings.ToLower(filename), ".apk") {
		hostPlatform = "android-arm64-v8a"
	}
	return ExtractFeaturesForPlatform(filename, hostPlatform)
}

// ExtractFeaturesForPlatform extracts a feature vector from a filename with
// explicit host platform context. The host platform determines the dynamic
// match features (FeatureMatchesHostOS, FeatureMatchesHostArch).
func ExtractFeaturesForPlatform(filename, hostPlatform string) []float64 {
	features := make([]float64, NumFeatures)

	// Static features from filename patterns.
	for f, pattern := range featurePatterns {
		if pattern.MatchString(filename) {
			features[f] = 1.0
		}
	}

	// Dynamic platform match features.
	fileOS := artifact.PlatformOSFromFilename(filename)
	fileArch := artifact.PlatformArchFromFilename(filename)

	hostOS, hostArch := artifact.SplitPlatform(hostPlatform)

	// OS match: file's OS matches host OS, or file has no OS indicator.
	if fileOS != "" && hostOS != "" {
		if fileOS == hostOS {
			features[FeatureMatchesHostOS] = 1.0
		}
	} else if fileOS == "" {
		// No OS detected — could be platform-agnostic or just unnamed.
		// Give partial credit for APKs targeting android.
		if strings.HasSuffix(strings.ToLower(filename), ".apk") && hostOS == "android" {
			features[FeatureMatchesHostOS] = 1.0
		}
	}

	// Arch match: normalize then compare.
	if fileArch != "" && hostArch != "" {
		if archMatches(fileArch, hostArch) {
			features[FeatureMatchesHostArch] = 1.0
		}
	} else if fileArch == "" {
		// No arch detected — could be universal or unnamed.
		// Partial credit for universal builds.
		if features[FeatureUniversal] == 1.0 {
			features[FeatureMatchesHostArch] = 0.5
		}
	}

	return features
}

// archMatches returns true if two architecture identifiers refer to the same architecture,
// accounting for naming variations (amd64/x86_64, arm64/aarch64/arm64-v8a).
func archMatches(a, b string) bool {
	a = normalizeArch(a)
	b = normalizeArch(b)
	return a == b
}

// normalizeArch normalizes architecture names to a canonical form.
func normalizeArch(arch string) string {
	switch strings.ToLower(arch) {
	case "x86_64", "amd64", "x86-64":
		return "x86_64"
	case "aarch64", "arm64", "arm64-v8a":
		return "arm64"
	case "armv7l", "armeabi-v7a", "armhf", "arm":
		return "armv7"
	case "x86", "i686", "i386", "386":
		return "x86"
	case "riscv64":
		return "riscv64"
	default:
		return strings.ToLower(arch)
	}
}

// Score computes a score for a filename using KNN with the host platform as context.
func (m *Model) Score(filename string) float64 {
	return m.ScoreForPlatform(filename, defaultHostPlatform(filename))
}

// ScoreForPlatform computes a score for a filename using KNN with explicit platform context.
func (m *Model) ScoreForPlatform(filename, hostPlatform string) float64 {
	features := ExtractFeaturesForPlatform(filename, hostPlatform)

	// Calculate distances to all training samples.
	type distLabel struct {
		dist  float64
		label float64
	}
	distances := make([]distLabel, len(m.samples))
	for i, sample := range m.samples {
		distances[i] = distLabel{
			dist:  euclideanDistance(features, sample.Features),
			label: sample.Label,
		}
	}

	// Sort by distance.
	sort.Slice(distances, func(i, j int) bool {
		return distances[i].dist < distances[j].dist
	})

	// Take k nearest neighbors and compute weighted average.
	k := m.k
	if k > len(distances) {
		k = len(distances)
	}

	var sum float64
	for i := 0; i < k; i++ {
		// Weight by inverse distance (closer = more weight).
		weight := 1.0
		if distances[i].dist > 0 {
			weight = 1.0 / (1.0 + distances[i].dist)
		}
		sum += distances[i].label * weight
	}

	return sum / float64(k)
}

// ScoreWithWeights computes a score using feature weights (alternative to KNN).
func ScoreWithWeights(filename string) float64 {
	return ScoreWithWeightsForPlatform(filename, defaultHostPlatform(filename))
}

// ScoreWithWeightsForPlatform computes a weighted score with explicit platform context.
func ScoreWithWeightsForPlatform(filename, hostPlatform string) float64 {
	features := ExtractFeaturesForPlatform(filename, hostPlatform)
	var score float64
	for f := Feature(0); f < NumFeatures; f++ {
		if w, ok := featureWeights[f]; ok {
			score += features[f] * w
		}
	}
	return score
}

// euclideanDistance computes Euclidean distance between two feature vectors.
func euclideanDistance(a, b []float64) float64 {
	var sum float64
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		diff := a[i] - b[i]
		sum += diff * diff
	}
	// Treat missing dimensions as 0.
	for i := minLen; i < len(a); i++ {
		sum += a[i] * a[i]
	}
	for i := minLen; i < len(b); i++ {
		sum += b[i] * b[i]
	}
	return math.Sqrt(sum)
}

// RankAssets ranks assets by their scores (highest first) using the host platform.
func (m *Model) RankAssets(assets []*source.Asset) []ScoredAsset {
	return m.RankAssetsForPlatform(assets, artifact.HostPlatform())
}

// RankAssetsForPlatform ranks assets by their scores for a specific platform.
func (m *Model) RankAssetsForPlatform(assets []*source.Asset, hostPlatform string) []ScoredAsset {
	scored := make([]ScoredAsset, len(assets))
	for i, asset := range assets {
		scored[i] = ScoredAsset{
			Asset: asset,
			Score: m.ScoreForPlatform(asset.Name, hostPlatform),
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	return scored
}

// PickBest returns the best asset from a list, or nil if empty.
func (m *Model) PickBest(assets []*source.Asset) *source.Asset {
	return m.PickBestForPlatform(assets, artifact.HostPlatform())
}

// PickBestForPlatform returns the best asset for a specific platform.
func (m *Model) PickBestForPlatform(assets []*source.Asset, hostPlatform string) *source.Asset {
	if len(assets) == 0 {
		return nil
	}

	ranked := m.RankAssetsForPlatform(assets, hostPlatform)
	return ranked[0].Asset
}

// ---------------------------------------------------------------------------
// Filtering helpers
// ---------------------------------------------------------------------------

// FilterAPKs filters assets to only include .apk files.
// Checks both the asset name and URL for .apk extension.
func FilterAPKs(assets []*source.Asset) []*source.Asset {
	var apks []*source.Asset
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		url := strings.ToLower(asset.URL)
		if strings.HasSuffix(name, ".apk") || strings.HasSuffix(url, ".apk") {
			apks = append(apks, asset)
		}
	}
	return apks
}

// FilterSupported filters assets to those with supported filenames.
// Removes archives, checksums, signatures, .deb, .rpm, and other
// formats not supported by NIP-82.
func FilterSupported(assets []*source.Asset) []*source.Asset {
	var supported []*source.Asset
	for _, asset := range assets {
		if artifact.IsSupportedAssetFilename(asset.Name) {
			supported = append(supported, asset)
		}
	}
	return supported
}

// FilterByMatch filters assets using a regex pattern.
func FilterByMatch(assets []*source.Asset, pattern string) ([]*source.Asset, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid match pattern: %w", err)
	}

	var matched []*source.Asset
	for _, asset := range assets {
		if re.MatchString(asset.Name) {
			matched = append(matched, asset)
		}
	}
	return matched, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// defaultHostPlatform returns the appropriate host platform for scoring a filename.
// APK filenames default to android-arm64-v8a; everything else uses the actual host.
func defaultHostPlatform(filename string) string {
	if strings.HasSuffix(strings.ToLower(filename), ".apk") {
		return "android-arm64-v8a"
	}
	return artifact.HostPlatform()
}
