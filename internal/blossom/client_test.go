package blossom

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	nostrpkg "github.com/zapstore/zsp/internal/nostr"
)

// testSigner is a minimal nostrpkg.Signer backed by an ephemeral key, so
// tests can exercise Client.Upload/UploadBytes without production signer
// plumbing.
type testSigner struct {
	sk string
	pk string
}

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	sk := nostr.GeneratePrivateKey()
	pk, err := nostr.GetPublicKey(sk)
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}
	return &testSigner{sk: sk, pk: pk}
}

func (s *testSigner) Type() nostrpkg.SignerType { return nostrpkg.SignerNsec }
func (s *testSigner) PublicKey() string         { return s.pk }
func (s *testSigner) Close() error              { return nil }
func (s *testSigner) Sign(_ context.Context, event *nostr.Event) error {
	event.PubKey = s.pk
	return event.Sign(s.sk)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeTempFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// requestLog records the method and headers of every request the fake
// server receives, in order.
type requestLog struct {
	Method string
	Header http.Header
}

// newFakeServer returns a Blossom server double whose HEAD and PUT
// /upload behavior is controlled by the caller, and a pointer to the
// ordered log of requests it received.
func newFakeServer(t *testing.T, head func(w http.ResponseWriter, r *http.Request), put func(w http.ResponseWriter, r *http.Request, body []byte)) (*httptest.Server, *[]requestLog) {
	t.Helper()
	log := &[]requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*log = append(*log, requestLog{Method: r.Method, Header: r.Header.Clone()})
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/upload":
			if head != nil {
				head(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/upload":
			body, _ := io.ReadAll(r.Body)
			if put != nil {
				put(w, r, body)
				return
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

func descriptorJSON(url, sha256, mimeType string, size int64) []byte {
	b, _ := json.Marshal(UploadResult{URL: url, SHA256: sha256, Size: size, Type: mimeType})
	return b
}

func TestUploadAuthEventShape(t *testing.T) {
	data := []byte("apk bytes")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)

	var putCount, headCount int32
	var capturedAuth *nostr.Event
	srv, _ := newFakeServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&headCount, 1)
			w.WriteHeader(http.StatusOK)
		},
		func(w http.ResponseWriter, r *http.Request, body []byte) {
			atomic.AddInt32(&putCount, 1)
			evt, err := decodeAuthHeader(r.Header.Get("Authorization"))
			if err != nil {
				t.Fatalf("decodeAuthHeader() error = %v", err)
			}
			capturedAuth = evt
			w.WriteHeader(http.StatusCreated)
			w.Write(descriptorJSON(fmt.Sprintf("%s/%s", serverURLFromRequest(r), hash), hash, apkContentType, int64(len(data))))
		},
	)

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	before := time.Now()
	result, err := client.Upload(context.Background(), filePath, hash, signer, nil)
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if result.SHA256 != hash {
		t.Errorf("SHA256 = %q, want %q", result.SHA256, hash)
	}
	if headCount != 1 || putCount != 1 {
		t.Fatalf("headCount = %d, putCount = %d, want 1 and 1 (no retries)", headCount, putCount)
	}
	if capturedAuth == nil {
		t.Fatal("PUT request had no decodable authorization event")
	}
	if capturedAuth.Kind != nostrpkg.KindBlossomAuth {
		t.Errorf("auth kind = %d, want %d", capturedAuth.Kind, nostrpkg.KindBlossomAuth)
	}
	if !hasTagValue(capturedAuth.Tags, "t", "upload") {
		t.Errorf("auth tags missing t=upload: %v", capturedAuth.Tags)
	}
	if !hasTagValueFold(capturedAuth.Tags, "x", hash) {
		t.Errorf("auth tags missing x=%s: %v", hash, capturedAuth.Tags)
	}
	wantHost := strings.ToLower(strings.Split(strings.TrimPrefix(srv.URL, "http://"), ":")[0])
	if servers := tagValues(capturedAuth.Tags, "server"); !containsFold(servers, wantHost) {
		t.Errorf("auth server tags = %v, want to contain %q", servers, wantHost)
	}
	expiresAt, ok := firstTagInt64(capturedAuth.Tags, "expiration")
	if !ok {
		t.Fatal("auth event missing expiration tag")
	}
	wantExpiry := before.Add(AuthExpiration).Unix()
	if diff := expiresAt - wantExpiry; diff < -2 || diff > 2 {
		t.Errorf("expiration = %d, want ~%d (5 minutes from now)", expiresAt, wantExpiry)
	}
	ok2, err := capturedAuth.CheckSignature()
	if err != nil || !ok2 {
		t.Errorf("auth event signature invalid: ok=%v err=%v", ok2, err)
	}
}

func TestClientHostLowercasesConfiguredHost(t *testing.T) {
	client := NewClient("https://CDN.Example.COM:8443/")
	if got, want := client.host(), "cdn.example.com"; got != want {
		t.Errorf("host() = %q, want %q", got, want)
	}
}

func TestUploadPreflightHeaders(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)
	const contentType = "application/vnd.android.package-archive"

	var headHeader, putHeader http.Header
	srv, log := newFakeServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			headHeader = r.Header.Clone()
			w.WriteHeader(http.StatusOK)
		},
		func(w http.ResponseWriter, r *http.Request, body []byte) {
			putHeader = r.Header.Clone()
			w.WriteHeader(http.StatusCreated)
			w.Write(descriptorJSON(fmt.Sprintf("%s/%s", serverURLFromRequest(r), hash), hash, contentType, int64(len(data))))
		},
	)

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	if _, err := client.Upload(context.Background(), filePath, hash, signer, nil); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	if len(*log) != 2 || (*log)[0].Method != http.MethodHead || (*log)[1].Method != http.MethodPut {
		t.Fatalf("request sequence = %v, want [HEAD PUT]", *log)
	}

	for _, h := range []http.Header{headHeader, putHeader} {
		if h.Get("X-SHA-256") != hash {
			t.Errorf("X-SHA-256 = %q, want %q", h.Get("X-SHA-256"), hash)
		}
		if h.Get("X-Content-Type") != contentType {
			t.Errorf("X-Content-Type = %q, want %q", h.Get("X-Content-Type"), contentType)
		}
		if h.Get("X-Content-Length") != strconv.Itoa(len(data)) {
			t.Errorf("X-Content-Length = %q, want %d", h.Get("X-Content-Length"), len(data))
		}
		if h.Get("Authorization") == "" {
			t.Error("Authorization header is empty")
		}
	}
	if headHeader.Get("Authorization") != putHeader.Get("Authorization") {
		t.Error("HEAD and PUT authorization headers differ, want identical facts/auth")
	}
	if putHeader.Get("Content-Digest") != hash {
		t.Errorf("PUT Content-Digest = %q, want %q", putHeader.Get("Content-Digest"), hash)
	}
}

