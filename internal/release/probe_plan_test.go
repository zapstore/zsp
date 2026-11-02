package release

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zapstore/zsp/v2/internal/apk"
	"github.com/zapstore/zsp/v2/internal/source"
)

// fakeAsset is one APK a probeForge serves.
type fakeAsset struct {
	name string
	file string // under testdata/apks
	// noRanges answers every request with the whole file, as a server that
	// ignores Range does.
	noRanges bool
	// garbageDownload answers a plain download with bytes that are not an APK,
	// while ranged reads see the real file.
	garbageDownload bool
}

// probeForge is a fake Gitea forge whose latest release carries the given
// assets. It counts, per asset, the downloads and the ranged reads.
type probeForge struct {
	mu     sync.Mutex
	full   map[string]int
	ranged map[string]int
}

func (f *probeForge) downloads(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.full[name]
}

func (f *probeForge) rangedReads(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ranged[name]
}

func newProbeForge(t *testing.T, assets ...fakeAsset) (FetchConfig, *probeForge) {
	t.Helper()
	forge := &probeForge{full: map[string]int{}, ranged: map[string]int{}}
	paths := map[string]string{}
	sizes := map[string]int64{}
	for _, a := range assets {
		path := filepath.Join("..", "..", "testdata", "apks", a.file)
		info, err := os.Stat(path)
		if err != nil {
			t.Skipf("no test APK: %v", err)
		}
		paths[a.name], sizes[a.name] = path, info.Size()
	}

	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/owner/repo/releases", func(w http.ResponseWriter, r *http.Request) {
		var list []string
		for _, a := range assets {
			list = append(list, fmt.Sprintf(`{"name":%q,"size":%d,"browser_download_url":"%s/dl/%s"}`,
				a.name, sizes[a.name], server.URL, a.name))
		}
		_, _ = fmt.Fprintf(w, `[{"tag_name":"v1.0.0","name":"One","body":"notes","published_at":"2024-05-01T00:00:00Z","assets":[%s]}]`,
			strings.Join(list, ","))
	})
	for _, a := range assets {
		mux.HandleFunc("/dl/"+a.name, func(w http.ResponseWriter, r *http.Request) {
			isRange := r.Header.Get("Range") != ""
			forge.mu.Lock()
			if isRange {
				forge.ranged[a.name]++
			} else {
				forge.full[a.name]++
			}
			forge.mu.Unlock()

			if !isRange && a.garbageDownload {
				_, _ = w.Write([]byte(strings.Repeat("not a zip ", 1000)))
				return
			}
			f, err := os.Open(paths[a.name])
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer f.Close()
			if a.noRanges {
				w.Header().Set("Content-Length", fmt.Sprint(sizes[a.name]))
				_, _ = w.Write(mustRead(t, f))
				return
			}
			http.ServeContent(w, r, a.name, time.Time{}, f)
		})
	}
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return FetchConfig{ReleaseSource: &ReleaseSource{URL: server.URL + "/owner/repo", Type: "gitea"}}, forge
}

func mustRead(t *testing.T, f *os.File) []byte {
	t.Helper()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, info.Size())
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	return buf
}

func fetchAPKs(t *testing.T, config FetchConfig) []*APK {
	t.Helper()
	apks, err := Fetch(t.Context(), config, FetchOptions{SkipHTTPCache: true})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	t.Cleanup(func() { closeAPKs(apks) })
	return apks
}

func versionCodes(apks []*APK) []int64 {
	var codes []int64
	for _, a := range apks {
		codes = append(codes, a.VersionCode)
	}
	slices.Sort(codes)
	return codes
}

