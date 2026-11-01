package source

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// rangeServer serves content with range support and counts the requests.
func rangeServer(t *testing.T, content []byte) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.ServeContent(w, r, "app.apk", time.Time{}, bytes.NewReader(content))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func randomContent(size int) []byte {
	content := make([]byte, size)
	rand.New(rand.NewSource(1)).Read(content)
	return content
}

func TestRangeReaderReadsAnyPart(t *testing.T) {
	content := randomContent(1<<20 + 123)
	server, requests := rangeServer(t, content)

	r, err := OpenRange(t.Context(), server.URL, int64(len(content)), 0, nil)
	if err != nil {
		t.Fatalf("OpenRange: %v", err)
	}
	if r.Size() != int64(len(content)) {
		t.Fatalf("Size = %d, want %d", r.Size(), len(content))
	}

	for _, c := range []struct{ off, n int }{
		{len(content) - 1000, 1000}, // the tail, where a zip index is
		{0, 4096},                   // the head
		{70000, 200000},             // across pages
		{len(content) - 10, 10},     // inside a page already fetched
		{rangePageSize - 5, 10},     // across a page boundary
		{len(content) - 5, 100},     // past the end: short read
	} {
		got := make([]byte, c.n)
		n, err := r.ReadAt(got, int64(c.off))
		wantN := min(c.n, len(content)-c.off)
		if n != wantN {
			t.Fatalf("ReadAt(%d, %d) = %d bytes, want %d", c.off, c.n, n, wantN)
		}
		if wantN < c.n && !errors.Is(err, io.EOF) {
			t.Fatalf("ReadAt(%d, %d) past the end = %v, want io.EOF", c.off, c.n, err)
		}
		if wantN == c.n && err != nil {
			t.Fatalf("ReadAt(%d, %d) = %v", c.off, c.n, err)
		}
		if !bytes.Equal(got[:n], content[c.off:c.off+n]) {
			t.Fatalf("ReadAt(%d, %d) returned the wrong bytes", c.off, c.n)
		}
	}

	// reading again what was fetched costs nothing
	before := requests.Load()
	if _, err := r.ReadAt(make([]byte, 4096), 0); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != before {
		t.Errorf("rereading cached bytes made %d requests", requests.Load()-before)
	}
	if fetched, _ := r.Fetched(); fetched >= int64(len(content)) {
		t.Errorf("fetched %d of %d bytes: the reads should not have taken the whole file", fetched, len(content))
	}
}

func TestRangeReaderSequentialReadsGrow(t *testing.T) {
	content := randomContent(1 << 20)
	server, requests := rangeServer(t, content)
	r, err := OpenRange(t.Context(), server.URL, int64(len(content)), 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	// zip reads its central directory forward in small steps
	buf := make([]byte, 4096)
	for off := 0; off < len(content); off += len(buf) {
		if _, err := r.ReadAt(buf, int64(off)); err != nil {
			t.Fatalf("ReadAt(%d): %v", off, err)
		}
		if !bytes.Equal(buf, content[off:off+len(buf)]) {
			t.Fatalf("wrong bytes at %d", off)
		}
	}
	pages := (len(content) + rangePageSize - 1) / rangePageSize
	if got := requests.Load(); got >= int64(pages)/2 {
		t.Errorf("a forward read of %d pages took %d requests, want read-ahead to cut that well below", pages, got)
	}
}

func TestRangeReaderLearnsTheSize(t *testing.T) {
	content := randomContent(300_000)
	server, _ := rangeServer(t, content)
	r, err := OpenRange(t.Context(), server.URL, 0, 0, nil)
	if err != nil {
		t.Fatalf("OpenRange: %v", err)
	}
	if r.Size() != int64(len(content)) {
		t.Fatalf("Size = %d, want %d", r.Size(), len(content))
	}
}

func TestRangeReaderNeedsRangeSupport(t *testing.T) {
	// a server that ignores Range and sends the whole body
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(randomContent(200_000))
	}))
	t.Cleanup(server.Close)

	if _, err := OpenRange(t.Context(), server.URL, 0, 0, nil); !errors.Is(err, ErrRangeUnsupported) {
		t.Fatalf("OpenRange without a size = %v, want ErrRangeUnsupported", err)
	}
	r, err := OpenRange(t.Context(), server.URL, 200_000, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadAt(make([]byte, 10), 0); !errors.Is(err, ErrRangeUnsupported) {
		t.Fatalf("ReadAt = %v, want ErrRangeUnsupported", err)
	}
}

func TestRangeReaderStopsAtItsBudget(t *testing.T) {
	content := randomContent(1 << 20)
	server, _ := rangeServer(t, content)
	r, err := OpenRange(t.Context(), server.URL, int64(len(content)), 2*rangePageSize, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadAt(make([]byte, 10), 0); err != nil {
		t.Fatalf("first page: %v", err)
	}
	if _, err := r.ReadAt(make([]byte, 10), 500_000); err != nil {
		t.Fatalf("second page: %v", err)
	}
	if _, err := r.ReadAt(make([]byte, 10), 800_000); !errors.Is(err, ErrRangeBudget) {
		t.Fatalf("third page = %v, want ErrRangeBudget", err)
	}
	if !errors.Is(r.Err(), ErrRangeBudget) {
		t.Errorf("Err() = %v, want the budget error remembered", r.Err())
	}
	if fetched, _ := r.Fetched(); fetched > 2*rangePageSize {
		t.Errorf("fetched %d bytes, want at most the budget %d", fetched, 2*rangePageSize)
	}
}

func TestRangeReaderSendsHeadersAndReportsHTTPErrors(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	_, err := OpenRange(t.Context(), server.URL, 0, 0, map[string]string{"Authorization": "Bearer t"})
	if err == nil || errors.Is(err, ErrRangeUnsupported) {
		t.Fatalf("OpenRange on a 404 = %v, want the HTTP error", err)
	}
	if gotAuth != "Bearer t" {
		t.Errorf("Authorization = %q, want the configured header", gotAuth)
	}
}

func TestRangeReaderRejectsInsecureURL(t *testing.T) {
	if _, err := OpenRange(t.Context(), "http://example.com/app.apk", 10, 0, nil); err == nil {
		t.Fatal("OpenRange over plain HTTP outside loopback should be refused")
	}
}

func TestParseContentRange(t *testing.T) {
	tests := []struct {
		in              string
		from, to, total int64
		ok              bool
	}{
		{"bytes 0-0/1234", 0, 0, 1234, true},
		{"bytes 100-199/1234", 100, 199, 1234, true},
		{"bytes 0-9/*", 0, 9, 0, true},
		{"bytes */1234", 0, 0, 0, false},
		{"items 0-1/2", 0, 0, 0, false},
		{"", 0, 0, 0, false},
	}
	for _, tt := range tests {
		from, to, total, ok := parseContentRange(tt.in)
		if ok != tt.ok || (ok && (from != tt.from || to != tt.to || total != tt.total)) {
			t.Errorf("parseContentRange(%q) = %d %d %d %v, want %d %d %d %v", tt.in, from, to, total, ok, tt.from, tt.to, tt.total, tt.ok)
		}
	}
}