func TestUploadRejectedPreflightSkipsPUT(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)

	srv, log := newFakeServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Reason", "quota exceeded")
			w.WriteHeader(http.StatusForbidden)
		},
		func(w http.ResponseWriter, r *http.Request, body []byte) {
			t.Fatal("PUT /upload must not be sent when the preflight is rejected")
		},
	)

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	_, err := client.Upload(context.Background(), filePath, hash, signer, nil)
	if err == nil {
		t.Fatal("Upload() error = nil, want preflight rejection")
	}
	if !errors.Is(err, ErrPreflightRejected) {
		t.Errorf("error = %v, want ErrPreflightRejected", err)
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("error = %v, want it to include the server's reason", err)
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusForbidden {
		t.Errorf("error = %v, want StatusError with status 403", err)
	}
	if len(*log) != 1 {
		t.Fatalf("requests = %d, want exactly 1 (HEAD only, no retry)", len(*log))
	}
}

func TestUploadRejectedPUT(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)

	srv, log := newFakeServer(t, nil, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("X-Reason", "blob too large")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	})

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	_, err := client.Upload(context.Background(), filePath, hash, signer, nil)
	if !errors.Is(err, ErrUploadRejected) {
		t.Fatalf("error = %v, want ErrUploadRejected", err)
	}
	if len(*log) != 2 {
		t.Fatalf("requests = %d, want exactly 2 (one HEAD, one PUT, no retry)", len(*log))
	}
}

func TestUploadNoRetryOnTransportError(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)

	var calls int32
	srv, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}, func(w http.ResponseWriter, r *http.Request, body []byte) {
		atomic.AddInt32(&calls, 1)
		// Close the connection without a response to simulate a transport
		// failure on the PUT.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("Hijack() error = %v", err)
		}
		conn.Close()
	})

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	_, err := client.Upload(context.Background(), filePath, hash, signer, nil)
	if err == nil {
		t.Fatal("Upload() error = nil, want a transport error")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want exactly 2 (no retry after the failed PUT)", got)
	}
}

func TestUploadValidatesDescriptorHash(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)
	otherHash := sha256Hex([]byte("different"))

	srv, _ := newFakeServer(t, nil, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(http.StatusCreated)
		w.Write(descriptorJSON(fmt.Sprintf("%s/%s", serverURLFromRequest(r), otherHash), otherHash, apkContentType, int64(len(data))))
	})

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	_, err := client.Upload(context.Background(), filePath, hash, signer, nil)
	if !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("error = %v, want ErrInvalidDescriptor", err)
	}
}

