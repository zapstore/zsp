package nostr

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsDuplicateError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "duplicate", err: errors.New("duplicate event"), want: true},
		{name: "already exists", err: errors.New("event already exists"), want: true},
		{name: "rejected", err: errors.New("blocked by relay policy"), want: false},
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
