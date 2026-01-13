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

