package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/config"
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

func publishSocket(ctx context.Context, path string, event *nostr.Event) error {
	conn, err := dialSocket(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := writeMsg(conn, []any{"EVENT", event}); err != nil {
		return fmt.Errorf("failed to publish: %w", err)
	}
	var msg []json.RawMessage
	if err := json.NewDecoder(conn).Decode(&msg); err != nil {
		return fmt.Errorf("failed to publish: %w", err)
	}
	if len(msg) < 3 {
		return fmt.Errorf("failed to publish: short response")
	}
	var typ, id string
	var ok bool
	_ = json.Unmarshal(msg[0], &typ)
	_ = json.Unmarshal(msg[1], &id)
	if err := json.Unmarshal(msg[2], &ok); err != nil || typ != "OK" || id != event.ID {
		return fmt.Errorf("failed to publish: bad response")
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
	return fmt.Errorf("failed to publish: %s", reason)
}

func querySocket(ctx context.Context, path string, filter nostr.Filter) ([]*nostr.Event, error) {
	conn, err := dialSocket(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	sub := "zsp"
	if err := writeMsg(conn, []any{"REQ", sub, filter}); err != nil {
		return nil, fmt.Errorf("failed to query: %w", err)
	}
	dec := json.NewDecoder(conn)
	var out []*nostr.Event
	for {
		var msg []json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			return nil, fmt.Errorf("failed to query: %w", err)
		}
		if len(msg) == 0 {
			continue
		}
		var typ string
		if err := json.Unmarshal(msg[0], &typ); err != nil {
			return nil, fmt.Errorf("failed to query: %w", err)
		}
		switch typ {
		case "EVENT":
			if len(msg) < 3 {
				continue
			}
			var event nostr.Event
			if err := json.Unmarshal(msg[2], &event); err != nil {
				return nil, fmt.Errorf("failed to query: %w", err)
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
			return nil, fmt.Errorf("failed to query: %s", reason)
		}
	}
}
