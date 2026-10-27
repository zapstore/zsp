package source

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zapstore/zsp/v2/internal/config"
)

// TestPlanMetadata tests the metadata source selection logic
func TestPlanMetadata(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		wantLen   int
		wantFirst string
		wantNil   bool
	}{
		{
			name: "github source tries fastlane before github metadata",
			cfg: &config.Config{
				Repository: "https://github.com/AeonBTC/mempal",
			},
			wantLen:   2,
			wantFirst: "fastlane",
		},
		{
			name: "gitlab source tries fastlane before gitlab metadata",
			cfg: &config.Config{
				Repository: "https://gitlab.com/AuroraOSS/AuroraStore",
			},
			wantLen:   2,
			wantFirst: "fastlane",
		},
		{
			name: "codeberg source tries fastlane only",
			cfg: &config.Config{
				Repository: "https://codeberg.org/Freeyourgadget/Gadgetbridge",
			},
			wantLen:   1,
			wantFirst: "fastlane",
		},
		{
			name: "self-hosted gitea with explicit type tries fastlane",
			cfg: &config.Config{
				Repository: "https://git.example.com/owner/app",
				ReleaseSource: &config.ReleaseSource{
					URL:  "https://git.example.com/owner/app",
					Type: "gitea",
				},
			},
			wantLen:   1,
			wantFirst: "fastlane",
		},
		{
			name: "fdroid source has no automatic metadata source",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{
					URL: "https://f-droid.org/packages/de.danoeh.antennapod",
				},
			},
			wantNil: true,
		},
		{
			name: "local source with github repo tries fastlane first",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{LocalPath: "./app.apk"},
				Repository:    "https://github.com/AeonBTC/mempal",
			},
			wantLen:   2,
			wantFirst: "fastlane",
		},
		{
			name: "explicit metadata_sources replace automatic sources",
			cfg: &config.Config{
				Repository:      "https://github.com/AeonBTC/mempal",
				MetadataSources: []string{"playstore"},
			},
			wantLen: 1,
		},
		{
			name: "explicit metadata_sources without duplicates",
			cfg: &config.Config{
				Repository:      "https://github.com/AeonBTC/mempal",
				MetadataSources: []string{"github", "playstore"},
			},
			wantLen: 2,
		},
		{
			name: "unknown source type with no repo",
			cfg: &config.Config{
				ReleaseSource: &config.ReleaseSource{LocalPath: "./app.apk"},
			},
			wantNil: true,
		},
		{
			name: "web source with github repo tries fastlane first",
			cfg: &config.Config{
				Repository: "https://github.com/AntennaPod/AntennaPod",
				ReleaseSource: &config.ReleaseSource{
					URL:         "https://example.com/releases",
					IsWebSource: true,
					AssetURL:    "https://example.com/app.apk",
				},
			},
			wantLen:   2,
			wantFirst: "fastlane",
		},
		{
			name: "multiple explicit sources",
			cfg: &config.Config{
				Repository:      "https://github.com/AntennaPod/AntennaPod",
				ReleaseSource:   &config.ReleaseSource{URL: "https://f-droid.org/packages/de.danoeh.antennapod"},
				MetadataSources: []string{"playstore"},
			},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanMetadata(tt.cfg)
			sources := plan.Sources

			if tt.wantNil {
				if sources != nil || plan.Automatic {
					t.Errorf("PlanMetadata() = %+v, want an empty plan", plan)
				}
				return
			}

			if len(sources) != tt.wantLen {
				t.Errorf("PlanMetadata() len = %d, want %d, got %v", len(sources), tt.wantLen, sources)
			}

			if tt.wantFirst != "" && len(sources) > 0 && sources[0] != tt.wantFirst {
				t.Errorf("PlanMetadata()[0] = %q, want %q", sources[0], tt.wantFirst)
			}
			if want := tt.cfg.MetadataSources == nil; plan.Automatic != want {
				t.Errorf("PlanMetadata().Automatic = %v, want %v", plan.Automatic, want)
			}
		})
	}
}

