// Package picker implements APK selection using a KNN-based scoring algorithm.
package picker

import (
	_ "embed"
	"encoding/csv"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/zapstore/zsp/internal/source"
)

//go:embed training.csv
var trainingData string

// Feature represents a binary feature extracted from a filename.
type Feature int

const (
	FeatureArm64 Feature = iota
	FeatureFDroid
	FeatureFoss
	FeatureLibre
	FeatureOss
	FeatureRelease
	FeatureUniversal
	FeatureGoogle
	FeaturePlaystore
	FeatureGms
	FeatureDebug
	FeatureBeta
	FeatureAlpha
	FeatureRC
	FeatureX86
	FeatureX86_64
	FeatureArmeabi
	FeatureArmeabiV7a
	NumFeatures
)

// featurePatterns maps features to their regex patterns.
var featurePatterns = map[Feature]*regexp.Regexp{
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
}

// featureWeights assigns weights to features based on their importance.
// Positive weights favor selection, negative weights disfavor.
var featureWeights = map[Feature]float64{
	FeatureArm64:      2.0,  // Strong positive: we want arm64
	FeatureFDroid:     1.5,  // Positive: F-Droid builds are preferred
	FeatureFoss:       1.5,  // Positive: FOSS builds are preferred
	FeatureLibre:      1.5,  // Positive: Libre builds are preferred
	FeatureOss:        1.5,  // Positive: OSS builds are preferred
	FeatureRelease:    1.0,  // Positive: release builds are good
	FeatureUniversal:  -0.5, // Slight negative: prefer split APKs
	FeatureGoogle:     -2.0, // Strong negative: avoid Google builds
	FeaturePlaystore:  -2.0, // Strong negative: avoid Play Store builds
	FeatureGms:        -2.0, // Strong negative: avoid GMS builds
	FeatureDebug:      -3.0, // Very negative: never want debug
	FeatureBeta:       -1.5, // Negative: avoid pre-release
	FeatureAlpha:      -2.0, // Strong negative: avoid alpha
	FeatureRC:         -1.0, // Negative: avoid release candidates
	FeatureX86:        -2.0, // Strong negative: wrong architecture
	FeatureX86_64:     -2.0, // Strong negative: wrong architecture
	FeatureArmeabi:    -1.5, // Negative: old architecture
	FeatureArmeabiV7a: -1.0, // Slight negative: prefer 64-bit
}

// Sample represents a training sample with features and label.
type Sample struct {
	Filename string
	Features []float64
	Label    float64 // -1, 0, or 1
}

// Model represents a trained KNN model.
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
		// This should never happen with valid embedded data
		panic(fmt.Sprintf("failed to train default model: %v", err))
	}
}

// TrainFromCSV trains a model from CSV data.
func TrainFromCSV(data string) (*Model, error) {
	reader := csv.NewReader(strings.NewReader(data))
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to parse CSV: %w", err)
	}

	if len(records) < 2 {
		return nil, fmt.Errorf("CSV must have header and at least one data row")
	}

	// Skip header
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

		var label float64
		switch strings.TrimSpace(record[1]) {
		case "1":
			label = 1
		case "-1":
			label = -1
		default:
			label = 0
		}

		samples = append(samples, Sample{
			Filename: filename,
			Features: ExtractFeatures(filename),
			Label:    label,
		})
	}

	return &Model{
		samples: samples,
		k:       5, // Use 5 nearest neighbors
	}, nil
}

// ExtractFeatures extracts feature vector from a filename.
func ExtractFeatures(filename string) []float64 {
	features := make([]float64, NumFeatures)
	for f, pattern := range featurePatterns {
		if pattern.MatchString(filename) {
			features[f] = 1.0
		}
	}
	return features
}

// Score computes a score for a filename using KNN.
func (m *Model) Score(filename string) float64 {
	features := ExtractFeatures(filename)

	// Calculate distances to all training samples
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

	// Sort by distance
	sort.Slice(distances, func(i, j int) bool {
		return distances[i].dist < distances[j].dist
	})

	// Take k nearest neighbors and compute weighted average
	k := m.k
	if k > len(distances) {
		k = len(distances)
	}

	var sum float64
	for i := 0; i < k; i++ {
		// Weight by inverse distance (closer = more weight)
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
	features := ExtractFeatures(filename)
	var score float64
	for f := Feature(0); f < NumFeatures; f++ {
		score += features[f] * featureWeights[f]
	}
	return score
}

// euclideanDistance computes Euclidean distance between two feature vectors.
func euclideanDistance(a, b []float64) float64 {
	var sum float64
	for i := range a {
		diff := a[i] - b[i]
		sum += diff * diff
	}
	return math.Sqrt(sum)
}

// RankAssets ranks assets by their scores (highest first).
func (m *Model) RankAssets(assets []*source.Asset) []ScoredAsset {
	scored := make([]ScoredAsset, len(assets))
	for i, asset := range assets {
		scored[i] = ScoredAsset{
			Asset: asset,
			Score: m.Score(asset.Name),
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	return scored
}

// PickBest returns the best asset from a list, or nil if empty.
func (m *Model) PickBest(assets []*source.Asset) *source.Asset {
	if len(assets) == 0 {
		return nil
	}

	ranked := m.RankAssets(assets)
	return ranked[0].Asset
}

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

