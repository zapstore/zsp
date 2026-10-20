package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

func TestSocketPath(t *testing.T) {
	path, ok := socketPath("/var/lib/zapstore/relay/relay.sock")
	if !ok || path != "/var/lib/zapstore/relay/relay.sock" {
		t.Fatalf("path: %q %v", path, ok)
	}
	path, ok = socketPath("unix:///tmp/relay.sock")
	if !ok || path != "/tmp/relay.sock" {
		t.Fatalf("unix: %q %v", path, ok)
	}
	if _, ok := socketPath("wss://relay.zapstore.dev"); ok {
		t.Fatal("websocket treated as a socket")
	}
	if _, ok := socketPath("unix://"); ok {
		t.Fatal("empty unix path treated as a socket")
	}
}

func TestPublishAndQuerySocket(t *testing.T) {
	event := testSocketEvent()
	path := listenSocket(t, func(conn net.Conn) {
		typ, _ := readSocketMsg(conn)
		switch typ {
		case "EVENT":
			_ = writeMsg(conn, []any{"OK", event.ID, true, ""})
		case "REQ":
			_ = writeMsg(conn, []any{"EVENT", "zsp", event})
			_ = writeMsg(conn, []any{"EOSE", "zsp"})
		}
	})

	publisher := NewPublisher([]string{path})
	results := publisher.Publish(t.Context(), event)
	if len(results) != 1 || !results[0].Success || results[0].IsDuplicate || results[0].Error != nil {
		t.Fatalf("publish: %+v", results)
	}
	got, err := publisher.queryRelayMultiple(t.Context(), path, nostr.Filter{IDs: []string{event.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != event.ID {
		t.Fatalf("query: %+v", got)
	}

	unreachable, err := publisher.EnsureReachable(t.Context())
	if err != nil || len(unreachable) != 0 {
		t.Fatalf("EnsureReachable() unreachable=%v err=%v", unreachable, err)
	}
}

func TestPublishSocketDuplicateRequiresTheEvent(t *testing.T) {
	event := testSocketEvent()
	path := listenSocket(t, func(conn net.Conn) {
		typ, _ := readSocketMsg(conn)
		switch typ {
		case "EVENT":
			_ = writeMsg(conn, []any{"OK", event.ID, false, "duplicate: already exists"})
		case "REQ":
			_ = writeMsg(conn, []any{"EOSE", "zsp"})
		}
	})

	results := NewPublisher([]string{path}).Publish(t.Context(), event)
	if len(results) != 1 || results[0].Success || results[0].IsDuplicate {
		t.Fatalf("unconfirmed duplicate accepted: %+v", results)
	}
}

func testSocketEvent() *nostr.Event {
	event := &nostr.Event{CreatedAt: 1, Kind: 1, Content: "hello"}
	if err := event.Sign(nostr.GeneratePrivateKey()); err != nil {
		panic(err)
	}
	return event
}

func TestPublishSocketSkipsNoticeAndOtherOK(t *testing.T) {
	event := testSocketEvent()
	path := listenSocket(t, func(conn net.Conn) {
		readSocketMsg(conn)
		_ = writeMsg(conn, []any{"NOTICE", "slow down"})
		_ = writeMsg(conn, []any{"AUTH", "challenge"})
		_ = writeMsg(conn, []any{"OK", strings.Repeat("ee", 32), false, "other event"})
		_ = writeMsg(conn, []any{"OK", event.ID, false, "blocked: spam"})
	})

	err := publishSocket(t.Context(), path, event)
	var rejected *rejectedError
	if !errors.As(err, &rejected) || rejected.reason != "blocked: spam" {
		t.Fatalf("publishSocket() = %v, want rejection %q", err, "blocked: spam")
	}
}

func TestPublishResultMessageOmitsPrefix(t *testing.T) {
	event := testSocketEvent()
	path := listenSocket(t, func(conn net.Conn) {
		readSocketMsg(conn)
		_ = writeMsg(conn, []any{"OK", event.ID, false, "blocked: spam"})
	})
	results := NewPublisher([]string{path}).Publish(t.Context(), event)
	if results[0].Success || results[0].Message != "blocked: spam" {
		t.Fatalf("result = %+v, want rejection with Message %q", results[0], "blocked: spam")
	}
}

func TestQuerySocketDropsUnverifiedEvents(t *testing.T) {
	good := testSocketEvent()
	badSignature := testSocketEvent()
	badSignature.Sig = good.Sig
	badID := testSocketEvent()
	badID.Content = "tampered"
	other := &nostr.Event{CreatedAt: 1, Kind: 7, Content: "reaction"}
	if err := other.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatal(err)
	}
	path := listenSocket(t, func(conn net.Conn) {
		readSocketMsg(conn)
		for _, event := range []*nostr.Event{badSignature, badID, other, good} {
			_ = writeMsg(conn, []any{"EVENT", "zsp", event})
		}
		_ = writeMsg(conn, []any{"EOSE", "zsp"})
	})

	got, err := querySocket(t.Context(), path, nostr.Filter{Kinds: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != good.ID {
		t.Fatalf("querySocket() = %d events, want only the verified matching event", len(got))
	}
}

func TestSocketCancellationUnblocksWithoutDeadline(t *testing.T) {
	event := testSocketEvent()
	path := listenSocket(t, func(conn net.Conn) {
		readSocketMsg(conn)
		<-t.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)

	done := make(chan error, 1)
	go func() { done <- publishSocket(ctx, path, event) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("publishSocket() = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publishSocket did not return after cancellation")
	}
}

func TestPublishRunsRelaysConcurrentlyInRelayOrder(t *testing.T) {
	event := testSocketEvent()
	// Each relay answers only after both have received the event, so serial
	// publishing would stall until the relay timeout.
	var arrived sync.WaitGroup
	arrived.Add(2)
	answer := func(ok bool, reason string) func(net.Conn) {
		return func(conn net.Conn) {
			readSocketMsg(conn)
			arrived.Done()
			arrived.Wait()
			_ = writeMsg(conn, []any{"OK", event.ID, ok, reason})
		}
	}
	first := listenSocket(t, answer(false, "first rejects"))
	second := listenSocket(t, answer(true, ""))

	results := NewPublisher([]string{first, second}).Publish(t.Context(), event)
	if results[0].RelayURL != first || results[0].Success || results[0].Message != "first rejects" ||
		results[1].RelayURL != second || !results[1].Success {
		t.Fatalf("results out of order or wrong: %+v", results)
	}
}

func listenSocket(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	// macOS rejects Unix socket paths longer than 104 bytes, and t.TempDir is longer.
	dir, err := os.MkdirTemp("/tmp", "zs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				handle(conn)
			}(conn)
		}
	}()
	return path
}

func readSocketMsg(conn net.Conn) (string, []json.RawMessage) {
	var msg []json.RawMessage
	if err := json.NewDecoder(conn).Decode(&msg); err != nil || len(msg) == 0 {
		return "", nil
	}
	var typ string
	_ = json.Unmarshal(msg[0], &typ)
	return typ, msg
}