func TestPlanMetadataPreservesExplicitOrder(t *testing.T) {
	configured := []string{"github", " PlayStore ", "fdroid", "github"}
	got := PlanMetadata(&config.Config{Repository: "https://github.com/owner/app", MetadataSources: configured})
	want := []string{"github", "playstore", "fdroid"}
	if !slices.Equal(got.Sources, want) || got.Automatic {
		t.Fatalf("PlanMetadata() = %+v, want explicit %v", got, want)
	}
}

func TestPlanMetadataEmptyListDisablesMetadata(t *testing.T) {
	got := PlanMetadata(&config.Config{Repository: "https://github.com/owner/app", MetadataSources: []string{}})
	if len(got.Sources) != 0 || got.Automatic {
		t.Fatalf("PlanMetadata() = %+v, want no sources", got)
	}
}

// TestMetadataFetcherCreation tests MetadataFetcher creation
func TestMetadataFetcherCreation(t *testing.T) {
	cfg := &config.Config{
		Repository: "https://github.com/AeonBTC/mempal",
	}

	fetcher := NewMetadataFetcher(cfg)
	if fetcher == nil {
		t.Error("NewMetadataFetcher() returned nil")
	}

	fetcherWithPkg := NewMetadataFetcherWithPackageID(cfg, "com.aeonbtc.mempal")
	if fetcherWithPkg == nil {
		t.Error("NewMetadataFetcherWithPackageID() returned nil")
	}
	if fetcherWithPkg.PackageID != "com.aeonbtc.mempal" {
		t.Errorf("PackageID = %q, want %q", fetcherWithPkg.PackageID, "com.aeonbtc.mempal")
	}
}

func TestPlanMetadataUsesLocalFastlane(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, fastlaneMetadataPath, "en-US"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := PlanMetadata(&config.Config{BaseDir: root, ReleaseSource: &config.ReleaseSource{LocalPath: "app.apk"}})
	if len(got.Sources) != 1 || got.Sources[0] != "fastlane" || !got.Automatic {
		t.Fatalf("plan = %+v, want automatic local fastlane", got)
	}
	if got := PlanMetadata(&config.Config{ReleaseSource: &config.ReleaseSource{LocalPath: "app.apk"}}); got.Sources != nil {
		t.Fatalf("plan = %+v, want none without a local tree or repository", got)
	}
}

func TestFetchLocalFastlaneMetadata(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, fastlaneMetadataPath, "en-US")
	if err := os.MkdirAll(filepath.Join(base, "images", "phoneScreenshots"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(base, "title.txt"):                            "Example\n",
		filepath.Join(base, "short_description.txt"):                "Short description\n",
		filepath.Join(base, "full_description.txt"):                 "Long description\n",
		filepath.Join(base, "images", "icon.png"):                   "icon",
		filepath.Join(base, "images", "phoneScreenshots", "01.png"): "screenshot",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	metadata, err := NewMetadataFetcher(&config.Config{BaseDir: root}).fetchFastlaneMetadata(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Name != "Example" || metadata.Summary != "Short description" || metadata.Description != "Long description" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.LocalIcon != filepath.Join(base, "images", "icon.png") {
		t.Errorf("icon = %q", metadata.LocalIcon)
	}
	if len(metadata.LocalImages) != 1 || metadata.LocalImages[0] != filepath.Join(base, "images", "phoneScreenshots", "01.png") {
		t.Errorf("screenshots = %v", metadata.LocalImages)
	}
	if metadata.IconURL != "" || len(metadata.ImageURLs) != 0 {
		t.Errorf("local files leaked into remote fields: icon %q images %v", metadata.IconURL, metadata.ImageURLs)
	}
}

func TestFetchLocalFastlaneMetadataSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	base := filepath.Join(root, fastlaneMetadataPath, "en-US")
	shots := filepath.Join(base, "images", "phoneScreenshots")
	if err := os.MkdirAll(shots, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(base, "full_description.txt"): "Long description\n",
		filepath.Join(shots, "01.png"):              "screenshot",
		filepath.Join(outside, "secret.txt"):        "secret",
		filepath.Join(outside, "icon.png"):          "outside icon",
		filepath.Join(outside, "shot.png"):          "outside shot",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		filepath.Join(base, "title.txt"):          filepath.Join(outside, "secret.txt"),
		filepath.Join(base, "images", "icon.png"): filepath.Join(outside, "icon.png"),
		filepath.Join(shots, "02.png"):            filepath.Join(outside, "shot.png"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}

	metadata, err := NewMetadataFetcher(&config.Config{BaseDir: root}).fetchFastlaneMetadata(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Name != "" || metadata.Description != "Long description" {
		t.Errorf("name %q description %q, want the symlinked title skipped", metadata.Name, metadata.Description)
	}
	if metadata.LocalIcon != "" {
		t.Errorf("icon = %q, want the symlinked icon skipped", metadata.LocalIcon)
	}
	if len(metadata.LocalImages) != 1 || filepath.Base(metadata.LocalImages[0]) != "01.png" {
		t.Errorf("screenshots = %v, want only the regular file", metadata.LocalImages)
	}
}

func TestFetchLocalFastlaneMetadataRejectsSymlinkedTree(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "en-US"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "fastlane", "metadata"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, fastlaneMetadataPath)); err != nil {
		t.Fatal(err)
	}
	if hasLocalFastlane(root) {
		t.Error("hasLocalFastlane() = true for a symlinked tree")
	}
	_, err := NewMetadataFetcher(&config.Config{BaseDir: root}).fetchFastlaneMetadata(t.Context())
	if err == nil || errors.Is(err, errFastlaneUnavailable) {
		t.Fatalf("fetchFastlaneMetadata() error = %v, want a failure other than unavailable", err)
	}
}

func TestFetchStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	fetcher := NewMetadataFetcher(&config.Config{Repository: "https://github.com/example/app"})
	result := fetcher.Fetch(ctx, MetadataPlan{Sources: []string{"github", "playstore"}})
	if len(result.Errors) != 1 || !errors.Is(result.Errors[0].Err, context.Canceled) {
		t.Fatalf("metadata errors = %v, want one cancellation", result.Errors)
	}
}

