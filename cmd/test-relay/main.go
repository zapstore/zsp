// Command test-relay runs an in-memory Nostr relay, Blossom server, and
// no-op indexer /suggest endpoint for local testing.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/blossom"
	"github.com/pippellia-btc/blossy"
	"github.com/pippellia-btc/rely/v2"
	"github.com/zapstore/zsp/internal/config"
)

const maxBlobSize = 64 << 20

var errUnauthorizedKey = errors.New("event pubkey is not authorized")

const defaultAuthorizedKey = "1"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("test relay stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	keys, err := authorizedKeys(envOrDefault("RELAY_AUTHORIZED_KEYS", defaultAuthorizedKey))
	if err != nil {
		return err
	}
	blossomKeys := keys
	if configured := strings.TrimSpace(config.GetEnv("RELAY_BLOSSOM_AUTHORIZED_KEYS")); configured != "" {
		blossomKeys, err = authorizedKeys(configured)
		if err != nil {
			return err
		}
	}

	relayAddress := envOrDefault("RELAY_ADDRESS", "0.0.0.0:3334")
	blossomAddress := envOrDefault("RELAY_BLOSSOM_ADDRESS", "localhost:3335")
	blossomHostname := envOrDefault("RELAY_BLOSSOM_HOSTNAME", hostname(blossomAddress))

	store := newMemoryStore()
	relay := rely.NewRelay()
	relay.Reject.Event.Append(allowEvents(keys))
	relay.On.Event = func(_ rely.Client, event *nostr.Event) rely.EventResult {
		store.saveEvent(event)
		return rely.Success()
	}
	relay.On.Req = func(_ context.Context, _ rely.Client, _ string, filters nostr.Filters) ([]nostr.Event, error) {
		return store.queryEvents(filters...), nil
	}

	blossomServer, err := newBlossomServer(blossomHostname, blossomAddress, blossomKeys, store)
	if err != nil {
		return fmt.Errorf("create blossom server: %w", err)
	}

	catalog, err := loadCatalogFixtures()
	if err != nil {
		return fmt.Errorf("load catalog fixtures: %w", err)
	}

	slog.Info("starting test relay", "relay", relayAddress, "blossom", blossomAddress, "authorized_keys", len(keys), "catalog_pubkey", catalog.pubkey)
	errs := make(chan error, 2)
	go func() { errs <- serveRelay(ctx, relay, relayAddress, store, catalog) }()
	go func() { errs <- blossomServer.StartAndServe(ctx, blossomAddress) }()

	for range 2 {
		if err := <-errs; err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return nil
}

const suggestBodyLimit = 1 << 20

// serveRelay starts the Nostr relay and serves it behind a mux that also
// accepts POST /suggest as a no-op indexer on the same host as the relay.
func serveRelay(ctx context.Context, relay *rely.Relay, address string, store *memoryStore, catalog *catalogFixtures) error {
	relay.Start(ctx)
	exit := make(chan error, 1)
	server := &http.Server{
		Addr:              address,
		Handler:           withCatalog(withTestState(withSuggest(relay, store), store), catalog),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			exit <- err
		}
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		relay.Wait()
		return err
	case err := <-exit:
		return err
	}
}

func withSuggest(next http.Handler, store *memoryStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimRight(r.URL.Path, "/") == "/suggest" {
			handleSuggest(w, r, store)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withTestState exposes minimal loopback-only state for black-box E2E tests.
func withTestState(next http.Handler, store *memoryStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_test/state" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isLoopbackRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Suggestions int      `json:"suggestions"`
			Operations  []string `json:"operations"`
		}{Suggestions: store.suggestionCount(), Operations: store.operationLog()})
	})
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	return net.ParseIP(host).IsLoopback()
}

