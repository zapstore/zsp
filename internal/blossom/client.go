// Package blossom handles file uploads to Blossom servers.
//
// Uploads follow strict BUD-11/BUD-06/BUD-02 behavior: a five-minute kind
// 24242 authorization scoped to the upload action, the file's hash, and the
// server's lowercase host; a HEAD /upload preflight sent with that
// authorization and the same X-SHA-256/X-Content-Type/X-Content-Length
// facts; exactly one PUT /upload attempt using those same facts when the
// preflight is accepted; and exact validation of the returned blob
// descriptor. There are no automatic retries.
package blossom

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	nostrpkg "github.com/zapstore/zsp/internal/nostr"
)

const (
	// DefaultServer is the default Blossom server URL.
	DefaultServer = "https://cdn.zapstore.dev"

	// AuthExpiration is how long a BUD-11 authorization token is valid.
	AuthExpiration = 5 * time.Minute

	// apkContentType is the MIME type historically used by Upload and
	// UploadWithAuth, which only ever transferred APKs. It remains the
	// default when a caller does not supply an explicit content type, so
	// existing callers keep their current behavior; new callers may pass a
	// content type to use these methods generically for any file.
	apkContentType = "application/vnd.android.package-archive"

	// octetStreamContentType is the fallback content type for UploadBytes
	// callers that do not supply one.
	octetStreamContentType = "application/octet-stream"
)

// Client handles Blossom uploads.
type Client struct {
	serverURL  string
	httpClient *http.Client
}

// NewClient creates a new Blossom client.
func NewClient(serverURL string) *Client {
	if serverURL == "" {
		serverURL = DefaultServer
	}
	return &Client{
		serverURL:  serverURL,
		httpClient: newSecureHTTPClient(5 * time.Minute),
	}
}

// newSecureHTTPClient creates an HTTP client with security best practices.
func newSecureHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		// Upload preflight and PUT are each allowed exactly one request to the
		// configured server. Returning the redirect response lets callers
		// classify it as a rejection without following or replaying it.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// UploadResult contains the result of an upload.
type UploadResult struct {
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Type    string `json:"type"`
	Existed bool   `json:"-"` // True if the blob already existed on the server (PUT returned 200 rather than 201).
}

// ProgressFunc is called during upload to report progress.
type ProgressFunc func(uploaded, total int64)

