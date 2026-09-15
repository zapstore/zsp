package blossom

import (
	"errors"
	"fmt"
)

// Sentinel errors identify well-known Blossom upload failures so callers can
// branch with errors.Is. Wrapped context (status codes, hashes, hosts) is
// safe to display; it never includes authorization tokens or file contents.
var (
	// ErrInvalidAuthEvent means a BUD-11 kind 24242 authorization event does
	// not authorize the requested upload: wrong kind, missing or mismatched
	// t/x tags, an expiration that has already passed, or server tags that
	// do not name this server.
	ErrInvalidAuthEvent = errors.New("blossom: invalid authorization event")

	// ErrPreflightRejected means the server's HEAD /upload response
	// (BUD-06) did not accept the declared upload.
	ErrPreflightRejected = errors.New("blossom: upload preflight rejected")

	// ErrUploadRejected means the server's PUT /upload response (BUD-02)
	// rejected the upload.
	ErrUploadRejected = errors.New("blossom: upload rejected")

	// ErrInvalidDescriptor means the server accepted the upload but
	// returned a blob descriptor that does not exactly match the uploaded
	// file's hash, size, type, or URL.
	ErrInvalidDescriptor = errors.New("blossom: invalid blob descriptor")
)

// StatusError pairs a sentinel error with the HTTP status code and optional
// server-supplied reason (the BUD-06/BUD-02 "X-Reason" or "X-Upload-Message"
// header, or a short response body) that produced it.
type StatusError struct {
	Err        error
	StatusCode int
	Reason     string
}

func (e *StatusError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s: status %d: %s", e.Err, e.StatusCode, e.Reason)
	}
	return fmt.Sprintf("%s: status %d", e.Err, e.StatusCode)
}

func (e *StatusError) Unwrap() error { return e.Err }
