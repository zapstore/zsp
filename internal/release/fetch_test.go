package release

import (
	"reflect"
	"testing"

	"github.com/zapstore/zsp/v2/internal/source"
)

func TestSelectFetchedAPKs(t *testing.T) {
	arm64 := []string{"arm64-v8a"}
	fat := []string{"arm64-v8a", "armeabi-v7a"}
	tests := []struct {
		name string
		in   []*APK
		want []string
	}{
		{
			name: "greatest arm64 version",
			in: []*APK{
				apkChoice("old", "com.app", "cert", 10, arm64),
				apkChoice("new", "com.app", "cert", 12, arm64),
				apkChoice("mid", "com.app", "cert", 11, fat),
			},
			want: []string{"new"},
		},
		{
			name: "fat dropped when arm64-only shares the version",
			in: []*APK{
				apkChoice("fat", "com.app", "cert", 12, fat),
				apkChoice("arm", "com.app", "cert", 12, arm64),
				apkChoice("old-arm", "com.app", "cert", 11, arm64),
			},
			want: []string{"arm"},
		},
		{
			name: "higher fat kept when it is the greatest arm64 version",
			in: []*APK{
				apkChoice("arm", "com.app", "cert", 11, arm64),
				apkChoice("fat", "com.app", "cert", 12, fat),
			},
			want: []string{"fat"},
		},
		{
			name: "several arm64-only files remain",
			in: []*APK{
				apkChoice("online", "com.app", "cert", 12, arm64),
				apkChoice("offline", "com.app", "cert", 12, arm64),
				apkChoice("old", "com.app", "cert", 9, arm64),
			},
			want: []string{"online", "offline"},
		},
		{
			name: "no native code when nothing includes arm64",
			in: []*APK{
				apkChoice("old-java", "com.app", "cert", 4, nil),
				apkChoice("java", "com.app", "cert", 8, nil),
			},
			want: []string{"java"},
		},
		{
			name: "arm64 preferred over a higher java version",
			in: []*APK{
				apkChoice("java", "com.app", "cert", 20, nil),
				apkChoice("arm", "com.app", "cert", 12, arm64),
			},
			want: []string{"arm"},
		},
		{
			name: "each app keeps its own greatest version",
			in: []*APK{
				apkChoice("a-old", "com.a", "cert-a", 2, arm64),
				apkChoice("b", "com.b", "cert-b", 9, arm64),
				apkChoice("a-new", "com.a", "cert-a", 4, arm64),
			},
			want: []string{"b", "a-new"},
		},
		{
			name: "same package with two certificates stays two apps",
			in: []*APK{
				apkChoice("first", "com.app", "cert-a", 2, arm64),
				apkChoice("second", "com.app", "cert-b", 3, arm64),
			},
			want: []string{"first", "second"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := selectFetchedAPKs(test.in)
			names := make([]string, len(got))
			for i, candidate := range got {
				names[i] = candidate.Filename
			}
			if !reflect.DeepEqual(names, test.want) {
				t.Fatalf("selectFetchedAPKs() = %v, want %v", names, test.want)
			}
		})
	}
}

func apkChoice(name, appID, certificate string, version int64, architectures []string) *APK {
	return &APK{
		Filename:        name,
		AppID:           appID,
		CertificateHash: certificate,
		VersionCode:     version,
		Architectures:   architectures,
	}
}

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
			name: "debug and test-signed lose to an fdroid release",
			assets: []string{
				"antivocale-debug-arm64-v8a.apk",
				"antivocale-fdroid-testsigned-arm64-v8a.apk",
				"app-fdroid-arm64-v8a-release.apk",
			},
			want: []string{"app-fdroid-arm64-v8a-release.apk"},
		},
		{
			name:   "debug remains when it is the only build",
			assets: []string{"app-debug-arm64-v8a.apk"},
			want:   []string{"app-debug-arm64-v8a.apk"},
		},
		{
			name:   "test-signed remains when it is the only build",
			assets: []string{"app-test-signed-arm64-v8a.apk"},
			want:   []string{"app-test-signed-arm64-v8a.apk"},
		},
		{
			name: "production release beats debug and fdroid",
			assets: []string{
				"app-debug-arm64-v8a.apk",
				"app-fdroid-arm64-v8a.apk",
				"app-arm64-v8a.apk",
			},
			want: []string{"app-arm64-v8a.apk"},
		},
		{
			name: "debug token does not match a longer word",
			assets: []string{
				"app-debugging-arm64-v8a.apk",
				"app-debug-arm64-v8a.apk",
			},
			want: []string{"app-debugging-arm64-v8a.apk"},
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
