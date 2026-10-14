// Package cli handles command-line interface concerns.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
)

// forceExitCode is the exit status of a forced stop, the shell convention for
// a process ended by SIGINT.
const forceExitCode = 130

// SignalHandler turns the first interrupt into context cancellation, so
// deferred cleanup runs, and the second into an immediate exit.
type SignalHandler struct {
	ctx      context.Context
	cancel   context.CancelFunc
	sigCh    chan os.Signal
	doneCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	quiet    atomic.Bool
}

// NewSignalHandler creates a signal handler with a cancellable context.
//   - First SIGINT or SIGTERM: cancels the context and prints a hint to stderr
//   - Second signal: exits immediately with code 130, skipping deferred cleanup
//
// Call Stop when the command finishes.
func NewSignalHandler() *SignalHandler {
	ctx, cancel := context.WithCancel(context.Background())

	h := &SignalHandler{
		ctx:    ctx,
		cancel: cancel,
		sigCh:  make(chan os.Signal, 2),
		doneCh: make(chan struct{}),
	}
	signal.Notify(h.sigCh, syscall.SIGINT, syscall.SIGTERM)

	h.wg.Add(1)
	go h.watch()

	return h
}

// Context returns the handler's context, which is cancelled on the first signal.
// Pass this context to all operations that should be cancellable.
func (h *SignalHandler) Context() context.Context {
	return h.ctx
}

// SetQuiet suppresses the stderr hint, for output modes where stderr carries
// a machine-readable document.
func (h *SignalHandler) SetQuiet(quiet bool) {
	h.quiet.Store(quiet)
}

func (h *SignalHandler) watch() {
	defer h.wg.Done()
	select {
	case <-h.sigCh:
	case <-h.doneCh:
		return
	}
	h.cancel()
	if !h.quiet.Load() {
		fmt.Fprintln(os.Stderr, "stopping… press Ctrl-C again to force")
	}
	select {
	case <-h.sigCh:
		os.Exit(forceExitCode)
	case <-h.doneCh:
	}
}

// Stop stops watching for signals, releases the context, and waits for the
// watcher to exit. Call it in a defer after NewSignalHandler.
func (h *SignalHandler) Stop() {
	h.stopOnce.Do(func() {
		signal.Stop(h.sigCh)
		close(h.doneCh)
		h.cancel()
		h.wg.Wait()
	})
}