// TestExtractFirstParagraph tests the markdown paragraph extraction
func TestExtractFirstParagraph(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
		want     string
	}{
		{
			name:     "simple paragraph",
			markdown: "This is the first paragraph.",
			want:     "This is the first paragraph.",
		},
		{
			name: "skip header",
			markdown: `# Header
This is the content.`,
			want: "This is the content.",
		},
		{
			name: "skip badges",
			markdown: `![badge](url)
[![another](url)](link)
Real content here.`,
			want: "Real content here.",
		},
		{
			name: "skip html",
			markdown: `<div>ignored</div>
Content after html.`,
			want: "Content after html.",
		},
		{
			name: "multiline paragraph",
			markdown: `# Title
First line of paragraph.
Second line of paragraph.

Next paragraph ignored.`,
			want: "First line of paragraph. Second line of paragraph.",
		},
		{
			name:     "empty string",
			markdown: "",
			want:     "",
		},
		{
			name: "only headers and badges",
			markdown: `# Title
![badge](url)`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFirstParagraph(tt.markdown)
			if got != tt.want {
				t.Errorf("extractFirstParagraph() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGetPlayStorePackageID tests Play Store URL parsing
func TestGetPlayStorePackageID(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{
			"https://play.google.com/store/apps/details?id=com.example.app",
			"com.example.app",
		},
		{
			"https://play.google.com/store/apps/details?id=de.danoeh.antennapod&hl=en",
			"de.danoeh.antennapod",
		},
		{
			"https://play.google.com/store/apps/details?hl=en&id=com.example.app",
			"com.example.app",
		},
		{
			"https://github.com/user/repo",
			"",
		},
		{
			"not a url",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := GetPlayStorePackageID(tt.url)
			if got != tt.want {
				t.Errorf("GetPlayStorePackageID(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestFetchGiteaMetadata(t *testing.T) {
	fetcher := NewMetadataFetcher(&config.Config{Repository: "https://codeberg.org/owner/app"})
	fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://codeberg.org/api/v1/repos/owner/app" {
			t.Fatalf("request URL = %q", req.URL)
		}
		return testResponse(http.StatusOK, `{
			"name":"Example",
			"description":"A Gitea application",
			"website":"https://example.com",
			"topics":["android","nostr"]
		}`), nil
	})}

	meta, err := fetcher.fetchGiteaMetadata(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "Example" || meta.Description != "A Gitea application" ||
		meta.Website != "https://example.com" || !slices.Equal(meta.Tags, []string{"android", "nostr"}) {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestFetchFastlaneMetadata(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		transport  roundTripperFunc
		wantName   string
		wantLocale string
		wantIcon   string
		wantImages int
	}{
		{
			name:       "GitHub imports preferred en-US locale",
			repository: "https://github.com/owner/app",
			transport: func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host + req.URL.Path {
				case "api.github.com/repos/owner/app/contents/fastlane/metadata/android":
					return testResponse(http.StatusOK, `[{"name":"de-DE","type":"dir"},{"name":"en-US","type":"dir"}]`), nil
				case "api.github.com/repos/owner/app/contents/fastlane/metadata/android/en-US":
					return testResponse(http.StatusOK, `[
						{"name":"title.txt","type":"file","download_url":"https://raw.test/title"},
						{"name":"short_description.txt","type":"file","download_url":"https://raw.test/short"},
						{"name":"full_description.txt","type":"file","download_url":"https://raw.test/full"}
					]`), nil
				case "api.github.com/repos/owner/app/contents/fastlane/metadata/android/en-US/images":
					return testResponse(http.StatusOK, `[{"name":"icon.png","type":"file","download_url":"https://raw.test/icon"}]`), nil
				case "api.github.com/repos/owner/app/contents/fastlane/metadata/android/en-US/images/phoneScreenshots":
					return testResponse(http.StatusOK, `[
						{"name":"02.png","type":"file","download_url":"https://raw.test/02"},
						{"name":"01.png","type":"file","download_url":"https://raw.test/01"}
					]`), nil
				case "raw.test/title":
					return testResponse(http.StatusOK, " Flow \n"), nil
				case "raw.test/short":
					return testResponse(http.StatusOK, "A short description\n"), nil
				case "raw.test/full":
					return testResponse(http.StatusOK, "A full description\n"), nil
				default:
					return testResponse(http.StatusNotFound, ""), nil
				}
			},
			wantName:   "Flow",
			wantLocale: "A short description",
			wantIcon:   "https://raw.test/icon",
			wantImages: 2,
		},
		{
			name:       "GitLab falls back to deterministic locale",
			repository: "https://gitlab.com/group/app",
			transport: func(req *http.Request) (*http.Response, error) {
				path := req.URL.Path
				switch {
				case strings.HasSuffix(path, "/repository/tree") && req.URL.Query().Get("path") == fastlaneMetadataPath:
					return testResponse(http.StatusOK, `[{"name":"fr-FR","path":"fastlane/metadata/android/fr-FR","type":"tree"}]`), nil
				case strings.HasSuffix(path, "/repository/tree") && req.URL.Query().Get("path") == fastlaneMetadataPath+"/fr-FR":
					return testResponse(http.StatusOK, `[
						{"name":"title.txt","path":"fastlane/metadata/android/fr-FR/title.txt","type":"blob"},
						{"name":"short_description.txt","path":"fastlane/metadata/android/fr-FR/short_description.txt","type":"blob"}
					]`), nil
				case strings.HasSuffix(path, "/repository/tree") && req.URL.Query().Get("path") == fastlaneMetadataPath+"/fr-FR/images":
					return testResponse(http.StatusNotFound, ""), nil
				case strings.HasSuffix(path, "/repository/files/fastlane/metadata/android/fr-FR/title.txt/raw"):
					return testResponse(http.StatusOK, "Flux"), nil
				case strings.HasSuffix(path, "/repository/files/fastlane/metadata/android/fr-FR/short_description.txt/raw"):
					return testResponse(http.StatusOK, "Résumé"), nil
				default:
					return testResponse(http.StatusNotFound, ""), nil
				}
			},
			wantName:   "Flux",
			wantLocale: "Résumé",
			wantImages: 0,
		},
		{
			name:       "Codeberg/Gitea imports preferred en-US locale",
			repository: "https://codeberg.org/owner/app",
			transport: func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host + req.URL.Path {
				case "codeberg.org/api/v1/repos/owner/app/contents/fastlane/metadata/android":
					return testResponse(http.StatusOK, `[{"name":"de-DE","type":"dir"},{"name":"en-US","type":"dir"}]`), nil
				case "codeberg.org/api/v1/repos/owner/app/contents/fastlane/metadata/android/en-US":
					return testResponse(http.StatusOK, `[
						{"name":"title.txt","type":"file","download_url":"https://raw.test/title"},
						{"name":"short_description.txt","type":"file","download_url":"https://raw.test/short"},
						{"name":"full_description.txt","type":"file","download_url":"https://raw.test/full"}
					]`), nil
				case "codeberg.org/api/v1/repos/owner/app/contents/fastlane/metadata/android/en-US/images":
					return testResponse(http.StatusOK, `[{"name":"icon.png","type":"file","download_url":"https://raw.test/icon"}]`), nil
				case "codeberg.org/api/v1/repos/owner/app/contents/fastlane/metadata/android/en-US/images/phoneScreenshots":
					return testResponse(http.StatusOK, `[
						{"name":"02.png","type":"file","download_url":"https://raw.test/02"},
						{"name":"01.png","type":"file","download_url":"https://raw.test/01"}
					]`), nil
				case "raw.test/title":
					return testResponse(http.StatusOK, " Gadget \n"), nil
				case "raw.test/short":
					return testResponse(http.StatusOK, "A short description\n"), nil
				case "raw.test/full":
					return testResponse(http.StatusOK, "A full description\n"), nil
				default:
					return testResponse(http.StatusNotFound, ""), nil
				}
			},
			wantName:   "Gadget",
			wantLocale: "A short description",
			wantIcon:   "https://raw.test/icon",
			wantImages: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Repository: tt.repository}
			fetcher := NewMetadataFetcher(cfg)
			fetcher.client = &http.Client{Transport: tt.transport}

			meta, err := fetcher.fetchFastlaneMetadata(context.Background())
			if err != nil {
				t.Fatalf("fetchFastlaneMetadata() error = %v", err)
			}
			if meta.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", meta.Name, tt.wantName)
			}
			if meta.Summary != tt.wantLocale {
				t.Errorf("Summary = %q, want %q", meta.Summary, tt.wantLocale)
			}
			if meta.IconURL != tt.wantIcon {
				t.Errorf("IconURL = %q, want %q", meta.IconURL, tt.wantIcon)
			}
			if len(meta.ImageURLs) != tt.wantImages {
				t.Errorf("ImageURLs = %v, want %d images", meta.ImageURLs, tt.wantImages)
			}
			if len(meta.ImageURLs) == 2 && meta.ImageURLs[0] != "https://raw.test/01" {
				t.Errorf("ImageURLs not sorted: %v", meta.ImageURLs)
			}
		})
	}
}

