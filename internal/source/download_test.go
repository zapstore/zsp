package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPStatusError(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		header        http.Header
		wantRateLimit bool
		wantTemporary bool
		wantContains  string
	}{
		{name: "404", status: http.StatusNotFound, wantContains: "404"},
		{name: "plain 403", status: http.StatusForbidden, wantContains: "forbidden"},
		{name: "403 with exhausted GitHub quota", status: http.StatusForbidden, header: http.Header{"X-Ratelimit-Remaining": {"0"}}, wantRateLimit: true, wantContains: "rate limited"},
		{name: "403 with exhausted GitLab quota", status: http.StatusForbidden, header: http.Header{"Ratelimit-Remaining": {"0"}}, wantRateLimit: true, wantContains: "rate limited"},
		{name: "403 with Retry-After", status: http.StatusForbidden, header: http.Header{"Retry-After": {"60"}}, wantRateLimit: true, wantContains: "retry after 60"},
		{name: "403 with quota left", status: http.StatusForbidden, header: http.Header{"X-Ratelimit-Remaining": {"12"}}, wantContains: "forbidden"},
		{name: "429", status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"30"}}, wantRateLimit: true, wantTemporary: true, wantContains: "retry after 30"},
		{name: "500", status: http.StatusInternalServerError, wantTemporary: true, wantContains: "try again later"},
		{name: "408", status: http.StatusRequestTimeout, wantTemporary: true},
		{name: "400", status: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := testResponse(tt.status, "")
			for name, values := range tt.header {
				resp.Header[name] = values
			}
			err := checkHTTPStatus(resp, "Test API")
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("checkHTTPStatus() = %v, want *HTTPStatusError", err)
			}
			if statusErr.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", statusErr.StatusCode, tt.status)
			}
			if got := errors.Is(err, ErrRateLimited); got != tt.wantRateLimit {
				t.Errorf("errors.Is(ErrRateLimited) = %v, want %v", got, tt.wantRateLimit)
			}
			if statusErr.Temporary() != tt.wantTemporary {
				t.Errorf("Temporary() = %v, want %v", statusErr.Temporary(), tt.wantTemporary)
			}
			if tt.wantContains != "" && !strings.Contains(err.Error(), tt.wantContains) {
				t.Errorf("error = %q, want substring %q", err, tt.wantContains)
			}
		})
	}

	if err := checkHTTPStatus(testResponse(http.StatusOK, ""), "Test API"); err != nil {
		t.Errorf("checkHTTPStatus(200) = %v, want nil", err)
	}
}

func TestHTTPStatusErrorHintOnlyOnRateLimit(t *testing.T) {
	limited := checkHTTPStatus(testResponse(http.StatusTooManyRequests, ""), "GitHub API")
	if err := withHint(limited, "set GITHUB_TOKEN"); !strings.Contains(err.Error(), "set GITHUB_TOKEN") {
		t.Errorf("rate-limited error = %q, want the hint", err)
	}
	missing := checkHTTPStatus(testResponse(http.StatusNotFound, ""), "GitHub API")
	if err := withHint(missing, "set GITHUB_TOKEN"); strings.Contains(err.Error(), "set GITHUB_TOKEN") {
		t.Errorf("404 error = %q, want no hint", err)
	}
}

