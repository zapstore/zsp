package ui

import (
	"context"
	"errors"
	"sync"
)

// ErrInterrupted is returned when an operation is interrupted by Ctrl+C.
var ErrInterrupted = errors.New("interrupted")

var (
	globalCtx context.Context = context.Background()
	ctxMu     sync.RWMutex
)

// SetContext sets the global context used by all UI prompts.
// Call this at startup with the signal handler's context.
func SetContext(ctx context.Context) {
	ctxMu.Lock()
	defer ctxMu.Unlock()
	globalCtx = ctx
}

// GetContext returns the global context.
func GetContext() context.Context {
	ctxMu.RLock()
	defer ctxMu.RUnlock()
	return globalCtx
}

// IsInterrupted checks if the global context has been cancelled.
func IsInterrupted() bool {
	ctx := GetContext()
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// ContextError returns ErrInterrupted if the context is cancelled,
// otherwise returns the original error.
func ContextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ErrInterrupted
	}
	return err
}
