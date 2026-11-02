package source

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	_ RangeOpener = (*GitHub)(nil)
	_ RangeOpener = (*Gitea)(nil)
	_ RangeOpener = (*GitLab)(nil)
)

// TestOpenRangeSendsTheForgesToken checks that a ranged read carries the same
// credential a download would, and only to the forge host it belongs to.
func TestOpenRangeSendsTheForgesToken(t *testing.T) {
	content := []byte(strings.Repeat("0123456789", 1000))
	tests := []struct {
		name   string
		opener func() RangeOpener
		env    func(t *testing.T, host string)
		header string
		want   string
	}{
		{
			name:   "gitea on its configured host",
			opener: func() RangeOpener { return &Gitea{} },
			env: func(t *testing.T, host string) {
				t.Setenv("GITEA_TOKEN", "gt")
				t.Setenv("GITEA_HOST", host)
			},
			header: "Authorization", want: "token gt",
		},
		{
			name:   "gitea on another host",
			opener: func() RangeOpener { return &Gitea{} },
			env: func(t *testing.T, host string) {
				t.Setenv("GITEA_TOKEN", "gt")
				t.Setenv("GITEA_HOST", "other.example.com")
			},
			header: "Authorization", want: "",
		},
		{
			name:   "gitlab on its configured host",
			opener: func() RangeOpener { return &GitLab{} },
			env: func(t *testing.T, host string) {
				t.Setenv("GITLAB_TOKEN", "gl")
				t.Setenv("GITLAB_HOST", host)
			},
			header: "PRIVATE-TOKEN", want: "gl",
		},
		{
			name:   "gitlab on another host",
			opener: func() RangeOpener { return &GitLab{} },
			env: func(t *testing.T, host string) {
				t.Setenv("GITLAB_TOKEN", "gl")
				t.Setenv("GITLAB_HOST", "other.example.com")
			},
			header: "PRIVATE-TOKEN", want: "",
		},
		{
			// the token is GitHub's, so a host that is not GitHub's never sees it
			name:   "github on a host that is not github",
			opener: func() RangeOpener { return &GitHub{} },
			env: func(t *testing.T, host string) {
				t.Setenv("GITHUB_TOKEN", "gh")
			},
			header: "Authorization", want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get(tt.header)
				http.ServeContent(w, r, "a.apk", time.Time{}, strings.NewReader(string(content)))
			}))
			t.Cleanup(server.Close)
			tt.env(t, strings.TrimPrefix(server.URL, "http://"))

			asset := &Asset{Name: "a.apk", URL: server.URL + "/a.apk", Size: int64(len(content))}
			reader, err := tt.opener().OpenRange(t.Context(), asset, 0)
			if err != nil {
				t.Fatalf("OpenRange: %v", err)
			}
			buf := make([]byte, 16)
			if _, err := reader.ReadAt(buf, 100); err != nil {
				t.Fatalf("ReadAt: %v", err)
			}
			if string(buf) != string(content[100:116]) {
				t.Errorf("read %q, want %q", buf, content[100:116])
			}
			if got != tt.want {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestOpenRangeNeedsAURL(t *testing.T) {
	for name, opener := range map[string]RangeOpener{"github": &GitHub{}, "gitea": &Gitea{}, "gitlab": &GitLab{}} {
		if _, err := opener.OpenRange(t.Context(), &Asset{Name: "a.apk"}, 0); err == nil {
			t.Errorf("%s: OpenRange of an asset with no URL succeeded", name)
		}
	}
}
