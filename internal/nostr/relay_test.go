package nostr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/v2/internal/sanitize"
)

func TestIsDuplicateError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "duplicate", err: &rejectedError{reason: "duplicate: event"}, want: true},
		{name: "already exists", err: &rejectedError{reason: "event already exists"}, want: true},
		{name: "wrapped", err: newOpError("publish", &rejectedError{reason: "duplicate: have it"}), want: true},
		{name: "rejected", err: &rejectedError{reason: "blocked by relay policy"}, want: false},
		{name: "transport error mentioning duplicate", err: errors.New("duplicate connection"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isDuplicateError(test.err); got != test.want {
				t.Fatalf("isDuplicateError(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestRelayQueryErrorPreservesCancellation(t *testing.T) {
	err := relayQueryError("release", []error{fmt.Errorf("relay failed: %w", context.Canceled)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("relayQueryError() = %v, want context.Canceled", err)
	}
}

func TestRelayInfoURL(t *testing.T) {
	tests := []struct {
		relay string
		want  string
	}{
		{"wss://relay.example/path?token=secret", "https://relay.example/path"},
		{"ws://localhost:3334", "http://localhost:3334"},
	}
	for _, test := range tests {
		t.Run(test.relay, func(t *testing.T) {
			got, err := relayInfoURL(test.relay)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("relayInfoURL(%q) = %q, want %q", test.relay, got, test.want)
			}
		})
	}
}

func TestRelayRejection(t *testing.T) {
	rejected := relayRejection(errors.New("msg: blocked: spam"))
	var got *rejectedError
	if !errors.As(rejected, &got) || got.reason != "blocked: spam" {
		t.Fatalf("relayRejection(msg) = %v, want rejection with reason", rejected)
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("connection closed")} {
		if errors.As(relayRejection(err), &got) {
			t.Errorf("relayRejection(%v) became a rejection", err)
		}
	}
}

func TestOpErrorKeepsCauseAndSanitizesText(t *testing.T) {
	const secret = "supersecretvalue1234"
	sanitize.Register(secret)
	cause := fmt.Errorf("dial %s: %w", secret, context.DeadlineExceeded)
	err := newOpError("connect", cause)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(DeadlineExceeded) = false for %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Error() leaked a registered secret: %q", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "connect: ") {
		t.Fatalf("Error() = %q, want connect prefix", err.Error())
	}
}

func TestErrorReasonOmitsOperationPrefix(t *testing.T) {
	err := newOpError("publish", &rejectedError{reason: "blocked: spam"})
	if got := errorReason(err); got != "blocked: spam" {
		t.Fatalf("errorReason() = %q, want %q", got, "blocked: spam")
	}
}

func signedProfile(t *testing.T, privateKey string, createdAt int64, name string) *nostr.Event {
	t.Helper()
	event := &nostr.Event{Kind: 0, CreatedAt: nostr.Timestamp(createdAt), Content: `{"name":"` + name + `"}`}
	if err := event.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	return event
}

func serveEvents(events ...*nostr.Event) func(net.Conn) {
	return func(conn net.Conn) {
		readSocketMsg(conn)
		for _, event := range events {
			_ = writeMsg(conn, []any{"EVENT", "zsp", event})
		}
		_ = writeMsg(conn, []any{"EOSE", "zsp"})
	}
}

func deadSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "missing.sock")
}

func TestDeveloperProfileNewestWinsAcrossParallelRelays(t *testing.T) {
	privateKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	older := signedProfile(t, privateKey, 100, "old")
	newer := signedProfile(t, privateKey, 200, "new")
	// Each relay answers only after both have the query, so serial queries
	// would stall until the profile timeout.
	var arrived sync.WaitGroup
	arrived.Add(2)
	answer := func(event *nostr.Event) func(net.Conn) {
		return func(conn net.Conn) {
			readSocketMsg(conn)
			arrived.Done()
			arrived.Wait()
			_ = writeMsg(conn, []any{"EVENT", "zsp", event})
			_ = writeMsg(conn, []any{"EOSE", "zsp"})
		}
	}
	first := listenSocket(t, answer(newer))
	second := listenSocket(t, answer(older))

	got, warnings, err := NewPublisher([]string{first, second}).DeveloperProfile(t.Context(), pubkey, nil)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("DeveloperProfile() warnings=%v err=%v", warnings, err)
	}
	if got == nil || got.ID != newer.ID {
		t.Fatalf("DeveloperProfile() = %+v, want the newest kind 0 %s", got, newer.ID)
	}
}