var automaticGitHubPlan = MetadataPlan{Sources: []string{"fastlane", "github"}, Automatic: true}

func TestFetchAutomaticMetadataFallback(t *testing.T) {
	t.Run("uses GitHub metadata when Fastlane is absent", func(t *testing.T) {
		fetcher := NewMetadataFetcher(&config.Config{Repository: "https://github.com/owner/app"})
		fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/repos/owner/app/contents/fastlane/metadata/android":
				return testResponse(http.StatusNotFound, ""), nil
			case "/repos/owner/app":
				return testResponse(http.StatusOK, `{
					"name":"Repository name",
					"description":"A repository description that is deliberately long enough to avoid a README request.",
					"homepage":"https://example.com",
					"topics":["example"]
				}`), nil
			default:
				return testResponse(http.StatusNotFound, ""), nil
			}
		})}

		result := fetcher.Fetch(context.Background(), automaticGitHubPlan)
		if result.HasErrors() {
			t.Fatalf("unexpected metadata errors: %v", result.Errors[0])
		}
		if len(result.Metadata) != 1 || result.Metadata[0].Source != "github" || result.Metadata[0].Name != "Repository name" {
			t.Errorf("metadata = %+v, want native GitHub metadata only", result.Metadata)
		}
	})

	t.Run("does not call GitHub when Fastlane has a description", func(t *testing.T) {
		fetcher := NewMetadataFetcher(&config.Config{Repository: "https://github.com/owner/app"})
		fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/repos/owner/app/contents/fastlane/metadata/android":
				return testResponse(http.StatusOK, `[{"name":"en-US","type":"dir"}]`), nil
			case "/repos/owner/app/contents/fastlane/metadata/android/en-US":
				return testResponse(http.StatusOK, `[
					{"name":"title.txt","type":"file","download_url":"https://raw.test/title"},
					{"name":"full_description.txt","type":"file","download_url":"https://raw.test/full"}
				]`), nil
			case "/repos/owner/app/contents/fastlane/metadata/android/en-US/images":
				return testResponse(http.StatusNotFound, ""), nil
			case "/title":
				return testResponse(http.StatusOK, "Fastlane title"), nil
			case "/full":
				return testResponse(http.StatusOK, "Fastlane full description"), nil
			case "/repos/owner/app":
				t.Fatal("GitHub fallback must not be called when Fastlane has a description")
			}
			return testResponse(http.StatusNotFound, ""), nil
		})}

		result := fetcher.Fetch(context.Background(), automaticGitHubPlan)
		if result.HasErrors() || len(result.Metadata) != 1 {
			t.Fatalf("metadata = %+v errors = %v, want Fastlane only", result.Metadata, result.Errors)
		}
		if got := result.Metadata[0]; got.Source != "fastlane" || got.Name != "Fastlane title" || got.Description != "Fastlane full description" {
			t.Errorf("metadata = %+v, want Fastlane metadata", got)
		}
	})

	t.Run("uses GitHub description when Fastlane has no full description", func(t *testing.T) {
		fetcher := NewMetadataFetcher(&config.Config{Repository: "https://github.com/owner/app"})
		fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/repos/owner/app/contents/fastlane/metadata/android":
				return testResponse(http.StatusOK, `[{"name":"en-US","type":"dir"}]`), nil
			case "/repos/owner/app/contents/fastlane/metadata/android/en-US":
				return testResponse(http.StatusOK, `[{"name":"title.txt","type":"file","download_url":"https://raw.test/title"}]`), nil
			case "/repos/owner/app/contents/fastlane/metadata/android/en-US/images":
				return testResponse(http.StatusNotFound, ""), nil
			case "/title":
				return testResponse(http.StatusOK, "Fastlane title"), nil
			case "/repos/owner/app":
				return testResponse(http.StatusOK, `{
					"name":"Repository name",
					"description":"A repository description that is deliberately long enough to avoid a README request."
				}`), nil
			default:
				return testResponse(http.StatusNotFound, ""), nil
			}
		})}

		result := fetcher.Fetch(context.Background(), automaticGitHubPlan)
		if result.HasErrors() {
			t.Fatalf("unexpected metadata errors: %v", result.Errors[0])
		}
		if len(result.Metadata) != 2 || result.Metadata[0].Source != "fastlane" || result.Metadata[1].Source != "github" {
			t.Fatalf("metadata = %+v, want Fastlane then GitHub in order", result.Metadata)
		}
		if result.Metadata[0].Name != "Fastlane title" || result.Metadata[0].Description != "" ||
			result.Metadata[1].Description != "A repository description that is deliberately long enough to avoid a README request." {
			t.Errorf("metadata = %+v, want Fastlane name and GitHub description", result.Metadata)
		}
	})

	t.Run("uses GitHub description when Fastlane lookup fails", func(t *testing.T) {
		fetcher := NewMetadataFetcher(&config.Config{Repository: "https://github.com/owner/app"})
		fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/repos/owner/app/contents/fastlane/metadata/android":
				return testResponse(http.StatusForbidden, "rate limit"), nil
			case "/repos/owner/app":
				return testResponse(http.StatusOK, `{
					"name":"Repository name",
					"description":"A repository description that is deliberately long enough to avoid a README request."
				}`), nil
			default:
				return testResponse(http.StatusNotFound, ""), nil
			}
		})}

		result := fetcher.Fetch(context.Background(), automaticGitHubPlan)
		if len(result.Errors) != 1 || result.Errors[0].Source != "fastlane" {
			t.Fatalf("errors = %v, want the Fastlane failure recorded", result.Errors)
		}
		if len(result.Metadata) != 1 || result.Metadata[0].Source != "github" {
			t.Errorf("metadata = %+v, want the GitHub fallback", result.Metadata)
		}
	})

	t.Run("cancellation skips the fallback", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		fetcher := NewMetadataFetcher(&config.Config{Repository: "https://github.com/owner/app"})
		fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			cancel()
			return nil, req.Context().Err()
		})}

		result := fetcher.Fetch(ctx, automaticGitHubPlan)
		if len(result.Metadata) != 0 || len(result.Errors) != 1 || !errors.Is(result.Errors[0], context.Canceled) {
			t.Fatalf("metadata = %+v errors = %v, want one cancellation and no fallback", result.Metadata, result.Errors)
		}
	})
}