// The two Amber APKs share an app and a certificate. The arm64-only one has
// the greater version_code, so the universal one is never chosen and need not
// be downloaded.
func TestFetchSkipsTheDownloadsProbingRulesOut(t *testing.T) {
	assets := []fakeAsset{
		{name: "x.apk", file: "amber-arm64-v8a-v6.3.0.apk"},
		{name: "y.apk", file: "amber-free-universal-v3.4.1.apk"},
	}

	config, forge := newProbeForge(t, assets...)
	got := fetchAPKs(t, config)
	if codes := versionCodes(got); !slices.Equal(codes, []int64{197}) {
		t.Fatalf("version codes = %v, want [197]", codes)
	}
	if forge.downloads("x.apk") != 1 || forge.downloads("y.apk") != 0 {
		t.Errorf("downloads x=%d y=%d, want 1 and 0", forge.downloads("x.apk"), forge.downloads("y.apk"))
	}
	if forge.rangedReads("x.apk") == 0 || forge.rangedReads("y.apk") == 0 {
		t.Errorf("ranged reads x=%d y=%d, want both probed", forge.rangedReads("x.apk"), forge.rangedReads("y.apk"))
	}

	// Without probing the same APK is chosen, after both are downloaded.
	t.Run("same result without probing", func(t *testing.T) {
		old := probeMinSize
		probeMinSize = 1 << 60
		t.Cleanup(func() { probeMinSize = old })
		config, forge := newProbeForge(t, assets...)
		want := fetchAPKs(t, config)
		if codes := versionCodes(want); !slices.Equal(codes, versionCodes(got)) || want[0].Hash != got[0].Hash {
			t.Errorf("without probing got %v, with probing %v", codes, versionCodes(got))
		}
		if forge.downloads("x.apk") != 1 || forge.downloads("y.apk") != 1 || forge.rangedReads("x.apk") != 0 {
			t.Errorf("baseline downloads x=%d y=%d ranged x=%d, want 1, 1, 0",
				forge.downloads("x.apk"), forge.downloads("y.apk"), forge.rangedReads("x.apk"))
		}
	})
}

// Different apps are never ranked against each other, so each is downloaded.
func TestFetchDownloadsEveryAppItProbes(t *testing.T) {
	config, forge := newProbeForge(t,
		fakeAsset{name: "a.apk", file: "exiferaser.apk"},
		fakeAsset{name: "b.apk", file: "sample.apk"},
	)
	got := fetchAPKs(t, config)
	if len(got) != 2 {
		t.Fatalf("got %d APKs, want 2", len(got))
	}
	for _, name := range []string{"a.apk", "b.apk"} {
		if forge.downloads(name) != 1 || forge.rangedReads(name) == 0 {
			t.Errorf("%s: downloads=%d ranged=%d, want 1 download after probing", name, forge.downloads(name), forge.rangedReads(name))
		}
	}
}

// A candidate whose identity cannot be read is downloaded as it would be
// without probing, and then judged by the usual selection.
func TestFetchDownloadsWhatItCannotProbe(t *testing.T) {
	config, forge := newProbeForge(t,
		fakeAsset{name: "x.apk", file: "amber-arm64-v8a-v6.3.0.apk"},
		fakeAsset{name: "y.apk", file: "amber-free-universal-v3.4.1.apk", noRanges: true},
	)
	got := fetchAPKs(t, config)
	if codes := versionCodes(got); !slices.Equal(codes, []int64{197}) {
		t.Fatalf("version codes = %v, want [197]", codes)
	}
	if forge.downloads("x.apk") != 1 || forge.downloads("y.apk") != 1 {
		t.Errorf("downloads x=%d y=%d, want both downloaded", forge.downloads("x.apk"), forge.downloads("y.apk"))
	}
}

// The probe only predicts. When the best candidate fails verification the next
// one is downloaded.
func TestFetchTriesTheNextCandidateWhenTheBestFails(t *testing.T) {
	config, forge := newProbeForge(t,
		fakeAsset{name: "x.apk", file: "amber-arm64-v8a-v6.3.0.apk", garbageDownload: true},
		fakeAsset{name: "y.apk", file: "amber-free-universal-v3.4.1.apk"},
	)
	got := fetchAPKs(t, config)
	if codes := versionCodes(got); !slices.Equal(codes, []int64{141}) {
		t.Fatalf("version codes = %v, want [141]: the next best after the failed one", codes)
	}
	if forge.downloads("x.apk") != 1 || forge.downloads("y.apk") != 1 {
		t.Errorf("downloads x=%d y=%d, want 1 each", forge.downloads("x.apk"), forge.downloads("y.apk"))
	}
}

