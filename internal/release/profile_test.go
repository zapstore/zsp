package release

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	internalnostr "github.com/zapstore/zsp/v2/internal/nostr"
)

// serveRelay answers each REQ on a Unix socket with the stored events that
// match its filter.
func serveRelay(t *testing.T, events ...*gonostr.Event) string {
	t.Helper()
	// macOS rejects Unix socket paths longer than 104 bytes, and t.TempDir is longer.
	dir, err := os.MkdirTemp("/tmp", "zr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				var message []json.RawMessage
				if json.NewDecoder(conn).Decode(&message) != nil || len(message) < 3 {
					return
				}
				var filter gonostr.Filter
				if json.Unmarshal(message[2], &filter) != nil {
					return
				}
				encoder := json.NewEncoder(conn)
				for _, event := range events {
					if filter.Matches(event) {
						_ = encoder.Encode([]any{"EVENT", "zsp", event})
					}
				}
				_ = encoder.Encode([]any{"EOSE", "zsp"})
			}(conn)
		}
	}()
	return path
}

func signedEvent(t *testing.T, privateKey string, kind int, tags gonostr.Tags) *gonostr.Event {
	t.Helper()
	event := &gonostr.Event{Kind: kind, CreatedAt: 100, Tags: tags, Content: "{}"}
	if err := event.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestRequireProfileForNewListing(t *testing.T) {
	const appID = "com.example.app"
	signerKey := gonostr.GeneratePrivateKey()
	signer, err := gonostr.GetPublicKey(signerKey)
	if err != nil {
		t.Fatal(err)
	}
	profile := signedEvent(t, signerKey, 0, nil)
	otherApp := signedEvent(t, gonostr.GeneratePrivateKey(), internalnostr.KindAppMetadata, gonostr.Tags{{"d", appID}})
	ownApp := signedEvent(t, signerKey, internalnostr.KindAppMetadata, gonostr.Tags{{"d", appID}})

	tests := []struct {
		name         string
		events       []*gonostr.Event
		skipAppEvent bool
		wantErr      error
	}{
		{"new listing without profile", nil, false, ErrProfileRequired},
		{"new listing with profile", []*gonostr.Event{profile}, false, nil},
		{"existing listing without profile", []*gonostr.Event{ownApp}, false, nil},
		{"existing listing of another key without profile", []*gonostr.Event{otherApp}, false, nil},
		{"join existing app without profile", []*gonostr.Event{otherApp}, true, nil},
		{"app event skipped without profile", nil, true, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := internalnostr.NewPublisher([]string{serveRelay(t, test.events...)})
			_, err := requireProfileForNewListing(t.Context(), publisher, appID, signer, test.skipAppEvent, nil)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("requireProfileForNewListing() = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestRequireProfileForNewListingJoinedAuthorSkipsAppEvent(t *testing.T) {
	const appID = "com.example.app"
	signerKey := gonostr.GeneratePrivateKey()
	signer, err := gonostr.GetPublicKey(signerKey)
	if err != nil {
		t.Fatal(err)
	}
	developerApp := signedEvent(t, gonostr.GeneratePrivateKey(), internalnostr.KindAppMetadata, gonostr.Tags{{"d", appID}})
	joined := otherApplicationAuthor([]*gonostr.Event{developerApp}, appID, signer)
	publisher := internalnostr.NewPublisher([]string{serveRelay(t)})

	if _, err := requireProfileForNewListing(t.Context(), publisher, appID, signer, joined != "", nil); err != nil {
		t.Fatalf("requireProfileForNewListing() = %v, want nil when joining %q", err, joined)
	}
}

func TestRequireProfileForNewListingReportsProfileRelayWarnings(t *testing.T) {
	const appID = "com.example.app"
	signerKey := gonostr.GeneratePrivateKey()
	signer, err := gonostr.GetPublicKey(signerKey)
	if err != nil {
		t.Fatal(err)
	}
	good := serveRelay(t, signedEvent(t, signerKey, 0, nil))
	dead := filepath.Join(t.TempDir(), "missing.sock")
	publisher := internalnostr.NewPublisher([]string{good, dead})

	warnings, err := requireProfileForNewListing(t.Context(), publisher, appID, signer, false, nil)
	if err != nil {
		t.Fatalf("requireProfileForNewListing() = %v, want nil", err)
	}
	if !slices.Contains(warnings, "profile query failed on one relay") {
		t.Fatalf("warnings = %q, want the profile relay failure", warnings)
	}
	for _, warning := range warnings {
		if strings.Contains(warning, dead) {
			t.Fatalf("warning %q leaks the relay address", warning)
		}
	}
}

func TestRequireProfileForNewListingFailsWhenNoRelayAnswers(t *testing.T) {
	signer, err := gonostr.GetPublicKey(gonostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	publisher := internalnostr.NewPublisher([]string{filepath.Join(t.TempDir(), "missing.sock")})

	_, err = requireProfileForNewListing(t.Context(), publisher, "com.example.app", signer, false, nil)
	if err == nil || errors.Is(err, ErrProfileRequired) {
		t.Fatalf("requireProfileForNewListing() = %v, want a relay query error, not ErrProfileRequired", err)
	}
}