func TestStallReader(t *testing.T) {
	t.Run("silent body is closed and reported as a stall", func(t *testing.T) {
		pr, pw := io.Pipe()
		defer pw.Close()
		reader := newStallReader(pr, 50*time.Millisecond)
		defer reader.Close()

		_, err := reader.Read(make([]byte, 8))
		if !errors.Is(err, errDownloadStalled) {
			t.Fatalf("Read() error = %v, want errDownloadStalled", err)
		}
		if !IsTransient(err) {
			t.Error("stall error is not transient")
		}
		if _, err := reader.Read(make([]byte, 8)); !errors.Is(err, errDownloadStalled) {
			t.Errorf("Read() after stall error = %v, want errDownloadStalled", err)
		}
	})

	t.Run("slow consumer is not a stall", func(t *testing.T) {
		reader := newStallReader(io.NopCloser(strings.NewReader("hello world")), 50*time.Millisecond)
		defer reader.Close()

		buf := make([]byte, 5)
		var got []byte
		for {
			n, err := reader.Read(buf)
			got = append(got, buf[:n]...)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			time.Sleep(120 * time.Millisecond) // longer than the timeout, between reads
		}
		if string(got) != "hello world" {
			t.Errorf("read %q, want %q", got, "hello world")
		}
	})

	t.Run("regular errors pass through", func(t *testing.T) {
		boom := errors.New("boom")
		reader := newStallReader(io.NopCloser(errReader{boom}), time.Second)
		defer reader.Close()
		if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, boom) {
			t.Errorf("Read() error = %v, want boom", err)
		}
	})
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadBounded(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		max     int64
		wantErr bool
	}{
		{name: "under the cap", body: "abc", max: 10},
		{name: "exactly at the cap", body: "abcde", max: 5},
		{name: "one byte over", body: "abcdef", max: 5, wantErr: true},
		{name: "empty", body: "", max: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := readBounded(strings.NewReader(tt.body), tt.max)
			if tt.wantErr {
				if !errors.Is(err, errTooLarge) {
					t.Fatalf("readBounded() error = %v, want errTooLarge", err)
				}
				return
			}
			if err != nil || string(data) != tt.body {
				t.Fatalf("readBounded() = %q, %v; want %q", data, err, tt.body)
			}
		})
	}
}

func TestNormalizeSHA256(t *testing.T) {
	hexSum := strings.Repeat("ab", 32)
	tests := []struct{ in, want string }{
		{hexSum, hexSum},
		{"sha256:" + hexSum, hexSum},
		{strings.ToUpper(hexSum), hexSum},
		{"  " + hexSum + "\n", hexSum},
		{"sha1:" + hexSum, ""},
		{hexSum[:60], ""},
		{strings.Repeat("zz", 32), ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeSHA256(tt.in); got != tt.want {
			t.Errorf("normalizeSHA256(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDownloadHTTPVerifiesSHA256(t *testing.T) {
	payload := []byte("apk-bytes-ok")
	sum := sha256.Sum256(payload)
	good := hex.EncodeToString(sum[:])
	bad := strings.Repeat("0", 64)

	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{name: "matching digest", want: good},
		{name: "matching uppercase digest", want: strings.ToUpper(good)},
		{name: "no digest published", want: ""},
		{name: "mismatching digest", want: bad, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				_, _ = w.Write(payload)
			}))
			defer srv.Close()

			dest := filepath.Join(t.TempDir(), "app.apk")
			err := downloadHTTP(context.Background(), nil, srv.URL, dest, 0, tt.want, nil, nil)
			if tt.wantErr {
				if !errors.Is(err, ErrChecksumMismatch) {
					t.Fatalf("downloadHTTP() error = %v, want ErrChecksumMismatch", err)
				}
				if IsTransient(err) || hits.Load() != 1 {
					t.Errorf("mismatch was retried: transient = %v, hits = %d", IsTransient(err), hits.Load())
				}
				if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
					t.Errorf("mismatching download left on disk, stat error = %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("downloadHTTP() error = %v", err)
			}
			if got, _ := os.ReadFile(dest); string(got) != string(payload) {
				t.Errorf("downloaded %q, want %q", got, payload)
			}
		})
	}
}

func TestDownloadHTTPRetriesOnlyTransientStatuses(t *testing.T) {
	tests := []struct {
		status   int
		wantHits int32
	}{
		{http.StatusNotFound, 1},
		{http.StatusForbidden, 1},
		{http.StatusServiceUnavailable, downloadMaxAttempts},
		{http.StatusTooManyRequests, downloadMaxAttempts},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			dest := filepath.Join(t.TempDir(), "app.apk")
			err := downloadHTTP(context.Background(), nil, srv.URL, dest, 0, "", nil, nil)
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != tt.status {
				t.Fatalf("downloadHTTP() error = %v, want *HTTPStatusError %d", err, tt.status)
			}
			if hits.Load() != tt.wantHits {
				t.Errorf("hits = %d, want %d", hits.Load(), tt.wantHits)
			}
		})
	}
}