// With one candidate, or too few that are big enough, a probe cannot save a
// download, so none is made.
func TestFetchDoesNotProbeWhenItCannotSaveADownload(t *testing.T) {
	t.Run("one candidate", func(t *testing.T) {
		config, forge := newProbeForge(t, fakeAsset{name: "a.apk", file: "amber-arm64-v8a-v6.3.0.apk"})
		fetchAPKs(t, config)
		if forge.rangedReads("a.apk") != 0 || forge.downloads("a.apk") != 1 {
			t.Errorf("ranged=%d downloads=%d, want 0 and 1", forge.rangedReads("a.apk"), forge.downloads("a.apk"))
		}
	})
	t.Run("one candidate big enough", func(t *testing.T) {
		config, forge := newProbeForge(t,
			fakeAsset{name: "a.apk", file: "test-release.apk"},
			fakeAsset{name: "b.apk", file: "sample.apk"},
		)
		fetchAPKs(t, config)
		for _, name := range []string{"a.apk", "b.apk"} {
			if forge.rangedReads(name) != 0 || forge.downloads(name) != 1 {
				t.Errorf("%s: ranged=%d downloads=%d, want 0 and 1", name, forge.rangedReads(name), forge.downloads(name))
			}
		}
	})
}

// member builds a probed candidate named by its version and architectures.
func member(name string, version int64, archs ...string) probeMember {
	return probeMember{
		asset: &source.Asset{Name: name},
		id:    &apk.Identity{PackageID: "org.acme", VersionCode: version, Architectures: archs},
	}
}

func newPlan(members ...probeMember) *downloadPlan {
	plan := &downloadPlan{groupOf: map[*source.Asset]*probeGroup{}}
	group := &probeGroup{members: members, tried: map[*source.Asset]bool{}}
	plan.groups = []*probeGroup{group}
	for _, m := range members {
		plan.order = append(plan.order, m.asset)
		plan.groupOf[m.asset] = group
	}
	return plan
}

func names(assets []*source.Asset) []string {
	var out []string
	for _, a := range assets {
		out = append(out, a.Name)
	}
	return out
}

