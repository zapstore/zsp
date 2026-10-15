//go:build release && !race

package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// screen collects terminal output and wakes waiters when more arrives.
type screen struct {
	mu   sync.Mutex
	raw  bytes.Buffer
	wake chan struct{}
}

func newScreen() *screen {
	return &screen{wake: make(chan struct{}, 1)}
}

func (s *screen) Write(p []byte) (int, error) {
	s.add(p)
	return len(p), nil
}

func (s *screen) add(p []byte) {
	s.mu.Lock()
	s.raw.Write(p)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *screen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return stripANSI(s.raw.String())
}

func (s *screen) wait(ctx context.Context, needle string) error {
	got, err := s.waitAny(ctx, needle)
	if err != nil {
		return err
	}
	if got != needle {
		return fmt.Errorf("wait for %q saw %q", needle, got)
	}
	return nil
}

func (s *screen) waitAny(ctx context.Context, needles ...string) (string, error) {
	for {
		text := s.text()
		for _, needle := range needles {
			if strings.Contains(text, needle) {
				return needle, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wait for %q: %w\n%s", strings.Join(needles, " or "), ctx.Err(), tail(text))
		case <-s.wake:
		}
	}
}

// waitMore waits until needle appears again after seen earlier copies.
func (s *screen) waitMore(ctx context.Context, needle string, seen int) error {
	for {
		if strings.Count(s.text(), needle) > seen {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for another %q: %w\n%s", needle, ctx.Err(), tail(s.text()))
		case <-s.wake:
		}
	}
}

// console is one process whose stdin and stdout are a terminal.
type console struct {
	cmd    *exec.Cmd
	master *os.File
	screen *screen
	once   sync.Once
}

func startConsole(cmd *exec.Cmd) (*console, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, err
	}
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		slave.Close()
		master.Close()
		return nil, err
	}
	// The child holds the slave. Closing it here makes the master read EOF
	// when that child exits.
	slave.Close()
	c := &console{cmd: cmd, master: master, screen: newScreen()}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := master.Read(buf)
			if n > 0 {
				c.screen.add(buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()
	return c, nil
}

func (c *console) send(s string) error {
	_, err := c.master.Write([]byte(s))
	return err
}

func (c *console) wait(ctx context.Context, needle string) error {
	return c.screen.wait(ctx, needle)
}

func (c *console) waitMore(ctx context.Context, needle string, seen int) error {
	return c.screen.waitMore(ctx, needle, seen)
}

func (c *console) waitAny(ctx context.Context, needles ...string) (string, error) {
	return c.screen.waitAny(ctx, needles...)
}

func (c *console) text() string { return c.screen.text() }

// stop interrupts the process. finish waits for an exit the process takes on
// its own. Both are safe to call, and only the first waits.
func (c *console) stop(ctx context.Context) error {
	return c.finish(ctx, true)
}

func (c *console) finish(ctx context.Context, interrupt bool) error {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	var err error
	c.once.Do(func() {
		if interrupt {
			_ = c.cmd.Process.Signal(os.Interrupt)
		}
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case err = <-done:
		case <-ctx.Done():
			_ = c.cmd.Process.Kill()
			err = errors.Join(ctx.Err(), <-done)
		}
		c.master.Close()
	})
	if err != nil && !errors.Is(err, os.ErrProcessDone) && !signaled(err) {
		return err
	}
	return nil
}

func signaled(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	return ok && status.Signaled()
}

func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			// A raw terminal redraws with CR. Keep it as a line break so a
			// later line, such as SIGN_WITH, is not glued to the previous row.
			if s[i] == '\r' {
				b.WriteByte('\n')
				continue
			}
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			break
		}
		i++
		if s[i] != '[' {
			continue
		}
		for i+1 < len(s) && !isCSIFinal(s[i+1]) {
			i++
		}
		if i+1 < len(s) {
			i++
		}
	}
	return b.String()
}

func isCSIFinal(c byte) bool {
	return c >= 0x40 && c <= 0x7e
}

// pipeProc is a process whose stdin is a pipe, so hotbox reads one password line.
type pipeProc struct {
	cmd    *exec.Cmd
	screen *screen
	once   sync.Once
}

func startPipe(cmd *exec.Cmd, stdin string) (*pipeProc, error) {
	cmd.Stdin = strings.NewReader(stdin)
	scr := newScreen()
	cmd.Stdout = scr
	cmd.Stderr = scr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &pipeProc{cmd: cmd, screen: scr}, nil
}

func (p *pipeProc) wait(ctx context.Context, needle string) error {
	return p.screen.wait(ctx, needle)
}

func (p *pipeProc) text() string { return p.screen.text() }

func (p *pipeProc) stop(ctx context.Context) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	var err error
	p.once.Do(func() {
		_ = p.cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- p.cmd.Wait() }()
		select {
		case err = <-done:
		case <-ctx.Done():
			_ = p.cmd.Process.Kill()
			err = errors.Join(ctx.Err(), <-done)
		}
	})
	if err != nil && !errors.Is(err, os.ErrProcessDone) && !signaled(err) {
		return err
	}
	return nil
}

func tail(s string) string {
	const max = 4000
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