func TestFetchExplicitSourcesKeepOrderAndSkipFailures(t *testing.T) {
	fetcher := NewMetadataFetcher(&config.Config{Repository: "https://codeberg.org/owner/app"})
	fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, `{"name":"Example","description":"d"}`), nil
	})}
	result := fetcher.Fetch(t.Context(), MetadataPlan{Sources: []string{"github", "gitea", "bogus"}})
	if len(result.Metadata) != 1 || result.Metadata[0].Source != "gitea" {
		t.Fatalf("metadata = %+v, want only the gitea answer", result.Metadata)
	}
	if len(result.Errors) != 2 || result.Errors[0].Source != "github" || result.Errors[1].Source != "bogus" {
		t.Fatalf("errors = %v, want github and bogus recorded in order", result.Errors)
	}
}

func TestMetadataErrorMessage(t *testing.T) {
	err := &MetadataError{Source: "github", Err: errors.New("rate limited")}
	if got, want := err.Error(), "fetch github metadata: rate limited"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, err.Err) {
		t.Error("MetadataError does not unwrap to its cause")
	}
}

func TestFetchFDroidMetadataDescription(t *testing.T) {
	page := `<html><body>
<img class="package-icon" src="/repo/com.example.app/icon.png">
<div class="package-summary">Short summary</div>
<div class="package-description" dir="auto">Full description line.<br><br>Second paragraph.</div>
<div class="screenshots"><img src="/repo/com.example.app/1.png"></div>
</body></html>`
	yamlWithoutText := "Categories:\n  - Strategy Game\nLicense: MPL-2.0\nWebSite: https://example.com\nAutoName: Example\n"
	yamlWithText := yamlWithoutText + "Summary: YAML summary\nDescription: YAML description\n"

	tests := []struct {
		name            string
		yaml            string
		wantSummary     string
		wantDescription string
	}{
		{
			name:            "page text when YAML omits description",
			yaml:            yamlWithoutText,
			wantSummary:     "Short summary",
			wantDescription: "Full description line.\n\nSecond paragraph.",
		},
		{
			name:            "YAML text wins over the page",
			yaml:            yamlWithText,
			wantSummary:     "YAML summary",
			wantDescription: "YAML description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := NewMetadataFetcherWithPackageID(&config.Config{}, "com.example.app")
			fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host + req.URL.Path {
				case "f-droid.org/en/packages/com.example.app/":
					return testResponse(http.StatusOK, page), nil
				case "gitlab.com/fdroid/fdroiddata/-/raw/master/metadata/com.example.app.yml":
					return testResponse(http.StatusOK, tt.yaml), nil
				default:
					t.Errorf("unexpected request %s", req.URL)
					return testResponse(http.StatusNotFound, ""), nil
				}
			})}

			meta, err := fetcher.fetchFDroidMetadata(context.Background())
			if err != nil {
				t.Fatalf("fetchFDroidMetadata() error = %v", err)
			}
			if meta.Summary != tt.wantSummary {
				t.Errorf("Summary = %q, want %q", meta.Summary, tt.wantSummary)
			}
			if meta.Description != tt.wantDescription {
				t.Errorf("Description = %q, want %q", meta.Description, tt.wantDescription)
			}
			if meta.License != "MPL-2.0" || meta.Name != "Example" || meta.Website != "https://example.com" {
				t.Errorf("YAML fields = name %q license %q website %q", meta.Name, meta.License, meta.Website)
			}
			if len(meta.Tags) != 1 || meta.Tags[0] != "strategy game" {
				t.Errorf("Tags = %v, want [strategy game]", meta.Tags)
			}
			if meta.IconURL != "https://f-droid.org/repo/com.example.app/icon.png" {
				t.Errorf("IconURL = %q", meta.IconURL)
			}
			if len(meta.ImageURLs) != 1 || meta.ImageURLs[0] != "https://f-droid.org/repo/com.example.app/1.png" {
				t.Errorf("ImageURLs = %v", meta.ImageURLs)
			}
		})
	}
}

