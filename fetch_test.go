package zsp

import (
	"reflect"
	"testing"

	"github.com/zapstore/zsp/internal/source"
)

func TestFilterCandidatesKeepsAPKMediaTypeWithoutAPKSuffix(t *testing.T) {
	assets := []*source.Asset{
		{
			Name:        "6f1c0a.bin",
			URL:         "https://r2a.primal.net/blob/6f1c0a.bin",
			ContentType: "application/vnd.android.package-archive",
			Size:        10956212,
		},
		{Name: "other.bin", URL: "https://r2a.primal.net/blob/other.bin"},
		{Name: "notes.txt", URL: "https://example.com/notes.txt"},
	}

	candidates, err := filterCandidates(assets, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Name != "6f1c0a.bin" {
		t.Fatalf("filterCandidates() = %+v, want the content-typed APK", candidates)
	}
}

func TestFilterCandidatesPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		assets []string
		match  string
		want   []string
	}{
		{
			name: "explicit arm64 omits universal variants",
			assets: []string{
				"amber-arm64-v8a-v6.6.4.apk",
				"amber-fdroid-universal-v6.6.4.apk",
				"amber-offline-arm64-v8a-v6.6.4.apk",
				"amber-offline-universal-v6.6.4.apk",
				"amber-universal-v6.6.4.apk",
			},
			want: []string{
				"amber-arm64-v8a-v6.6.4.apk",
				"amber-offline-arm64-v8a-v6.6.4.apk",
			},
		},
		{
			name:   "universal remains when no explicit arm64 exists",
			assets: []string{"app-universal.apk", "app-offline-universal.apk"},
			want:   []string{"app-universal.apk", "app-offline-universal.apk"},
		},
		{
			name: "fdroid is fallback only",
			assets: []string{
				"app-fdroid-arm64-v8a.apk",
				"app-arm64-v8a.apk",
				"app-fdroid-universal.apk",
			},
			want: []string{"app-arm64-v8a.apk"},
		},
		{
			name:   "fdroid remains when it is the only source",
			assets: []string{"app-fdroid-arm64-v8a.apk"},
			want:   []string{"app-fdroid-arm64-v8a.apk"},
		},
		{
			name: "google and play are always omitted",
			assets: []string{
				"app-google-arm64-v8a.apk",
				"app-play-arm64-v8a.apk",
				"app-playstore-arm64-v8a.apk",
			},
			want: []string{},
		},
		{
			name: "labels are token aware",
			assets: []string{
				"app-googleplayful-arm64-v8a.apk",
				"app-fdroidian-arm64-v8a.apk",
				"app-universalized.apk",
			},
			want: []string{
				"app-googleplayful-arm64-v8a.apk",
				"app-fdroidian-arm64-v8a.apk",
				"app-universalized.apk",
			},
		},
		{
			name: "match narrows candidates before precedence",
			assets: []string{
				"app-arm64-v8a.apk",
				"app-fdroid-universal.apk",
				"app-universal.apk",
			},
			match: `fdroid`,
			want:  []string{"app-fdroid-universal.apk"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assets := make([]*source.Asset, 0, len(test.assets))
			for _, name := range test.assets {
				assets = append(assets, &source.Asset{Name: name})
			}

			candidates, err := filterCandidates(assets, test.match)
			if err != nil {
				t.Fatal(err)
			}

			got := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				got = append(got, candidate.Name)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("filterCandidates() = %v, want %v", got, test.want)
			}
		})
	}
}
