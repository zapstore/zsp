package config

import (
	"bytes"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMarshalYAMLRoundTrips(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{
			name:   "repository only",
			config: Config{Repository: "https://github.com/example/app"},
		},
		{
			name: "local release source",
			config: Config{
				ReleaseSource: &ReleaseSource{LocalPath: "builds/*.apk"},
				Name:          "Example",
			},
		},
		{
			name: "structured web source and full metadata",
			config: Config{
				Repository: "https://github.com/example/app",
				ReleaseSource: &ReleaseSource{
					URL:         "https://example.com/app",
					IsWebSource: true,
					AssetURL:    "https://example.com/app-{version}.apk",
					Version:     &VersionExtractor{URL: "https://example.com/latest", Path: "$.version"},
				},
				Channel:               "nightly",
				Name:                  "Example",
				Summary:               "A test app",
				Description:           "Longer description",
				Tags:                  []string{"social", "tools"},
				License:               "MIT",
				Website:               "https://example.com",
				Icon:                  "media/icon.png",
				Images:                []string{"media/one.png", "https://example.com/two.png"},
				ReleaseNotes:          "notes.md",
				SupportedNIPs:         []string{"1", "2"},
				MinAllowedVersion:     "1.0.0",
				MinAllowedVersionCode: 1,
				MetadataSources:       []string{},
			},
		},
		{
			name: "asset extractor source",
			config: Config{
				Repository: "https://github.com/example/app",
				ReleaseSource: &ReleaseSource{
					URL:         "https://example.com/app",
					IsWebSource: true,
					Version:     &VersionExtractor{URL: "https://example.com/latest", Path: "$.version"},
					Asset:       &VersionExtractor{URL: "https://example.com/latest", Selector: "a.download", Attribute: "href"},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := yaml.Marshal(test.config)
			if err != nil {
				t.Fatalf("Marshal() = %v", err)
			}
			got, err := Parse(bytes.NewReader(encoded))
			if err != nil {
				t.Fatalf("Parse(marshaled) = %v; yaml=\n%s", err, encoded)
			}

			// ReleaseSourceRaw holds the parse-time YAML node and is intentionally
			// not reproduced from a hand-built Config; everything else must survive.
			want := test.config
			want.ReleaseSourceRaw = yaml.Node{}
			got.ReleaseSourceRaw = yaml.Node{}
			if !reflect.DeepEqual(*got, want) {
				t.Fatalf("round trip mismatch; yaml=\n%s\ngot:  %+v\nwant: %+v", encoded, *got, want)
			}
		})
	}
}