func handleSuggest(w http.ResponseWriter, r *http.Request, store *memoryStore) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, suggestBodyLimit+1))
	if err != nil || len(body) > suggestBodyLimit {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var suggestion struct {
		Repository      string `json:"repository"`
		ReleaseSource   string `json:"release_source"`
		CertificateHash string `json:"certificate_hash"`
	}
	if err := json.Unmarshal(body, &suggestion); err != nil ||
		(suggestion.Repository == "" && suggestion.ReleaseSource == "") ||
		suggestion.CertificateHash == "" {
		http.Error(w, "repository, release_source, and certificate_hash are required", http.StatusBadRequest)
		return
	}
	if !validCertificateHash(suggestion.CertificateHash) {
		http.Error(w, "invalid certificate_hash", http.StatusBadRequest)
		return
	}
	key := suggestion.Repository + "\x00" + suggestion.ReleaseSource + "\x00" + suggestion.CertificateHash
	if store.suggestionExists(key) {
		w.WriteHeader(http.StatusOK)
		return
	}
	store.saveSuggestion(key)
	w.WriteHeader(http.StatusCreated)
}

func validCertificateHash(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func newBlossomServer(host, address string, keys map[string]struct{}, store *memoryStore) (*blossy.Server, error) {
	server, err := blossy.NewServer(blossy.WithHostname(host), blossy.WithRangeSupport())
	if err != nil {
		return nil, err
	}
	server.Reject.Upload.Append(allowBlossomUploads(keys))
	server.Reject.Delete.Append(allowBlossomDeletes(keys))
	server.On.Upload = func(request blossy.Request, hints blossy.UploadHints, data io.Reader) (blossom.BlobDescriptor, *blossom.Error) {
		content, err := io.ReadAll(io.LimitReader(data, maxBlobSize+1))
		if err != nil {
			return blossom.BlobDescriptor{}, blossom.ErrInternal("read upload: " + err.Error())
		}
		if len(content) > maxBlobSize {
			return blossom.BlobDescriptor{}, blossom.ErrTooLarge("blob exceeds 64 MiB")
		}
		hash := blossom.ComputeHash(content)
		if hints.Hash == nil || hash != *hints.Hash {
			return blossom.BlobDescriptor{}, blossom.ErrBadRequest("Content-Digest does not match upload")
		}
		typ := hints.Type
		if typ == "" {
			typ = "application/octet-stream"
		}
		store.saveBlob(hash, memoryBlob{data: content, typ: typ, owner: request.Pubkey()})
		return blossom.BlobDescriptor{
			URL:      fmt.Sprintf("http://%s/%s.%s", address, hash.Hex(), blossom.ExtFromType(typ)),
			Hash:     hash,
			Size:     int64(len(content)),
			Type:     typ,
			Uploaded: time.Now().Unix(),
		}, nil
	}
	server.On.Check = func(_ blossy.Request, hash blossom.Hash, _ string) (blossy.MetaDelivery, *blossom.Error) {
		blob, ok := store.blob(hash)
		if !ok {
			return nil, blossom.ErrNotFound("blob not found")
		}
		return blossy.Found(blob.typ, int64(len(blob.data))), nil
	}
	server.On.Download = func(_ blossy.Request, hash blossom.Hash, _ string) (blossy.BlobDelivery, *blossom.Error) {
		blob, ok := store.blob(hash)
		if !ok {
			return nil, blossom.ErrNotFound("blob not found")
		}
		return blossy.Serve(blossom.BlobFromStream(io.NopCloser(bytes.NewReader(blob.data)), int64(len(blob.data)), blob.typ)), nil
	}
	server.On.Delete = func(request blossy.Request, hash blossom.Hash) *blossom.Error {
		if !store.deleteBlob(hash, request.Pubkey()) {
			return blossom.ErrNotFound("blob not found")
		}
		return nil
	}
	return server, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(config.GetEnv(name)); value != "" {
		return value
	}
	return fallback
}

func hostname(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil && host != "" {
		return host
	}
	return "localhost"
}

func authorizedKeys(value string) (map[string]struct{}, error) {
	keys := make(map[string]struct{})
	for _, value := range strings.Split(value, ",") {
		key := strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if key == defaultAuthorizedKey {
			pubkey, err := nostr.GetPublicKey(strings.Repeat("0", 63) + key)
			if err != nil {
				return nil, fmt.Errorf("derive default authorized key: %w", err)
			}
			keys[pubkey] = struct{}{}
			continue
		}
		if !nostr.IsValidPublicKey(key) {
			return nil, fmt.Errorf("RELAY_AUTHORIZED_KEYS contains invalid public key %q", key)
		}
		keys[key] = struct{}{}
	}
	if len(keys) == 0 {
		return nil, errors.New("RELAY_AUTHORIZED_KEYS must contain at least one public key")
	}
	return keys, nil
}

func allowEvents(keys map[string]struct{}) func(rely.Client, *nostr.Event) error {
	return func(_ rely.Client, event *nostr.Event) error {
		if _, ok := keys[event.PubKey]; !ok {
			return errUnauthorizedKey
		}
		return nil
	}
}

func allowBlossomUploads(keys map[string]struct{}) func(blossy.Request, blossy.UploadHints) *blossom.Error {
	return func(request blossy.Request, _ blossy.UploadHints) *blossom.Error {
		if _, ok := keys[request.Pubkey()]; !ok {
			return blossom.ErrForbidden("public key is not authorized")
		}
		return nil
	}
}

func allowBlossomDeletes(keys map[string]struct{}) func(blossy.Request, blossom.Hash) *blossom.Error {
	return func(request blossy.Request, _ blossom.Hash) *blossom.Error {
		if _, ok := keys[request.Pubkey()]; !ok {
			return blossom.ErrForbidden("public key is not authorized")
		}
		return nil
	}
}

type memoryBlob struct {
	data  []byte
	typ   string
	owner string
}

type memoryStore struct {
	mu          sync.RWMutex
	events      map[string]nostr.Event
	blobs       map[blossom.Hash]memoryBlob
	suggestions map[string]struct{}
	operations  []string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		events:      make(map[string]nostr.Event),
		blobs:       make(map[blossom.Hash]memoryBlob),
		suggestions: make(map[string]struct{}),
	}
}

