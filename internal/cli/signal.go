// Package cli handles command-line interface concerns.
package cli

import (
	"context"
	"fmt"
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
	once       sync.Once

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

// initiateShutdown begins the shutdown process with a message.
func (h *SignalHandler) initiateShutdown(message string) {
	h.once.Do(func() {
		fmt.Fprintln(os.Stderr, "\n"+message)
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
				fmt.Fprintln(os.Stderr, "\nForce quit")
				os.Exit(130)
			default:
				// First signal - initiate graceful shutdown
				h.initiateShutdown("Interrupted")

				// Start timeout for force exit
				go func() {
					select {
					case <-time.After(GracefulShutdownTimeout):
						fmt.Fprintln(os.Stderr, "\nShutdown timeout, forcing exit")
						os.Exit(130)
					case <-h.ctx.Done():
						// Context was cancelled elsewhere, normal exit path
					}
				}()
			}
		case <-h.ctx.Done():
			// Context was cancelled elsewhere (e.g., normal program completion)
			return
		}
	}
}

// Stop releases resources and stops watching for signals.
// Call this in a defer after NewSignalHandler.
func (h *SignalHandler) Stop() {
	signal.Stop(h.sigCh)
	close(h.sigCh)
}

// --- Non-blocking stdin utilities ---

// readLineResult holds the result of a non-blocking line read.
type readLineResult struct {
	line string
	err  error
}

// readLineAsync reads a line from stdin in a goroutine using raw os.Stdin.Read().
// Does NOT use bufio to avoid buffering conflicts with other stdin readers.
// The goroutine is abandoned if context is cancelled (Go stdin reads cannot be interrupted).
func readLineAsync() <-chan readLineResult {
	ch := make(chan readLineResult, 1)
	go func() {
		var line []byte
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				ch <- readLineResult{err: err}
				return
			}
			if n > 0 {
				if buf[0] == '\n' {
					ch <- readLineResult{line: string(line)}
					return
				}
				if buf[0] != '\r' { // Skip carriage return
					line = append(line, buf[0])
				}
			}
		}
	}()
	return ch
}

// WaitForEnter waits for the user to press Enter, with context support.
// Returns context.Canceled if the context is cancelled.
func WaitForEnter(ctx context.Context) error {
	resultCh := readLineAsync()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-resultCh:
		return result.err
	}
}

// WaitForEnterWithContext is an alias for WaitForEnter for backwards compatibility.
func WaitForEnterWithContext(ctx context.Context) error {
	return WaitForEnter(ctx)
}