func TestDeveloperProfileIgnoresOtherAuthors(t *testing.T) {
	privateKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	other := signedProfile(t, nostr.GeneratePrivateKey(), 100, "other")
	relay := listenSocket(t, serveEvents(other))

	got, _, err := NewPublisher([]string{relay}).DeveloperProfile(t.Context(), pubkey, nil)
	if err != nil || got != nil {
		t.Fatalf("DeveloperProfile() = %+v, %v, want no profile", got, err)
	}
}

func TestDeveloperProfileAllRelaysFail(t *testing.T) {
	pubkey, err := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	got, warnings, err := NewPublisher([]string{deadSocket(t), deadSocket(t)}).DeveloperProfile(t.Context(), pubkey, nil)
	if err == nil || got != nil || len(warnings) != 2 {
		t.Fatalf("DeveloperProfile() = %+v, warnings=%d, err=%v, want an error after 2 failed relays", got, len(warnings), err)
	}
}

func TestDeveloperProfileOneRelayFailsIsAWarning(t *testing.T) {
	privateKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	relay := listenSocket(t, serveEvents())
	got, warnings, err := NewPublisher([]string{deadSocket(t), relay}).DeveloperProfile(t.Context(), pubkey, nil)
	if err != nil || got != nil || len(warnings) != 1 {
		t.Fatalf("DeveloperProfile() = %+v, warnings=%d, err=%v, want no profile and one warning", got, len(warnings), err)
	}
}

func TestDeveloperProfileSkipsUnreachableRelays(t *testing.T) {
	privateKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	event := signedProfile(t, privateKey, 100, "dev")
	dead := deadSocket(t)
	relay := listenSocket(t, serveEvents(event))

	got, warnings, err := NewPublisher([]string{dead, relay}).DeveloperProfile(t.Context(), pubkey, []string{dead})
	if err != nil || len(warnings) != 0 || got == nil || got.ID != event.ID {
		t.Fatalf("DeveloperProfile() = %+v, warnings=%v, err=%v, want the profile without querying %s", got, warnings, err, dead)
	}
}

func TestProfileRelaysAddPublicRelaysOnlyForDefaultRelays(t *testing.T) {
	custom := "wss://relay.example.test"
	tests := []struct {
		name        string
		relays      []string
		unreachable []string
		public      bool
		want        []string
	}{
		{"default", nil, nil, true, append([]string{DefaultRelay}, DefaultProfileRelays...)},
		{"default with trailing slash", []string{DefaultRelay + "/"}, nil, true, append([]string{DefaultRelay + "/"}, DefaultProfileRelays...)},
		{"custom relay", []string{custom}, nil, false, []string{custom}},
		{"default and custom", []string{DefaultRelay, custom}, nil, false, []string{DefaultRelay, custom}},
		{"local socket", []string{"/tmp/relay.sock"}, nil, false, []string{"/tmp/relay.sock"}},
		{"local websocket", []string{"ws://localhost:3334"}, nil, false, []string{"ws://localhost:3334"}},
		{"default with unreachable public relay", nil, []string{DefaultProfileRelays[0]}, true, append([]string{DefaultRelay}, DefaultProfileRelays[1:]...)},
		{"custom with unreachable relay", []string{custom, "wss://b.example.test"}, []string{"wss://b.example.test"}, false, []string{custom}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NewPublisher(test.relays).profileRelays(test.unreachable)
			slices.Sort(got)
			want := slices.Clone(test.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("profileRelays(%v) on %v = %v, want %v", test.unreachable, test.relays, got, want)
			}
			for _, public := range DefaultProfileRelays {
				if slices.Contains(got, public) && !test.public {
					t.Errorf("profileRelays() on %v includes public relay %s", test.relays, public)
				}
			}
		})
	}
}