// host returns the lowercase domain name of the configured Blossom server,
// suitable for a BUD-11 "server" authorization tag (a domain name only, not
// a full URL).
func (c *Client) host() string {
	parsed, err := url.Parse(c.serverURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// Upload signs a fresh five-minute BUD-11 authorization scoped to this
// server and uploads a local file. contentType is optional and defaults to
// the APK MIME type for backward compatibility; callers may pass their own
// content type to use Upload generically for any file.
func (c *Client) Upload(ctx context.Context, filePath string, sha256 string, signer nostrpkg.Signer, onProgress ProgressFunc, contentType ...string) (*UploadResult, error) {
	authEvent := nostrpkg.BuildBlossomAuthEvent(sha256, signer.PublicKey(), time.Now().Add(AuthExpiration), c.host())
	signCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := signer.Sign(signCtx, authEvent); err != nil {
		return nil, fmt.Errorf("failed to sign auth event: %w", err)
	}
	return c.UploadWithAuth(ctx, filePath, sha256, authEvent, onProgress, contentType...)
}

// UploadWithAuth uploads a local file using a pre-signed BUD-11 authorization
// event, performing one HEAD /upload preflight followed by, if accepted,
// exactly one PUT /upload attempt using the same hash, content type, size,
// and authorization. It does not retry. contentType is optional and
// defaults to the APK MIME type for backward compatibility.
func (c *Client) UploadWithAuth(ctx context.Context, filePath string, sha256 string, authEvent *nostr.Event, onProgress ProgressFunc, contentType ...string) (*UploadResult, error) {
	if err := c.validateAuthEvent(authEvent, sha256); err != nil {
		return nil, err
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	authHeader, err := encodeAuthHeader(authEvent)
	if err != nil {
		return nil, err
	}
	ct := resolveContentType(contentType, apkContentType)
	size := fi.Size()

	if err := c.preflight(ctx, authHeader, sha256, ct, size); err != nil {
		return nil, err
	}

	var reader io.Reader = f
	if onProgress != nil {
		reader = &progressReader{
			reader:     f,
			total:      size,
			onProgress: onProgress,
		}
	}

	return c.putUpload(ctx, reader, authHeader, sha256, ct, size)
}

// UploadBytes signs a fresh five-minute BUD-11 authorization scoped to this
// server and uploads raw bytes.
func (c *Client) UploadBytes(ctx context.Context, data []byte, sha256 string, contentType string, signer nostrpkg.Signer) (*UploadResult, error) {
	authEvent := nostrpkg.BuildBlossomAuthEvent(sha256, signer.PublicKey(), time.Now().Add(AuthExpiration), c.host())
	signCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := signer.Sign(signCtx, authEvent); err != nil {
		return nil, fmt.Errorf("failed to sign auth event: %w", err)
	}
	return c.UploadBytesWithAuth(ctx, data, sha256, contentType, authEvent)
}

// UploadBytesWithAuth uploads raw bytes using a pre-signed BUD-11
// authorization event, following the same strict HEAD-then-PUT behavior as
// UploadWithAuth.
func (c *Client) UploadBytesWithAuth(ctx context.Context, data []byte, sha256 string, contentType string, authEvent *nostr.Event) (*UploadResult, error) {
	return c.uploadBytesWithAuth(ctx, data, sha256, contentType, authEvent)
}

// UploadBytesWithAuthPreChecked is retained for callers that already probed a
// blob, but strict publication still performs the required HEAD /upload.
func (c *Client) UploadBytesWithAuthPreChecked(ctx context.Context, data []byte, sha256 string, contentType string, authEvent *nostr.Event, existed bool) (*UploadResult, error) {
	_ = existed
	return c.uploadBytesWithAuth(ctx, data, sha256, contentType, authEvent)
}

// uploadBytesWithAuth performs the strict BUD-06/BUD-02 preflight-then-PUT
// upload for in-memory bytes.
func (c *Client) uploadBytesWithAuth(ctx context.Context, data []byte, sha256 string, contentType string, authEvent *nostr.Event) (*UploadResult, error) {
	if err := c.validateAuthEvent(authEvent, sha256); err != nil {
		return nil, err
	}

	authHeader, err := encodeAuthHeader(authEvent)
	if err != nil {
		return nil, err
	}
	ct := resolveContentType([]string{contentType}, octetStreamContentType)
	size := int64(len(data))

	if err := c.preflight(ctx, authHeader, sha256, ct, size); err != nil {
		return nil, err
	}

	return c.putUpload(ctx, bytes.NewReader(data), authHeader, sha256, ct, size)
}

// preflight performs the BUD-06 HEAD /upload check, reusing the exact
// authorization, hash, content type, and size that the subsequent PUT will
// send.
func (c *Client) preflight(ctx context.Context, authHeader, sha256, contentType string, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.serverURL+"/upload", nil)
	if err != nil {
		return fmt.Errorf("failed to create upload preflight request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("X-SHA-256", sha256)
	req.Header.Set("X-Content-Type", contentType)
	req.Header.Set("X-Content-Length", strconv.FormatInt(size, 10))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload preflight failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &StatusError{Err: ErrPreflightRejected, StatusCode: resp.StatusCode, Reason: reasonFromHeader(resp)}
	}
	return nil
}

// putUpload performs the BUD-02 PUT /upload with the same facts validated
// (and, for HEAD, sent) by preflight, then validates the returned blob
// descriptor exactly.
func (c *Client) putUpload(ctx context.Context, body io.Reader, authHeader, sha256, contentType string, size int64) (*UploadResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.serverURL+"/upload", body)
	if err != nil {
		return nil, fmt.Errorf("failed to create upload request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Digest", sha256)
	req.Header.Set("X-SHA-256", sha256)
	req.Header.Set("X-Content-Type", contentType)
	req.Header.Set("X-Content-Length", strconv.FormatInt(size, 10))
	req.ContentLength = size

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, uploadError(resp)
	}

	var descriptor UploadResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&descriptor); err != nil {
		return nil, fmt.Errorf("%w: response was not a blob descriptor: %v", ErrInvalidDescriptor, err)
	}
	// BUD-02: 200 OK means the blob already existed; 201 Created means it
	// was newly stored.
	descriptor.Existed = resp.StatusCode == http.StatusOK

	if err := c.validateDescriptor(&descriptor, sha256, contentType, size); err != nil {
		return nil, err
	}
	return &descriptor, nil
}

// validateDescriptor checks the returned BUD-02 blob descriptor against the
// exact facts that were authorized and sent: hash, size, type, and a URL
// that resolves to this server and this hash.
func (c *Client) validateDescriptor(d *UploadResult, sha256, contentType string, size int64) error {
	if d.SHA256 == "" || !strings.EqualFold(d.SHA256, sha256) {
		return fmt.Errorf("%w: sha256 %q, want %q", ErrInvalidDescriptor, d.SHA256, sha256)
	}
	d.SHA256 = sha256
	if d.Size != size {
		return fmt.Errorf("%w: size %d, want %d", ErrInvalidDescriptor, d.Size, size)
	}
	if !strings.EqualFold(d.Type, contentType) {
		return fmt.Errorf("%w: type %q, want %q", ErrInvalidDescriptor, d.Type, contentType)
	}
	return c.validateDescriptorURL(d.URL, sha256)
}

// validateDescriptorURL confirms the descriptor's URL names this server and
// this blob's hash, ignoring an optional file extension.
func (c *Client) validateDescriptorURL(rawURL, sha256 string) error {
	if rawURL == "" {
		return fmt.Errorf("%w: missing url", ErrInvalidDescriptor)
	}
	server, err := url.Parse(c.serverURL)
	if err != nil {
		return fmt.Errorf("%w: configured server URL is invalid: %v", ErrInvalidDescriptor, err)
	}
	got, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: url %q is invalid: %v", ErrInvalidDescriptor, rawURL, err)
	}
	if !strings.EqualFold(got.Scheme, server.Scheme) || !strings.EqualFold(got.Hostname(), server.Hostname()) {
		return fmt.Errorf("%w: url %q is not on the configured server %q", ErrInvalidDescriptor, rawURL, c.serverURL)
	}
	name := strings.TrimSuffix(path.Base(got.Path), path.Ext(got.Path))
	if !strings.EqualFold(name, sha256) {
		return fmt.Errorf("%w: url %q does not reference blob %q", ErrInvalidDescriptor, rawURL, sha256)
	}
	return nil
}