func TestUploadValidatesDescriptorSize(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)

	srv, _ := newFakeServer(t, nil, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(http.StatusCreated)
		w.Write(descriptorJSON(fmt.Sprintf("%s/%s", serverURLFromRequest(r), hash), hash, apkContentType, int64(len(data))+1))
	})

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	_, err := client.Upload(context.Background(), filePath, hash, signer, nil)
	if !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("error = %v, want ErrInvalidDescriptor", err)
	}
}

func TestUploadValidatesDescriptorType(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)

	srv, _ := newFakeServer(t, nil, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(http.StatusCreated)
		w.Write(descriptorJSON(fmt.Sprintf("%s/%s", serverURLFromRequest(r), hash), hash, "image/png", int64(len(data))))
	})

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	_, err := client.Upload(context.Background(), filePath, hash, signer, nil)
	if !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("error = %v, want ErrInvalidDescriptor", err)
	}
}

func TestUploadValidatesDescriptorURLHost(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)

	srv, _ := newFakeServer(t, nil, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(http.StatusCreated)
		w.Write(descriptorJSON("https://evil.example/"+hash, hash, apkContentType, int64(len(data))))
	})

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	_, err := client.Upload(context.Background(), filePath, hash, signer, nil)
	if !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("error = %v, want ErrInvalidDescriptor", err)
	}
}

func TestUploadExistedReflectsStatusCode(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)

	for _, tc := range []struct {
		name    string
		status  int
		existed bool
	}{
		{"newly-created", http.StatusCreated, false},
		{"already-existed", http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filePath := writeTempFile(t, data)
			srv, _ := newFakeServer(t, nil, func(w http.ResponseWriter, r *http.Request, body []byte) {
				w.WriteHeader(tc.status)
				w.Write(descriptorJSON(fmt.Sprintf("%s/%s", serverURLFromRequest(r), hash), hash, apkContentType, int64(len(data))))
			})
			client := NewClient(srv.URL)
			signer := newTestSigner(t)
			result, err := client.Upload(context.Background(), filePath, hash, signer, nil)
			if err != nil {
				t.Fatalf("Upload() error = %v", err)
			}
			if result.Existed != tc.existed {
				t.Errorf("Existed = %v, want %v", result.Existed, tc.existed)
			}
		})
	}
}

func TestUploadGenericContentType(t *testing.T) {
	data := []byte("<svg/>")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)
	const contentType = "image/svg+xml"

	var headHeader http.Header
	srv, _ := newFakeServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			headHeader = r.Header.Clone()
			w.WriteHeader(http.StatusOK)
		},
		func(w http.ResponseWriter, r *http.Request, body []byte) {
			if got := r.Header.Get("X-Content-Type"); got != contentType {
				t.Errorf("PUT X-Content-Type = %q, want %q", got, contentType)
			}
			w.WriteHeader(http.StatusCreated)
			w.Write(descriptorJSON(fmt.Sprintf("%s/%s", serverURLFromRequest(r), hash), hash, contentType, int64(len(data))))
		},
	)

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	result, err := client.Upload(context.Background(), filePath, hash, signer, nil, contentType)
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if result.Type != contentType {
		t.Errorf("Type = %q, want %q", result.Type, contentType)
	}
	if headHeader.Get("X-Content-Type") != contentType {
		t.Errorf("HEAD X-Content-Type = %q, want %q", headHeader.Get("X-Content-Type"), contentType)
	}
}

func TestUploadBytesGenericContentType(t *testing.T) {
	data := []byte("png-bytes")
	hash := sha256Hex(data)
	const contentType = "image/png"

	srv, log := newFakeServer(t, nil, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if got := r.Header.Get("Content-Type"); got != contentType {
			t.Errorf("Content-Type = %q, want %q", got, contentType)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write(descriptorJSON(fmt.Sprintf("%s/%s", serverURLFromRequest(r), hash), hash, contentType, int64(len(data))))
	})

	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	result, err := client.UploadBytes(context.Background(), data, hash, contentType, signer)
	if err != nil {
		t.Fatalf("UploadBytes() error = %v", err)
	}
	if result.Type != contentType {
		t.Errorf("Type = %q, want %q", result.Type, contentType)
	}
	if len(*log) != 2 {
		t.Fatalf("requests = %d, want 2 (HEAD then PUT)", len(*log))
	}
}