func TestDominance(t *testing.T) {
	const arm, v7, x86 = "arm64-v8a", "armeabi-v7a", "x86"
	tests := []struct {
		name    string
		members []probeMember
		want    []string
	}{
		{"greater version wins", []probeMember{member("old", 1, arm), member("new", 2, arm)}, []string{"new"}},
		{"arm64 outranks a greater version with no ABI named",
			[]probeMember{member("arm", 1, arm), member("java", 2)}, []string{"arm", "java"}},
		{"a lower version with no ABI named loses to arm64",
			[]probeMember{member("arm", 2, arm), member("java", 1)}, []string{"arm"}},
		{"an app with no ABI named is kept alone", []probeMember{member("java", 1)}, []string{"java"}},
		{"ABIs without arm64 lose to arm64 at any version",
			[]probeMember{member("arm", 1, arm), member("x86", 9, x86)}, []string{"arm"}},
		{"ABIs without arm64 are kept when nothing declares arm64",
			[]probeMember{member("x86", 2, x86), member("v7", 1, v7)}, []string{"x86", "v7"}},
		{"arm64-only beats fat at one version",
			[]probeMember{member("fat", 1, arm, v7, x86), member("only", 1, arm)}, []string{"only"}},
		{"several arm64-only stay", []probeMember{member("a", 1, arm), member("b", 1, arm)}, []string{"a", "b"}},
		{"fat stays beside fat", []probeMember{member("a", 1, arm, v7), member("b", 1, arm, x86)}, []string{"a", "b"}},
		{"fat of a greater version beats arm64-only",
			[]probeMember{member("fat", 2, arm, v7), member("only", 1, arm)}, []string{"fat"}},
		{"a rival with no arm64 never wins",
			[]probeMember{member("arm", 1, arm), member("x86", 2, x86)}, []string{"arm"}},
		{"an uncertain rival never ranks another below it",
			[]probeMember{member("java", 9), member("x86", 1, x86)}, []string{"java", "x86"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := names(newPlan(tt.members...).first())
			if !slices.Equal(got, tt.want) {
				t.Errorf("first = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlanRetriesDownTheRanking(t *testing.T) {
	plan := newPlan(member("c", 1, "arm64-v8a"), member("a", 3, "arm64-v8a"), member("b", 2, "arm64-v8a"))
	if got := names(plan.first()); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("first = %v, want [a]", got)
	}
	for _, want := range []string{"b", "c"} {
		if got := names(plan.retry()); !slices.Equal(got, []string{want}) {
			t.Fatalf("retry = %v, want [%s]", got, want)
		}
	}
	if got := plan.retry(); len(got) != 0 {
		t.Fatalf("retry with every candidate tried = %v, want none", names(got))
	}

	plan = newPlan(member("a", 2, "arm64-v8a"), member("b", 1, "arm64-v8a"))
	first := plan.first()
	plan.verified(first[0])
	if got := plan.retry(); len(got) != 0 {
		t.Errorf("retry after a verified candidate = %v, want none", names(got))
	}
}

func TestPlanKeepsUnprobedCandidatesInOrder(t *testing.T) {
	unprobed := &source.Asset{Name: "u"}
	plan := newPlan(member("a", 2, "arm64-v8a"), member("b", 1, "arm64-v8a"))
	plan.free = []*source.Asset{unprobed}
	plan.order = []*source.Asset{plan.order[0], unprobed, plan.order[1]}
	if got := names(plan.first()); !slices.Equal(got, []string{"a", "u"}) {
		t.Errorf("first = %v, want the unprobed candidate beside the best probed one", got)
	}
}

// TestPruningNeverChangesTheSelection checks the rules against selectFetchedAPKs
// itself: whatever the probe cannot see, choosing among the candidates that
// survive a round picks what choosing among all of them picks. A candidate with
// no ABI named may be an arm64 APK whose native code Parse finds by its ELF header.
func TestPruningNeverChangesTheSelection(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	archSets := [][]string{
		nil,
		{"arm64-v8a"},
		{"arm64-v8a", "armeabi-v7a"},
		{"arm64-v8a", "armeabi-v7a", "x86", "x86_64"},
		{"armeabi-v7a"},
		{"x86"},
		{"x86", "x86_64"},
	}
	for trial := 0; trial < 5000; trial++ {
		n := 2 + rng.Intn(5)
		var members []probeMember
		var all []*APK
		byAsset := map[*source.Asset]*APK{}
		for i := 0; i < n; i++ {
			archs := archSets[rng.Intn(len(archSets))]
			m := member(fmt.Sprintf("m%d", i), int64(1+rng.Intn(4)), archs...)
			members = append(members, m)

			trueArchs := archs
			if len(archs) == 0 && rng.Intn(2) == 0 {
				trueArchs = []string{"arm64-v8a"}
			}
			a := &APK{Filename: m.asset.Name, AppID: "org.acme", VersionCode: m.id.VersionCode, Architectures: trueArchs}
			all = append(all, a)
			byAsset[m.asset] = a
		}
		want := chosenNames(chooseAppAPKs(all))

		plan := newPlan(members...)
		var survivors []*APK
		for _, asset := range plan.first() {
			survivors = append(survivors, byAsset[asset])
		}
		got := chosenNames(chooseAppAPKs(survivors))
		if !slices.Equal(got, want) {
			t.Fatalf("trial %d: choosing among survivors gave %v, among all %v\nmembers: %s", trial, got, want, describe(members))
		}
	}
}

func chosenNames(apks []*APK) []string {
	var out []string
	for _, a := range apks {
		out = append(out, a.Filename)
	}
	slices.Sort(out)
	return out
}

func describe(members []probeMember) string {
	var parts []string
	for _, m := range members {
		parts = append(parts, fmt.Sprintf("%s v%d %v", m.asset.Name, m.id.VersionCode, m.id.Architectures))
	}
	return strings.Join(parts, "; ")
}