// validateAuthEvent confirms a BUD-11 kind 24242 authorization event
// authorizes an upload of sha256 on this server: correct kind, a t=upload
// tag, a matching x tag, an expiration that has not passed, and, when
// server tags are present, one naming this server's host.
func (c *Client) validateAuthEvent(authEvent *nostr.Event, sha256 string) error {
	if authEvent == nil {
		return fmt.Errorf("%w: authorization event is required", ErrInvalidAuthEvent)
	}
	if authEvent.Kind != nostrpkg.KindBlossomAuth {
		return fmt.Errorf("%w: kind %d, want %d", ErrInvalidAuthEvent, authEvent.Kind, nostrpkg.KindBlossomAuth)
	}
	if !hasTagValue(authEvent.Tags, "t", "upload") {
		return fmt.Errorf("%w: missing t=upload tag", ErrInvalidAuthEvent)
	}
	if !hasTagValueFold(authEvent.Tags, "x", sha256) {
		return fmt.Errorf("%w: missing x tag for %s", ErrInvalidAuthEvent, sha256)
	}
	expiresAt, ok := firstTagInt64(authEvent.Tags, "expiration")
	if !ok {
		return fmt.Errorf("%w: missing expiration tag", ErrInvalidAuthEvent)
	}
	if time.Now().Unix() >= expiresAt {
		return fmt.Errorf("%w: authorization has expired", ErrInvalidAuthEvent)
	}
	if host := c.host(); host != "" {
		if servers := tagValues(authEvent.Tags, "server"); len(servers) == 0 || !containsFold(servers, host) {
			return fmt.Errorf("%w: not authorized for server %s", ErrInvalidAuthEvent, host)
		}
	}
	return nil
}

// ServerURL returns the configured server URL.
func (c *Client) ServerURL() string {
	return c.serverURL
}

// encodeAuthHeader base64-encodes a signed authorization event for the
// Nostr Authorization header scheme.
func encodeAuthHeader(authEvent *nostr.Event) (string, error) {
	authJSON, err := json.Marshal(authEvent)
	if err != nil {
		return "", fmt.Errorf("failed to marshal auth event: %w", err)
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(authJSON), nil
}

// resolveContentType returns the caller-supplied content type, or def when
// none was supplied.
func resolveContentType(contentType []string, def string) string {
	if len(contentType) > 0 && contentType[0] != "" {
		return contentType[0]
	}
	return def
}

// reasonFromHeader extracts a human-readable rejection reason from a
// BUD-06/BUD-02 response header. Servers have used both header names across
// spec revisions.
func reasonFromHeader(resp *http.Response) string {
	if reason := strings.TrimSpace(resp.Header.Get("X-Reason")); reason != "" {
		return reason
	}
	return strings.TrimSpace(resp.Header.Get("X-Upload-Message"))
}

// uploadError builds an error from a non-2xx PUT /upload response,
// preferring a reason header and falling back to a short response body.
func uploadError(resp *http.Response) error {
	reason := reasonFromHeader(resp)
	if reason == "" {
		if body, err := io.ReadAll(io.LimitReader(resp.Body, 512)); err == nil {
			reason = strings.TrimSpace(string(body))
		}
	}
	return &StatusError{Err: ErrUploadRejected, StatusCode: resp.StatusCode, Reason: reason}
}

// tagValues returns the second element of every tag named key.
func tagValues(tags nostr.Tags, key string) []string {
	var values []string
	for _, t := range tags {
		if len(t) >= 2 && t[0] == key {
			values = append(values, t[1])
		}
	}
	return values
}

func hasTagValue(tags nostr.Tags, key, value string) bool {
	for _, v := range tagValues(tags, key) {
		if v == value {
			return true
		}
	}
	return false
}

func hasTagValueFold(tags nostr.Tags, key, value string) bool {
	for _, v := range tagValues(tags, key) {
		if strings.EqualFold(v, value) {
			return true
		}
	}
	return false
}

func containsFold(values []string, target string) bool {
	for _, v := range values {
		if strings.EqualFold(v, target) {
			return true
		}
	}
	return false
}

func firstTagInt64(tags nostr.Tags, key string) (int64, bool) {
	values := tagValues(tags, key)
	if len(values) == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// progressReader wraps a reader to track progress.
type progressReader struct {
	reader     io.Reader
	total      int64
	uploaded   int64
	onProgress ProgressFunc
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.uploaded += int64(n)
	if pr.onProgress != nil {
		pr.onProgress(pr.uploaded, pr.total)
	}
	return n, err
}
