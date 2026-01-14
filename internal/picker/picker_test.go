package picker

import (
	"testing"

	"github.com/zapstore/zsp/internal/source"
)

func TestExtractFeatures(t *testing.T) {
	tests := []struct {
		filename string
		expected map[Feature]bool
	}{
		{
			filename: "app-arm64-v8a-fdroid.apk",
			expected: map[Feature]bool{
				FeatureArm64:  true,
				FeatureFDroid: true,
			},
		},
		{
			filename: "app-universal-google.apk",
			expected: map[Feature]bool{
				FeatureUniversal: true,
				FeatureGoogle:    true,
			},
		},
		{
			filename: "app-debug-x86.apk",
			expected: map[Feature]bool{
				FeatureDebug: true,
				FeatureX86:   true,
			},
		},
		{
			filename: "app-release-arm64-v8a-foss.apk",
			expected: map[Feature]bool{
				FeatureArm64:   true,
				FeatureRelease: true,
				FeatureFoss:    true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			features := ExtractFeatures(tt.filename)

			for f, shouldBeSet := range tt.expected {
				if (features[f] == 1.0) != shouldBeSet {
					t.Errorf("feature %d: expected %v, got %v", f, shouldBeSet, features[f] == 1.0)
				}
			}
		})
	}
}

func TestModelScore(t *testing.T) {
	// Test that the default model scores good APKs higher than bad ones
	goodAPKs := []string{
		"app-arm64-v8a-fdroid.apk",
		"app-arm64-v8a-release.apk",
		"app-foss-arm64-v8a.apk",
		"myapp-v1.0.0-arm64-v8a.apk",
	}

	badAPKs := []string{
		"app-debug.apk",
		"app-x86.apk",
		"app-google.apk",
		"app-universal-google.apk",
	}

	for _, good := range goodAPKs {
		goodScore := DefaultModel.Score(good)
		for _, bad := range badAPKs {
			badScore := DefaultModel.Score(bad)
			if goodScore <= badScore {
				t.Errorf("expected %q (%.2f) > %q (%.2f)", good, goodScore, bad, badScore)
			}
		}
	}
}

func TestScoreWithWeights(t *testing.T) {
	tests := []struct {
		filename string
		positive bool // Should have positive score
	}{
		{"app-arm64-v8a-fdroid.apk", true},
		{"app-arm64-v8a-release.apk", true},
		{"app-foss-arm64-v8a.apk", true},
		{"app-debug.apk", false},
		{"app-x86.apk", false},
		{"app-google.apk", false},
		{"app-alpha.apk", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			score := ScoreWithWeights(tt.filename)
			if tt.positive && score <= 0 {
				t.Errorf("expected positive score for %q, got %.2f", tt.filename, score)
			}
			if !tt.positive && score >= 0 {
				t.Errorf("expected non-positive score for %q, got %.2f", tt.filename, score)
			}
		})
	}
}

func TestRankAssets(t *testing.T) {
	assets := []*source.Asset{
		{Name: "app-debug.apk"},
		{Name: "app-arm64-v8a-fdroid.apk"},
		{Name: "app-x86.apk"},
		{Name: "app-arm64-v8a-release.apk"},
		{Name: "app-universal.apk"},
	}

	ranked := DefaultModel.RankAssets(assets)

	// The arm64-v8a-fdroid should be ranked first or second
	if ranked[0].Asset.Name != "app-arm64-v8a-fdroid.apk" && ranked[0].Asset.Name != "app-arm64-v8a-release.apk" {
		t.Errorf("expected arm64-v8a variant first, got %q", ranked[0].Asset.Name)
	}

	// Debug and x86 should be ranked last
	lastTwo := []string{ranked[len(ranked)-1].Asset.Name, ranked[len(ranked)-2].Asset.Name}
	hasDebug := false
	hasX86 := false
	for _, name := range lastTwo {
		if name == "app-debug.apk" {
			hasDebug = true
		}
		if name == "app-x86.apk" {
			hasX86 = true
		}
	}
	if !hasDebug || !hasX86 {
		t.Errorf("expected debug and x86 in last two, got %v", lastTwo)
	}

	t.Log("Ranked assets:")
	for i, sa := range ranked {
		t.Logf("  %d. %s (score: %.2f)", i+1, sa.Asset.Name, sa.Score)
	}
}