func TestFetchFastlaneMetadataErrors(t *testing.T) {
	tests := []struct {
		name      string
		ctx       context.Context
		transport roundTripperFunc
	}{
		{
			name: "malformed directory response",
			ctx:  context.Background(),
			transport: func(req *http.Request) (*http.Response, error) {
				return testResponse(http.StatusOK, "{invalid"), nil
			},
		},
		{
			name: "cancelled context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			transport: func(req *http.Request) (*http.Response, error) {
				return nil, req.Context().Err()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := NewMetadataFetcher(&config.Config{Repository: "https://github.com/owner/app"})
			fetcher.client = &http.Client{Transport: tt.transport}

			if _, err := fetcher.fetchFastlaneMetadata(tt.ctx); err == nil {
				t.Fatal("fetchFastlaneMetadata() error = nil, want error")
			}
		})
	}
}

func TestFetchGitLabMetadataLicenseAndWebsite(t *testing.T) {
	tests := []struct {
		name    string
		license string
		want    string
	}{
		{"mit", `{"key":"mit"}`, "MIT"},
		{"apache", `{"key":"apache-2.0"}`, "Apache-2.0"},
		{"gpl-3.0", `{"key":"gpl-3.0"}`, "GPL-3.0"},
		{"name", `{"key":"other","name":"MIT License"}`, "MIT"},
		{"upper-case key", `{"key":"MIT"}`, "MIT"},
		{"unmapped key", `{"key":"other"}`, ""},
		{"no license", `null`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := NewMetadataFetcher(&config.Config{Repository: "https://gitlab.com/owner/app"})
			fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return testResponse(http.StatusOK, `{
					"name":"Example","description":"`+strings.Repeat("d", 120)+`",
					"web_url":"https://gitlab.com/owner/app","license":`+tt.license+`}`), nil
			})}
			meta, err := fetcher.fetchGitLabMetadata(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if meta.License != tt.want {
				t.Errorf("License = %q, want %q", meta.License, tt.want)
			}
			if meta.Website != "" {
				t.Errorf("Website = %q, want empty", meta.Website)
			}
		})
	}
}