func TestUploadBytesWithAuthPreCheckedStillPerformsRequiredPreflight(t *testing.T) {
	data := []byte("bytes")
	hash := sha256Hex(data)
	contentType := "image/png"

	srv, log := newFakeServer(t, nil, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(http.StatusCreated)
		w.Write(descriptorJSON(fmt.Sprintf("%s/%s.png", serverURLFromRequest(r), hash), hash, contentType, int64(len(data))))
	})
	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	authEvent := nostrpkg.BuildBlossomAuthEvent(hash, signer.PublicKey(), time.Now().Add(AuthExpiration), client.host())
	if err := signer.Sign(context.Background(), authEvent); err != nil {
		t.Fatal(err)
	}

	result, err := client.UploadBytesWithAuthPreChecked(context.Background(), data, hash, contentType, authEvent, true)
	if err != nil {
		t.Fatalf("UploadBytesWithAuthPreChecked() error = %v", err)
	}
	if result.Existed {
		t.Error("Existed = true, want false")
	}
	if len(*log) != 2 {
		t.Fatalf("requests = %d, want 2 (HEAD then PUT)", len(*log))
	}
}

func TestUploadWithAuthRejectsWrongHash(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	wrongHash := sha256Hex([]byte("other"))
	filePath := writeTempFile(t, data)

	srv, log := newFakeServer(t, nil, nil)
	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	authEvent := nostrpkg.BuildBlossomAuthEvent(wrongHash, signer.PublicKey(), time.Now().Add(AuthExpiration), client.host())
	if err := signer.Sign(context.Background(), authEvent); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	_, err := client.UploadWithAuth(context.Background(), filePath, hash, authEvent, nil)
	if !errors.Is(err, ErrInvalidAuthEvent) {
		t.Fatalf("error = %v, want ErrInvalidAuthEvent", err)
	}
	if len(*log) != 0 {
		t.Fatalf("requests = %d, want 0 (an invalid auth event must not reach the network)", len(*log))
	}
}

func TestUploadWithAuthRejectsExpiredEvent(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)

	srv, log := newFakeServer(t, nil, nil)
	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	authEvent := nostrpkg.BuildBlossomAuthEvent(hash, signer.PublicKey(), time.Now().Add(-time.Minute), client.host())
	if err := signer.Sign(context.Background(), authEvent); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	_, err := client.UploadWithAuth(context.Background(), filePath, hash, authEvent, nil)
	if !errors.Is(err, ErrInvalidAuthEvent) {
		t.Fatalf("error = %v, want ErrInvalidAuthEvent", err)
	}
	if len(*log) != 0 {
		t.Fatalf("requests = %d, want 0", len(*log))
	}
}

func TestUploadWithAuthRejectsWrongServerScope(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)

	srv, log := newFakeServer(t, nil, nil)
	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	authEvent := nostrpkg.BuildBlossomAuthEvent(hash, signer.PublicKey(), time.Now().Add(AuthExpiration), "not-this-server.example")
	if err := signer.Sign(context.Background(), authEvent); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	_, err := client.UploadWithAuth(context.Background(), filePath, hash, authEvent, nil)
	if !errors.Is(err, ErrInvalidAuthEvent) {
		t.Fatalf("error = %v, want ErrInvalidAuthEvent", err)
	}
	if len(*log) != 0 {
		t.Fatalf("requests = %d, want 0", len(*log))
	}
}

func TestUploadWithAuthRejectsUnscopedEvent(t *testing.T) {
	data := []byte("payload")
	hash := sha256Hex(data)
	filePath := writeTempFile(t, data)

	srv, _ := newFakeServer(t, nil, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(http.StatusCreated)
		w.Write(descriptorJSON(fmt.Sprintf("%s/%s", serverURLFromRequest(r), hash), hash, apkContentType, int64(len(data))))
	})
	client := NewClient(srv.URL)
	signer := newTestSigner(t)
	authEvent := nostrpkg.BuildBlossomAuthEvent(hash, signer.PublicKey(), time.Now().Add(AuthExpiration))
	if err := signer.Sign(context.Background(), authEvent); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	if _, err := client.UploadWithAuth(context.Background(), filePath, hash, authEvent, nil); !errors.Is(err, ErrInvalidAuthEvent) {
		t.Fatalf("UploadWithAuth() error = %v, want ErrInvalidAuthEvent", err)
	}
}

// decodeAuthHeader parses the base64-encoded Nostr authorization header sent
// with an upload request.
func decodeAuthHeader(header string) (*nostr.Event, error) {
	const prefix = "Nostr "
	if !strings.HasPrefix(header, prefix) {
		return nil, fmt.Errorf("authorization header %q missing %q prefix", header, prefix)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return nil, err
	}
	var evt nostr.Event
	if err := json.Unmarshal(decoded, &evt); err != nil {
		return nil, err
	}
	return &evt, nil
}

func serverURLFromRequest(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
