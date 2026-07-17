package source

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zapstore/zsp/internal/config"
)

// TestDefaultMetadataSources tests the metadata source detection logic
func TestDefaultMetadataSources(t *testing.T) {
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
			sources := DefaultMetadataSources(tt.cfg)

			if tt.wantNil {
				if sources != nil {
					t.Errorf("DefaultMetadataSources() = %v, want nil", sources)
				}
				return
			}

			if sources == nil {
				t.Error("DefaultMetadataSources() returned nil, want non-nil")
				return
			}

			if len(sources) != tt.wantLen {
				t.Errorf("DefaultMetadataSources() len = %d, want %d, got %v", len(sources), tt.wantLen, sources)
			}

			if tt.wantFirst != "" && len(sources) > 0 && sources[0] != tt.wantFirst {
				t.Errorf("DefaultMetadataSources()[0] = %q, want %q", sources[0], tt.wantFirst)
			}
		})
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

func TestFetchAutomaticMetadataFallback(t *testing.T) {
	t.Run("uses GitHub metadata when Fastlane is absent", func(t *testing.T) {
		cfg := &config.Config{Repository: "https://github.com/owner/app"}
		fetcher := NewMetadataFetcher(cfg)
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

		if err := fetcher.FetchAutomaticMetadata(context.Background(), "github"); err != nil {
			t.Fatalf("FetchAutomaticMetadata() error = %v", err)
		}
		if cfg.Name != "Repository name" {
			t.Errorf("Name = %q, want native GitHub metadata", cfg.Name)
		}
	})

	t.Run("does not call GitHub when Fastlane exists", func(t *testing.T) {
		cfg := &config.Config{Repository: "https://github.com/owner/app"}
		fetcher := NewMetadataFetcher(cfg)
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
				t.Fatal("GitHub fallback must not be called when Fastlane exists")
			}
			return testResponse(http.StatusNotFound, ""), nil
		})}

		if err := fetcher.FetchAutomaticMetadata(context.Background(), "github"); err != nil {
			t.Fatalf("FetchAutomaticMetadata() error = %v", err)
		}
		if cfg.Name != "Fastlane title" {
			t.Errorf("Name = %q, want Fastlane metadata", cfg.Name)
		}
	})
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