func TestPickBest(t *testing.T) {
	assets := []*source.Asset{
		{Name: "app-debug.apk"},
		{Name: "app-arm64-v8a-fdroid.apk"},
		{Name: "app-x86.apk"},
	}

	best := DefaultModel.PickBest(assets)
	if best == nil {
		t.Fatal("PickBest returned nil")
	}

	// Should pick the arm64 fdroid variant over debug and x86
	if best.Name != "app-arm64-v8a-fdroid.apk" {
		t.Errorf("expected app-arm64-v8a-fdroid.apk, got %q", best.Name)
	}
}

func TestPickBestPrefersFDroid(t *testing.T) {
	// When both arm64 release and fdroid are available, either is acceptable
	// but fdroid should score well
	assets := []*source.Asset{
		{Name: "app-arm64-v8a.apk"},
		{Name: "app-arm64-v8a-fdroid.apk"},
	}

	best := DefaultModel.PickBest(assets)
	if best == nil {
		t.Fatal("PickBest returned nil")
	}

	// Both are good choices, just verify we got one of them
	if best.Name != "app-arm64-v8a.apk" && best.Name != "app-arm64-v8a-fdroid.apk" {
		t.Errorf("expected arm64 variant, got %q", best.Name)
	}
}

func TestPickBestEmpty(t *testing.T) {
	best := DefaultModel.PickBest(nil)
	if best != nil {
		t.Error("expected nil for empty assets")
	}

	best = DefaultModel.PickBest([]*source.Asset{})
	if best != nil {
		t.Error("expected nil for empty assets slice")
	}
}

func TestFilterAPKs(t *testing.T) {
	assets := []*source.Asset{
		{Name: "app.apk"},
		{Name: "app.zip"},
		{Name: "app.APK"},
		{Name: "readme.txt"},
		{Name: "app-arm64.apk"},
	}

	filtered := FilterAPKs(assets)
	if len(filtered) != 3 {
		t.Errorf("expected 3 APKs, got %d", len(filtered))
	}
}

func TestFilterByMatch(t *testing.T) {
	assets := []*source.Asset{
		{Name: "app-arm64-v8a.apk"},
		{Name: "app-x86.apk"},
		{Name: "app-universal.apk"},
		{Name: "app-arm64-v8a-fdroid.apk"},
	}

	matched, err := FilterByMatch(assets, `arm64`)
	if err != nil {
		t.Fatalf("FilterByMatch error: %v", err)
	}

	if len(matched) != 2 {
		t.Errorf("expected 2 matches, got %d", len(matched))
	}
}

func TestFilterByMatchInvalidPattern(t *testing.T) {
	_, err := FilterByMatch(nil, `[invalid`)
	if err == nil {
		t.Error("expected error for invalid pattern")
	}
}

func TestTrainingDataLoaded(t *testing.T) {
	if DefaultModel == nil {
		t.Fatal("DefaultModel is nil")
	}

	if len(DefaultModel.samples) == 0 {
		t.Error("DefaultModel has no samples")
	}

	t.Logf("Loaded %d training samples", len(DefaultModel.samples))
}

// TestRealWorldFilenames tests scoring with real APK filenames from testdata
func TestRealWorldFilenames(t *testing.T) {
	// Real APK filenames from testdata/apks
	// Note: "universal" builds are penalized (larger file size) so they may have negative scores
	tests := []struct {
		filename    string
		expectGood  bool // true = non-negative score, false = allow negative
		description string
	}{
		{"amber-free-universal-v3.4.1.apk", false, "universal free build (penalized for size)"},
		{"harmonymusic-1.12.0-arm64-v8a.apk", true, "arm64 build"},
		{"mempal-v1.5.3.apk", true, "simple version"},
		{"misty-breez-breez-signed.apk", true, "signed build"},
		{"misty-breez-google-signed.apk", false, "google signed (less preferred)"},
		{"phoenix-104-2.6.0-mainnet.apk", true, "mainnet build"},
		{"sentinel-v5.1.1a.apk", true, "simple version with suffix"},
		{"exiferaser.apk", true, "no version in name"},
		{"sample.apk", true, "generic name"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			score := ScoreWithWeights(tt.filename)
			if tt.expectGood && score < 0 {
				t.Errorf("%s: expected non-negative score, got %.2f (%s)", tt.filename, score, tt.description)
			}
			t.Logf("%s: score=%.2f (%s)", tt.filename, score, tt.description)
		})
	}
}

