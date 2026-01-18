package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// SignalHandler manages graceful shutdown and signal handling.
type SignalHandler struct {
	ctx        context.Context
	cancel     context.CancelFunc
	sigCh      chan os.Signal
	shutdownCh chan struct{}
	once       sync.Once
}

// NewSignalHandler creates a new signal handler with a cancellable context.
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
func (h *SignalHandler) Context() context.Context {
	return h.ctx
}

// ShutdownCh returns a channel that's closed when shutdown is triggered.
func (h *SignalHandler) ShutdownCh() <-chan struct{} {
	return h.shutdownCh
}

// Shutdown triggers a graceful shutdown.
func (h *SignalHandler) Shutdown() {
	h.once.Do(func() {
		close(h.shutdownCh)
		h.cancel()
	})
}

// watch monitors for signals and triggers shutdown.
func (h *SignalHandler) watch() {
	for {
		select {
		case <-h.sigCh:
			select {
			case <-h.shutdownCh:
				// Already shutting down, force exit on second signal
				fmt.Fprintln(os.Stderr, "\nForce quit")
				os.Exit(130)
			default:
				fmt.Fprintln(os.Stderr, "\nInterrupted")
				h.Shutdown()
			}
		case <-h.ctx.Done():
			// Context was cancelled elsewhere
			return
		}
	}
}

// Stop releases resources and stops watching for signals.
func (h *SignalHandler) Stop() {
	signal.Stop(h.sigCh)
	close(h.sigCh)
}

// StdinReader provides non-blocking stdin reading that respects context cancellation.
type StdinReader struct {
	lines chan string
	errs  chan error
	done  chan struct{}
}

// NewStdinReader creates a new non-blocking stdin reader.
// The goroutine reading stdin will be abandoned (not joined) when done is called,
// since Go's stdin reading cannot be interrupted.
func NewStdinReader() *StdinReader {
	r := &StdinReader{
		lines: make(chan string, 1),
		errs:  make(chan error, 1),
		done:  make(chan struct{}),
	}

	go r.readLoop()

	return r
}

// readLoop continuously reads lines from stdin.
// Note: This goroutine cannot be cleanly stopped since bufio.Reader.ReadString
// blocks and cannot be interrupted. It will be abandoned on shutdown.
func (r *StdinReader) readLoop() {
	reader := bufio.NewReader(os.Stdin)
	for {
		select {
		case <-r.done:
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			select {
			case r.errs <- err:
			case <-r.done:
				return
			}
			return
		}

		select {
		case r.lines <- line:
		case <-r.done:
			return
		}
	}
}

// ReadLine reads a line from stdin with context support.
// Returns the line (without newline), or an error if context is cancelled or EOF.
func (r *StdinReader) ReadLine(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-r.errs:
		return "", err
	case line := <-r.lines:
		// Trim the newline
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		return line, nil
	}
}

// WaitForEnter waits for the user to press Enter, with context support.
func (r *StdinReader) WaitForEnter(ctx context.Context) error {
	_, err := r.ReadLine(ctx)
	return err
}

// Close signals the reader to stop. Note that the underlying goroutine
// may not immediately exit due to blocking stdin read.
func (r *StdinReader) Close() {
	select {
	case <-r.done:
		// Already closed
	default:
		close(r.done)
	}
}

// WaitForEnterWithContext waits for Enter key or context cancellation.
// This is a convenience function that creates a temporary reader.
func WaitForEnterWithContext(ctx context.Context) error {
	reader := NewStdinReader()
	defer reader.Close()
	return reader.WaitForEnter(ctx)
}

// PromptWithContext displays a prompt and waits for input with context support.
func PromptWithContext(ctx context.Context, prompt string) (string, error) {
	fmt.Print(prompt)
	reader := NewStdinReader()
	defer reader.Close()
	return reader.ReadLine(ctx)
}
