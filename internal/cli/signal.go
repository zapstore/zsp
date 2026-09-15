// Package cli handles command-line interface concerns.
package cli

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// GracefulShutdownTimeout is the time allowed for graceful shutdown before force exit.
const GracefulShutdownTimeout = 3 * time.Second

// SignalHandler manages graceful shutdown and signal handling.
// It provides a context that is cancelled on first Ctrl+C, and forces
// exit on second Ctrl+C or after the graceful shutdown timeout.
type SignalHandler struct {
	ctx        context.Context
	cancel     context.CancelFunc
	sigCh      chan os.Signal
	shutdownCh chan struct{}
	doneCh     chan struct{}
	once       sync.Once
	stopOnce   sync.Once

	// Cleanup functions to run on shutdown
	cleanupMu sync.Mutex
	cleanups  []func()
}

// NewSignalHandler creates a new signal handler with a cancellable context.
// The handler:
//   - First Ctrl+C: cancels context, starts graceful shutdown
//   - Second Ctrl+C: forces immediate exit with code 130
//   - After GracefulShutdownTimeout: forces exit if still running
func NewSignalHandler() *SignalHandler {
	ctx, cancel := context.WithCancel(context.Background())

	h := &SignalHandler{
		ctx:        ctx,
		cancel:     cancel,
		sigCh:      make(chan os.Signal, 1),
		shutdownCh: make(chan struct{}),
		doneCh:     make(chan struct{}),
	}

	signal.Notify(h.sigCh, syscall.SIGINT, syscall.SIGTERM)

	go h.watch()

	return h
}

// Context returns the handler's context, which is cancelled on shutdown.
// Pass this context to all operations that should be cancellable.
func (h *SignalHandler) Context() context.Context {
	return h.ctx
}

// Done returns a channel that's closed when shutdown is triggered.
func (h *SignalHandler) Done() <-chan struct{} {
	return h.shutdownCh
}

// IsShuttingDown returns true if shutdown has been triggered.
func (h *SignalHandler) IsShuttingDown() bool {
	select {
	case <-h.shutdownCh:
		return true
	default:
		return false
	}
}

// OnCleanup registers a function to be called during graceful shutdown.
// Cleanup functions are called in reverse order of registration (LIFO).
func (h *SignalHandler) OnCleanup(fn func()) {
	h.cleanupMu.Lock()
	defer h.cleanupMu.Unlock()
	h.cleanups = append(h.cleanups, fn)
}

// Shutdown triggers a graceful shutdown programmatically.
func (h *SignalHandler) Shutdown() {
	h.initiateShutdown("Shutting down...")
}

// initiateShutdown begins the shutdown process. Command owners emit their
// final output so operational commands can preserve one-document JSON.
func (h *SignalHandler) initiateShutdown(message string) {
	h.once.Do(func() {
		_ = message
		close(h.shutdownCh)
		h.cancel()
		h.runCleanups()
	})
}

// runCleanups executes registered cleanup functions in reverse order.
func (h *SignalHandler) runCleanups() {
	h.cleanupMu.Lock()
	cleanups := make([]func(), len(h.cleanups))
	copy(cleanups, h.cleanups)
	h.cleanupMu.Unlock()

	// Run in reverse order (LIFO)
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// watch monitors for signals and triggers shutdown.
func (h *SignalHandler) watch() {
	for {
		select {
		case sig := <-h.sigCh:
			if sig == nil {
				// Channel closed, stop watching
				return
			}
			select {
			case <-h.shutdownCh:
				// Already shutting down, force exit on second signal
				os.Exit(130)
			default:
				// First signal - initiate graceful shutdown
				h.initiateShutdown("Interrupted")

				// Start timeout for force exit
				go func() {
					select {
					case <-time.After(GracefulShutdownTimeout):
						os.Exit(130)
					case <-h.doneCh:
						// Command completed normally.
					}
				}()
			}
		case <-h.doneCh:
			return
		}
	}
}

// Stop releases resources and stops watching for signals.
// Call this in a defer after NewSignalHandler.
func (h *SignalHandler) Stop() {
	h.stopOnce.Do(func() {
		signal.Stop(h.sigCh)
		close(h.doneCh)
	})
}