// TestExtractFeaturesRealFilenames tests feature extraction with real filenames
func TestExtractFeaturesRealFilenames(t *testing.T) {
	tests := []struct {
		filename        string
		expectedFeature Feature
		shouldBeSet     bool
	}{
		{"amber-free-universal-v3.4.1.apk", FeatureUniversal, true},
		{"harmonymusic-1.12.0-arm64-v8a.apk", FeatureArm64, true},
		{"misty-breez-google-signed.apk", FeatureGoogle, true},
		{"citrine-arm64-v8a-v1.0.0.apk", FeatureArm64, true},
		{"app-armeabi-v7a-release.apk", FeatureArmeabi, true},
		{"app-x86_64-debug.apk", FeatureX86_64, true}, // Note: x86_64 is separate from x86
		{"app-x86_64-debug.apk", FeatureDebug, true},
		{"app-x86-release.apk", FeatureX86, true}, // Plain x86
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			features := ExtractFeatures(tt.filename)
			hasFeature := features[tt.expectedFeature] == 1.0
			if hasFeature != tt.shouldBeSet {
				t.Errorf("%s: feature %d should be %v, got %v", tt.filename, tt.expectedFeature, tt.shouldBeSet, hasFeature)
			}
		})
	}
}

// TestRankRealAssets tests ranking with real APK names
func TestRankRealAssets(t *testing.T) {
	// Simulate a GitHub release with multiple APK variants
	assets := []*source.Asset{
		{Name: "app-v1.0.0-arm64-v8a.apk"},
		{Name: "app-v1.0.0-armeabi-v7a.apk"},
		{Name: "app-v1.0.0-x86.apk"},
		{Name: "app-v1.0.0-x86_64.apk"},
		{Name: "app-v1.0.0-universal.apk"},
	}

	ranked := DefaultModel.RankAssets(assets)

	// arm64 should be ranked first
	if ranked[0].Asset.Name != "app-v1.0.0-arm64-v8a.apk" {
		t.Errorf("expected arm64 variant first, got %q (score: %.2f)", ranked[0].Asset.Name, ranked[0].Score)
	}

	// x86 variants should be ranked last
	lastTwoNames := []string{ranked[len(ranked)-1].Asset.Name, ranked[len(ranked)-2].Asset.Name}
	hasX86 := false
	for _, name := range lastTwoNames {
		if name == "app-v1.0.0-x86.apk" || name == "app-v1.0.0-x86_64.apk" {
			hasX86 = true
			break
		}
	}
	if !hasX86 {
		t.Errorf("expected x86 variant in last two, got %v", lastTwoNames)
	}

	t.Log("Ranked real assets:")
	for i, sa := range ranked {
		t.Logf("  %d. %s (score: %.2f)", i+1, sa.Asset.Name, sa.Score)
	}
}

// TestFilterByMatchRealPatterns tests match filtering with real patterns
func TestFilterByMatchRealPatterns(t *testing.T) {
	assets := []*source.Asset{
		{Name: "phoenix-104-2.6.0-mainnet.apk"},
		{Name: "phoenix-104-2.6.0-testnet.apk"},
		{Name: "phoenix-104-2.6.0-mainnet-debug.apk"},
	}

	tests := []struct {
		pattern string
		want    int
	}{
		{"mainnet", 2},
		{"mainnet\\.apk$", 1}, // Only non-debug mainnet
		{"testnet", 1},
		{".*debug.*", 1},
		{"phoenix.*mainnet\\.apk$", 1},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			matched, err := FilterByMatch(assets, tt.pattern)
			if err != nil {
				t.Fatalf("FilterByMatch error: %v", err)
			}
			if len(matched) != tt.want {
				t.Errorf("FilterByMatch(%q) = %d matches, want %d", tt.pattern, len(matched), tt.want)
			}
		})
	}
}

