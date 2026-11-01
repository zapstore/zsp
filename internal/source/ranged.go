package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// rangePageSize is the unit a RangeReader fetches and caches.
	rangePageSize = 64 << 10

	// rangeMaxReadAhead is the most a sequential read fetches in one request.
	rangeMaxReadAhead = 16 * rangePageSize

	// DefaultRangeBudget is how many bytes a RangeReader fetches before it
	// gives up. An APK's identity takes its archive index, signing block and
	// manifest, a small part of the file.
	DefaultRangeBudget int64 = 8 << 20

	rangeRequestTimeout = 30 * time.Second
)

var (
	// ErrRangeUnsupported means the server does not answer byte-range requests.
	ErrRangeUnsupported = errors.New("server does not support range requests")

	// ErrRangeBudget means reading the resource would take more bytes than the
	// budget allows.
	ErrRangeBudget = errors.New("range read budget exhausted")
)

// RangeReader is an [io.ReaderAt] over an HTTP resource. It fetches the resource
// in pages with Range requests and keeps what it fetched, so a reader that
// seeks around an archive, such as zip, costs a few requests and a small part of
// the bytes. Reads that continue where the last fetch ended grow the next fetch,
// so reading a long run forward does not cost a request per page.
//
// It is safe for concurrent use.
type RangeReader struct {
	ctx     context.Context
	client  *http.Client
	url     string
	headers map[string]string
	size    int64
	budget  int64

	mu        sync.Mutex
	pages     map[int64][]byte
	fetched   int64
	requests  int
	nextPage  int64 // the page a sequential read would fetch next
	readAhead int64 // pages the next sequential fetch adds
	err       error
}

// OpenRange returns a reader over the resource at rawURL, size bytes long. A zero
// size is learned from the server with a one-byte range request. budget bounds
// the bytes fetched; zero uses [DefaultRangeBudget]. headers are sent with every
// request, for a forge that needs a token.
func OpenRange(ctx context.Context, rawURL string, size int64, budget int64, headers map[string]string) (*RangeReader, error) {
	if err := validateDownloadURL(rawURL); err != nil {
		return nil, err
	}
	if budget <= 0 {
		budget = DefaultRangeBudget
	}
	r := &RangeReader{
		ctx:     ctx,
		client:  newDownloadHTTPClient(nil),
		url:     rawURL,
		headers: headers,
		size:    size,
		budget:  budget,
		pages:   make(map[int64][]byte),
	}
	if size <= 0 {
		total, err := r.learnSize()
		if err != nil {
			return nil, err
		}
		r.size = total
	}
	return r, nil
}

// Size is the length of the resource.
func (r *RangeReader) Size() int64 { return r.size }

// Fetched is the number of bytes fetched so far, and Requests the number of
// requests that took.
func (r *RangeReader) Fetched() (bytes int64, requests int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fetched, r.requests
}

// Err returns the first error a fetch gave. A parser may take a failed read for
// a missing structure and carry on, so a caller that needs every read to have
// worked checks Err after it.
func (r *RangeReader) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// ReadAt implements [io.ReaderAt].
func (r *RangeReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= r.size {
		return 0, io.EOF
	}
	end := off + int64(len(p))
	short := false
	if end > r.size {
		end, short = r.size, true
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.load(off, end); err != nil {
		if r.err == nil {
			r.err = err
		}
		return 0, err
	}
	n := 0
	for pos := off; pos < end; {
		page := r.pages[pos/rangePageSize]
		within := pos - (pos/rangePageSize)*rangePageSize
		copied := copy(p[n:end-off], page[within:])
		n += copied
		pos += int64(copied)
	}
	if short {
		return n, io.EOF
	}
	return n, nil
}

// load makes every page of [from, to) available, fetching each run of missing
// pages with one request.
func (r *RangeReader) load(from, to int64) error {
	first, last := from/rangePageSize, (to-1)/rangePageSize
	for page := first; page <= last; page++ {
		if _, ok := r.pages[page]; ok {
			continue
		}
		run := page
		for run < last {
			if _, ok := r.pages[run+1]; ok {
				break
			}
			run++
		}

		// A read that picks up where the last fetch ended is a sequential
		// one: fetch ahead, doubling each time.
		if page == r.nextPage && r.nextPage > 0 {
			r.readAhead = min(max(r.readAhead*2, 1), rangeMaxReadAhead/rangePageSize)
			run += r.readAhead
		} else {
			r.readAhead = 0
		}
		lastPage := (r.size - 1) / rangePageSize
		run = min(run, lastPage)
		for run > page {
			if _, ok := r.pages[run]; !ok {
				break
			}
			run--
		}

		if err := r.fetch(page, run); err != nil {
			return err
		}
		r.nextPage = run + 1
		page = run
	}
	return nil
}

// fetch requests pages first..last in one range request and stores them.
func (r *RangeReader) fetch(first, last int64) error {
	start := first * rangePageSize
	end := min((last+1)*rangePageSize, r.size) // exclusive
	if r.fetched+(end-start) > r.budget {
		return fmt.Errorf("%w: %d bytes allowed", ErrRangeBudget, r.budget)
	}

	body, err := r.get(start, end-1)
	if err != nil {
		return err
	}
	r.requests++
	r.fetched += int64(len(body))
	for page := first; page <= last; page++ {
		from := (page - first) * rangePageSize
		to := min(from+rangePageSize, int64(len(body)))
		r.pages[page] = body[from:to]
	}
	return nil
}

// get requests the inclusive byte range [start, end] and returns exactly those
// bytes.
func (r *RangeReader) get(start, end int64) ([]byte, error) {
	resp, err := r.do(fmt.Sprintf("bytes=%d-%d", start, end))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	from, _, _, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if resp.StatusCode != http.StatusPartialContent || !ok || from != start {
		return nil, ErrRangeUnsupported
	}
	want := end - start + 1
	body, err := io.ReadAll(io.LimitReader(resp.Body, want))
	if err != nil {
		return nil, fmt.Errorf("read range: %w", err)
	}
	if int64(len(body)) != want {
		return nil, fmt.Errorf("read range: got %d bytes, want %d", len(body), want)
	}
	return body, nil
}

// learnSize asks for the first byte, which answers with the resource's length.
func (r *RangeReader) learnSize() (int64, error) {
	resp, err := r.do("bytes=0-0")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	_, _, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if resp.StatusCode != http.StatusPartialContent || !ok || total <= 0 {
		return 0, ErrRangeUnsupported
	}
	return total, nil
}

func (r *RangeReader) do(rangeHeader string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(r.ctx, rangeRequestTimeout)
	req, err := newRequest(ctx, http.MethodGet, r.url)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("range read: %w", err)
	}
	for key, value := range r.headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Range", rangeHeader)

	resp, err := DoWithTorFallback(ctx, r.client, req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("range read: %w", err)
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		defer cancel()
		defer resp.Body.Close()
		return nil, checkHTTPStatus(resp, "range read")
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnClose releases a request's timeout when its body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// parseContentRange reads "bytes <from>-<to>/<total>".
func parseContentRange(value string) (from, to, total int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(value), "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	span, size, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, 0, false
	}
	low, high, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, 0, false
	}
	var err error
	if from, err = strconv.ParseInt(low, 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if to, err = strconv.ParseInt(high, 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if size == "*" {
		return from, to, 0, true
	}
	if total, err = strconv.ParseInt(size, 10, 64); err != nil {
		return 0, 0, 0, false
	}
	return from, to, total, true
}