func TestFetchGitLabMetadataNotFoundNamesProject(t *testing.T) {
	fetcher := NewMetadataFetcher(&config.Config{Repository: "https://gitlab.com/group/sub/app"})
	fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return testResponse(http.StatusNotFound, ""), nil
	})}
	_, err := fetcher.fetchGitLabMetadata(t.Context())
	if err == nil || !strings.Contains(err.Error(), "group/sub/app") {
		t.Fatalf("fetchGitLabMetadata() error = %v, want it to name group/sub/app", err)
	}
}

func TestFetchGitLabReadmeClosesEveryAttempt(t *testing.T) {
	var bodies []*closeTracker
	fetcher := NewMetadataFetcher(&config.Config{Repository: "https://gitlab.com/owner/app"})
	fetcher.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body := &closeTracker{Reader: strings.NewReader("not found")}
		bodies = append(bodies, body)
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: body, Request: req}, nil
	})}
	if _, err := fetcher.fetchGitLabReadme(t.Context(), "https://gitlab.com", "owner%2Fapp", "main"); err == nil {
		t.Fatal("fetchGitLabReadme() error = nil, want not found")
	}
	if len(bodies) < 2 {
		t.Fatalf("attempted %d README files, want several", len(bodies))
	}
	for i, body := range bodies {
		if !body.closed {
			t.Errorf("response body %d left open", i)
		}
	}
}
