package nostr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

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
