package nostr

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestSocketPath(t *testing.T) {
	path, ok := socketPath("/var/lib/zapstore/relay/data/nostr.sock")
	if !ok || path != "/var/lib/zapstore/relay/data/nostr.sock" {
		t.Fatalf("path: %q %v", path, ok)
	}
	path, ok = socketPath("unix:///tmp/nostr.sock")
	if !ok || path != "/tmp/nostr.sock" {
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
	return &nostr.Event{
		ID:        strings.Repeat("ab", 32),
		PubKey:    strings.Repeat("cd", 32),
		CreatedAt: 1,
		Kind:      1,
		Content:   "hello",
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