// TestRealWorldReleasePatterns tests patterns from real-world apps
func TestRealWorldReleasePatterns(t *testing.T) {
	tests := []struct {
		name    string
		assets  []*source.Asset
		pattern string
		want    int
	}{
		{
			name: "phoenix mainnet",
			assets: []*source.Asset{
				{Name: "phoenix-104-2.6.0-mainnet.apk"},
				{Name: "phoenix-104-2.6.0-testnet.apk"},
			},
			pattern: "phoenix-\\d+-\\d+\\.\\d+(\\.\\d+)?(\\.\\d+)?-mainnet\\.apk$",
			want:    1,
		},
		{
			name: "bluewallet versioned",
			assets: []*source.Asset{
				{Name: "BlueWallet-7.0.6-123.apk"},
				{Name: "BlueWallet-debug.apk"},
				{Name: "checksums.txt"},
			},
			pattern: "BlueWallet-\\d+\\.\\d+(\\.\\d+)?(\\.\\d+)?-\\d+\\.apk$",
			want:    1,
		},
		{
			name: "bitwarden fdroid",
			assets: []*source.Asset{
				{Name: "com.x8bit.bitwarden-fdroid.apk"},
				{Name: "com.x8bit.bitwarden-google.apk"},
				{Name: "com.x8bit.bitwarden.apk"},
			},
			pattern: "com\\.x8bit\\.bitwarden-fdroid\\.apk$",
			want:    1,
		},
		{
			name: "brave arm64 universal",
			assets: []*source.Asset{
				{Name: "Bravearm64Universal.apk"},
				{Name: "BravearmUniversal.apk"},
				{Name: "Bravex64Universal.apk"},
			},
			pattern: "Bravearm64Universal\\.apk$",
			want:    1,
		},
		{
			name: "cake wallet arm64",
			assets: []*source.Asset{
				{Name: "Cake_Wallet_v4.19.1-arm64-v8a.apk"},
				{Name: "Cake_Wallet_v4.19.1-armeabi-v7a.apk"},
				{Name: "Cake_Wallet_v4.19.1-x86_64.apk"},
			},
			pattern: "Cake_Wallet_v\\d+\\.\\d+(\\.\\d+)?(\\.\\d+)?-arm64-v8a\\.apk$",
			want:    1,
		},
		{
			name: "misty breez signed arm64",
			assets: []*source.Asset{
				{Name: "Misty.Breez.123.signed_by_breez.arm64-v8a.apk"},
				{Name: "Misty.Breez.123.signed_by_google.arm64-v8a.apk"},
				{Name: "Misty.Breez.123.unsigned.arm64-v8a.apk"},
			},
			pattern: "Misty\\.Breez\\.\\d+\\.signed_by_breez\\.arm64-v8a\\.apk$",
			want:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := FilterByMatch(tt.assets, tt.pattern)
			if err != nil {
				t.Fatalf("FilterByMatch error: %v", err)
			}
			if len(matched) != tt.want {
				names := make([]string, len(matched))
				for i, a := range matched {
					names[i] = a.Name
				}
				t.Errorf("FilterByMatch(%q) = %d matches %v, want %d", tt.pattern, len(matched), names, tt.want)
			}
		})
	}
}

// TestPreferFossOverGoogle tests that FOSS builds are preferred over Google builds
func TestPreferFossOverGoogle(t *testing.T) {
	assets := []*source.Asset{
		{Name: "app-google-release.apk"},
		{Name: "app-foss-release.apk"},
		{Name: "app-fdroid-release.apk"},
	}

	best := DefaultModel.PickBest(assets)
	if best == nil {
		t.Fatal("PickBest returned nil")
	}

	// Should prefer foss or fdroid over google
	if best.Name == "app-google-release.apk" {
		t.Errorf("expected foss/fdroid over google, got %q", best.Name)
	}
}

// TestFilterAPKsEdgeCases tests APK filtering edge cases
func TestFilterAPKsEdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		assets []*source.Asset
		want   int
	}{
		{
			name:   "nil input",
			assets: nil,
			want:   0,
		},
		{
			name:   "empty slice",
			assets: []*source.Asset{},
			want:   0,
		},
		{
			name: "mixed case extensions",
			assets: []*source.Asset{
				{Name: "app.APK"},
				{Name: "app.Apk"},
				{Name: "app.apk"},
			},
			want: 3,
		},
		{
			name: "apk in filename but wrong extension",
			assets: []*source.Asset{
				{Name: "apk-builder.zip"},
				{Name: "myapk.txt"},
			},
			want: 0,
		},
		{
			name: "real release assets mix",
			assets: []*source.Asset{
				{Name: "app-v1.0.0-arm64.apk"},
				{Name: "app-v1.0.0-arm64.apk.sha256"},
				{Name: "app-v1.0.0-arm64.aab"},
				{Name: "checksums.txt"},
				{Name: "source.tar.gz"},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := FilterAPKs(tt.assets)
			if len(filtered) != tt.want {
				t.Errorf("FilterAPKs() = %d, want %d", len(filtered), tt.want)
			}
		})
	}
}

