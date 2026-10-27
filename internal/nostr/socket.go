package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	"github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/v2/internal/config"
)

func socketPath(raw string) (string, bool) {
	return config.RelaySocketPath(raw)
}

func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

func writeMsg(conn net.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(raw, '\n'))
	return err
}

// closeOnDone closes conn when ctx ends so blocked reads and writes return.
// The returned function releases the watcher.
func closeOnDone(ctx context.Context, conn net.Conn) func() bool {
	return context.AfterFunc(ctx, func() { conn.Close() })
}

// socketErr prefers the context error when ctx closed the connection.
func socketErr(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func publishSocket(ctx context.Context, path string, event *nostr.Event) error {
	conn, err := dialSocket(ctx, path)
	if err != nil {
		return newOpError("connect", err)
	}
	defer conn.Close()
	stop := closeOnDone(ctx, conn)
	defer stop()

	if err := writeMsg(conn, []any{"EVENT", event}); err != nil {
		return newOpError("publish", socketErr(ctx, err))
	}
	dec := json.NewDecoder(conn)
	for {
		var msg []json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			return newOpError("publish", socketErr(ctx, err))
		}
		if len(msg) == 0 {
			continue
		}
		var typ string
		if err := json.Unmarshal(msg[0], &typ); err != nil || typ != "OK" {
			// NOTICE, AUTH and anything else are not an answer to the event.
			continue
		}
		if len(msg) < 3 {
			return newOpError("publish", errors.New("short response"))
		}
		var id string
		if err := json.Unmarshal(msg[1], &id); err != nil || id != event.ID {
			continue
		}
		var ok bool
		if err := json.Unmarshal(msg[2], &ok); err != nil {
			return newOpError("publish", errors.New("bad response"))
		}
		if ok {
			return nil
		}
		var reason string
		if len(msg) > 3 {
			_ = json.Unmarshal(msg[3], &reason)
		}
		if reason == "" {
			reason = "rejected"
		}
		return newOpError("publish", &rejectedError{reason: reason})
	}
}

// querySocket returns the events matching filter. Events that fail ID or
// signature verification, or do not match filter, are dropped.
func querySocket(ctx context.Context, path string, filter nostr.Filter) ([]*nostr.Event, error) {
	conn, err := dialSocket(ctx, path)
	if err != nil {
		return nil, newOpError("connect", err)
	}
	defer conn.Close()
	stop := closeOnDone(ctx, conn)
	defer stop()

	sub := "zsp"
	if err := writeMsg(conn, []any{"REQ", sub, filter}); err != nil {
		return nil, newOpError("query", socketErr(ctx, err))
	}
	dec := json.NewDecoder(conn)
	var out []*nostr.Event
	for {
		var msg []json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			return nil, newOpError("query", socketErr(ctx, err))
		}
		if len(msg) == 0 {
			continue
		}
		var typ string
		if err := json.Unmarshal(msg[0], &typ); err != nil {
			return nil, newOpError("query", err)
		}
		switch typ {
		case "EVENT":
			if len(msg) < 3 {
				continue
			}
			var event nostr.Event
			if err := json.Unmarshal(msg[2], &event); err != nil {
				return nil, newOpError("query", err)
			}
			if !verifiedMatch(&event, filter) {
				continue
			}
			out = append(out, &event)
		case "EOSE":
			return out, nil
		case "CLOSED", "NOTICE":
			var reason string
			if len(msg) > 1 {
				_ = json.Unmarshal(msg[len(msg)-1], &reason)
			}
			if reason == "" {
				reason = typ
			}
			return nil, newOpError("query", errors.New(reason))
		}
	}
}

func verifiedMatch(event *nostr.Event, filter nostr.Filter) bool {
	if !event.CheckID() {
		return false
	}
	if valid, err := event.CheckSignature(); err != nil || !valid {
		return false
	}
	return filter.Matches(event)
}
