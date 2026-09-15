package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/blossom"
)

const testPubkey = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

func TestAuthorizedKeys(t *testing.T) {
	keys, err := authorizedKeys(" " + testPubkey + ", " + testPubkey + " ")
	if err != nil {
		t.Fatalf("authorizedKeys() error = %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("authorizedKeys() count = %d, want 1", len(keys))
	}

	if _, err := authorizedKeys("not-a-key"); err == nil {
		t.Fatal("authorizedKeys() error = nil, want invalid-key error")
	}
	keys, err = authorizedKeys(defaultAuthorizedKey)
	if err != nil {
		t.Fatalf("authorizedKeys(default) error = %v", err)
	}
	if _, ok := keys[testPubkey]; !ok {
		t.Fatalf("authorizedKeys(default) = %v, want public key for private key 1", keys)
	}
}

func TestMemoryStoreReplacesAddressableEvent(t *testing.T) {
	store := newMemoryStore()
	first := &nostr.Event{ID: "first", Kind: 30_023, PubKey: testPubkey, Tags: nostr.Tags{{"d", "com.example.app"}}}
	second := &nostr.Event{ID: "second", Kind: 30_023, PubKey: testPubkey, Tags: nostr.Tags{{"d", "com.example.app"}}}

	store.saveEvent(first)
	store.saveEvent(second)

	got := store.queryEvents(nostr.Filter{Kinds: []int{30_023}})
	if len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("queryEvents() = %#v, want only replacement %q", got, second.ID)
	}
}

func TestHandleSuggest_Created(t *testing.T) {
	store := newMemoryStore()
	certificateHash := strings.Repeat("a", 64)
	body := `{"repository":"https://github.com/example/app","certificate_hash":"` + certificateHash + `"}`
	req := httptest.NewRequest(http.MethodPost, "/suggest", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handleSuggest(rec, req, store)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
}

func TestHandleSuggest_AcceptsUnsignedSuggestion(t *testing.T) {
	store := newMemoryStore()
	certificateHash := strings.Repeat("b", 64)
	body := `{"repository":"https://github.com/example/app","certificate_hash":"` + certificateHash + `"}`

	req := httptest.NewRequest(http.MethodPost, "/suggest", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleSuggest(rec, req, store)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
}

func TestHandleSuggest_Existing(t *testing.T) {
	store := newMemoryStore()
	certificateHash := strings.Repeat("c", 64)
	body := `{"repository":"https://github.com/example/app","certificate_hash":"` + certificateHash + `"}`

	first := httptest.NewRequest(http.MethodPost, "/suggest", strings.NewReader(body))
	firstRecorder := httptest.NewRecorder()
	handleSuggest(firstRecorder, first, store)
	if firstRecorder.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want %d", firstRecorder.Code, http.StatusCreated)
	}

	second := httptest.NewRequest(http.MethodPost, "/suggest", strings.NewReader(body))
	secondRecorder := httptest.NewRecorder()
	handleSuggest(secondRecorder, second, store)
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", secondRecorder.Code, http.StatusOK)
	}
}

func TestHandleSuggest_RejectsNonPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/suggest", nil)
	rec := httptest.NewRecorder()

	handleSuggest(rec, req, newMemoryStore())

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestWithSuggest_HandlesSuggestWithoutNext(t *testing.T) {
	store := newMemoryStore()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler must not run for /suggest")
	})
	req := httptest.NewRequest(http.MethodPost, "/suggest", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	withSuggest(next, store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestWithSuggest_RoutesOtherPathsToNext(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	withSuggest(next, newMemoryStore()).ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler was not called")
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
}

func TestWithTestState_ExposesSuggestionCountOnlyOnLoopback(t *testing.T) {
	store := newMemoryStore()
	store.saveSuggestion("one")
	handler := withTestState(http.NotFoundHandler(), store)

	loopback := httptest.NewRequest(http.MethodGet, "/_test/state", nil)
	loopback.RemoteAddr = "127.0.0.1:1234"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, loopback)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"suggestions":1`) {
		t.Fatalf("loopback response = %d %s", recorder.Code, recorder.Body.String())
	}

	remote := httptest.NewRequest(http.MethodGet, "/_test/state", nil)
	remote.RemoteAddr = "192.0.2.1:1234"
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, remote)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("remote response = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestMemoryStoreBlobOwnership(t *testing.T) {
	store := newMemoryStore()
	hash := blossom.ComputeHash([]byte("test blob"))
	store.saveBlob(hash, memoryBlob{data: []byte("test blob"), owner: testPubkey})

	if store.deleteBlob(hash, "other") {
		t.Fatal("deleteBlob() succeeded for another owner")
	}
	if _, ok := store.blob(hash); !ok {
		t.Fatal("blob was deleted by another owner")
	}
	if !store.deleteBlob(hash, testPubkey) {
		t.Fatal("deleteBlob() failed for owner")
	}
}
