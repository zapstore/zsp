//go:build unix

package cli

import (
	"context"
	"errors"
	"syscall"
	"testing"
)

func TestSignalHandlerFirstSignalCancelsContext(t *testing.T) {
	handler := NewSignalHandler()
	handler.SetQuiet(true)
	defer handler.Stop()

	if err := handler.Context().Err(); err != nil {
		t.Fatalf("Context().Err() before signal = %v, want nil", err)
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	<-handler.Context().Done()
	if err := handler.Context().Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Context().Err() after signal = %v, want context.Canceled", err)
	}
}

func TestSignalHandlerStopIsIdempotent(t *testing.T) {
	handler := NewSignalHandler()
	handler.Stop()
	handler.Stop()
	if err := handler.Context().Err(); err == nil {
		t.Fatal("Context().Err() after Stop = nil, want cancelled")
	}
}
