package nostr

import (
	"context"
	"errors"
	"strings"

	"github.com/zapstore/zsp/v2/internal/sanitize"
)

// ErrIncompleteResults means a relay returned a full result page, so matching
// events may exist beyond it.
var ErrIncompleteResults = errors.New("relay results may be incomplete")

// opError describes a failed operation. Error sanitizes the cause so secrets
// never escape, while Unwrap keeps it for errors.Is and errors.As. op must
// already be safe to show.
type opError struct {
	op  string
	err error
}

func newOpError(op string, err error) error {
	return &opError{op: op, err: err}
}

func (e *opError) Error() string {
	return e.op + ": " + sanitize.Text(e.err.Error())
}

func (e *opError) Unwrap() error { return e.err }

// rejectedError is a relay's OK=false answer to a published event.
type rejectedError struct {
	reason string
}

func (e *rejectedError) Error() string { return e.reason }

// relayRejection converts go-nostr's publish error into a rejectedError.
// go-nostr formats an OK=false reason as "msg: <reason>" without a typed
// error, so the prefix is the only way to tell a rejection from a transport
// failure.
func relayRejection(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if reason, ok := strings.CutPrefix(err.Error(), "msg: "); ok {
		return &rejectedError{reason: reason}
	}
	return err
}

// isDuplicateError reports whether a relay rejected an event because it
// already has it. NIP-01 only recommends a "duplicate:" reason prefix, so the
// wording is advisory: callers confirm the event exists before trusting it.
func isDuplicateError(err error) bool {
	var rejected *rejectedError
	if !errors.As(err, &rejected) {
		return false
	}
	reason := strings.ToLower(rejected.reason)
	return strings.Contains(reason, "duplicate") || strings.Contains(reason, "already exists")
}

// IsRateLimited reports whether a relay rejected an event with the NIP-01
// "rate-limited:" machine-readable prefix.
func IsRateLimited(err error) bool {
	var rejected *rejectedError
	return errors.As(err, &rejected) && strings.HasPrefix(strings.TrimSpace(rejected.reason), "rate-limited:")
}

// errorReason returns the sanitized relay reason carried by err, without any
// operation prefix.
func errorReason(err error) string {
	var rejected *rejectedError
	if errors.As(err, &rejected) {
		return sanitize.Text(rejected.reason)
	}
	var op *opError
	if errors.As(err, &op) {
		return sanitize.Text(op.err.Error())
	}
	return sanitize.Text(err.Error())
}