func (s *memoryStore) suggestionExists(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.suggestions[key]
	return ok
}

func (s *memoryStore) saveSuggestion(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suggestions[key] = struct{}{}
}

func (s *memoryStore) suggestionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.suggestions)
}

func (s *memoryStore) saveEvent(event *nostr.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if nostr.IsReplaceableKind(event.Kind) || nostr.IsAddressableKind(event.Kind) {
		for id, saved := range s.events {
			if replaceKey(&saved) == replaceKey(event) {
				delete(s.events, id)
			}
		}
	}
	s.events[event.ID] = *event
	s.operations = append(s.operations, fmt.Sprintf("event:%d", event.Kind))
}

func replaceKey(event *nostr.Event) string {
	dTag := ""
	if nostr.IsAddressableKind(event.Kind) {
		if tag := event.Tags.Find("d"); tag != nil {
			dTag = tag[1]
		}
	}
	return fmt.Sprintf("%d:%s:%s", event.Kind, event.PubKey, dTag)
}

func (s *memoryStore) queryEvents(filters ...nostr.Filter) []nostr.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []nostr.Event
	for _, event := range s.events {
		for _, filter := range filters {
			if filter.Matches(&event) {
				result = append(result, event)
				break
			}
		}
	}
	return result
}

func (s *memoryStore) saveBlob(hash blossom.Hash, blob memoryBlob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[hash] = blob
	s.operations = append(s.operations, "blob")
}

func (s *memoryStore) operationLog() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.operations...)
}

func (s *memoryStore) blob(hash blossom.Hash) (memoryBlob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	blob, ok := s.blobs[hash]
	return blob, ok
}

func (s *memoryStore) deleteBlob(hash blossom.Hash, owner string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, ok := s.blobs[hash]
	if !ok || blob.owner != owner {
		return false
	}
	delete(s.blobs, hash)
	return true
}
