package ui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/progress"
)

// Spinner displays a spinning animation during long operations.
type Spinner struct {
	message string
	frames  []string
	index   int
	done    chan struct{}
	wg      sync.WaitGroup
	writer  io.Writer
	active  bool
	mu      sync.Mutex
}

// DefaultFrames are the default spinner animation frames.
var DefaultFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// SimpleFrames are simple ASCII spinner frames.
var SimpleFrames = []string{"|", "/", "-", "\\"}

// NewSpinner creates a new spinner with a message.
func NewSpinner(message string) *Spinner {
	frames := DefaultFrames
	if NoColor {
		frames = SimpleFrames
	}

	return &Spinner{
		message: message,
		frames:  frames,
		writer:  os.Stderr,
		done:    make(chan struct{}),
	}
}

// Start begins the spinner animation.
func (s *Spinner) Start() {
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return
	}
	s.active = true
	s.done = make(chan struct{})
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				s.mu.Lock()
				frame := s.frames[s.index]
				s.index = (s.index + 1) % len(s.frames)
				msg := s.message
				s.mu.Unlock()

				fmt.Fprintf(s.writer, "\r%s %s", frame, msg)
			}
		}
	}()
}

// Stop stops the spinner animation.
func (s *Spinner) Stop() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.active = false
	close(s.done)
	s.mu.Unlock()

	s.wg.Wait()
	fmt.Fprintf(s.writer, "\r\033[K") // Clear the line with ANSI escape
}

// StopWithMessage stops the spinner and prints a final message.
func (s *Spinner) StopWithMessage(message string) {
	s.Stop()
	fmt.Fprintf(s.writer, "%s\n", message)
}

// StopWithSuccess stops the spinner with a success message.
func (s *Spinner) StopWithSuccess(message string) {
	s.Stop()
	checkmark := "✓"
	if NoColor {
		checkmark = "[OK]"
	}
	fmt.Fprintf(s.writer, "%s %s\n", Success(checkmark), message)
}

// StopWithError stops the spinner with an error message.
func (s *Spinner) StopWithError(message string) {
	s.Stop()
	cross := "✗"
	if NoColor {
		cross = "[ERROR]"
	}
	fmt.Fprintf(s.writer, "%s %s\n", Error(cross), message)
}

// StopWithWarning stops the spinner with a warning message.
func (s *Spinner) StopWithWarning(message string) {
	s.Stop()
	warning := "⚠"
	if NoColor {
		warning = "[WARN]"
	}
	fmt.Fprintf(s.writer, "%s %s\n", Warning(warning), message)
}

// UpdateMessage updates the spinner message.
func (s *Spinner) UpdateMessage(message string) {
	s.mu.Lock()
	s.message = message
	s.mu.Unlock()
}

// Progress displays a progress indicator with animated bar.
type Progress struct {
	total   int64
	current int64
	message string
	writer  io.Writer
	bar     progress.Model
	mu      sync.Mutex
}

// NewProgress creates a new progress indicator with animated bar.
func NewProgress(message string, total int64) *Progress {
	var bar progress.Model
	if NoColor {
		// Simple ASCII bar without colors
		bar = progress.New(
			progress.WithWidth(30),
			progress.WithoutPercentage(),
		)
	} else {
		// Beautiful gradient bar with colors
		bar = progress.New(
			progress.WithDefaultGradient(),
			progress.WithWidth(30),
			progress.WithoutPercentage(),
		)
	}

	return &Progress{
		message: message,
		total:   total,
		writer:  os.Stderr,
		bar:     bar,
	}
}

// Update updates the progress.
func (p *Progress) Update(current int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.current = current
	pct := float64(current) / float64(p.total)

	// Format size
	currentMB := float64(current) / (1024 * 1024)
	totalMB := float64(p.total) / (1024 * 1024)

	// Render the progress bar
	barView := p.bar.ViewAs(pct)

	fmt.Fprintf(p.writer, "\r\033[K%s %s %.1f%% (%.1f / %.1f MB)", p.message, barView, pct*100, currentMB, totalMB)
}

// Done marks the progress as complete.
func (p *Progress) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Show completed bar
	barView := p.bar.ViewAs(1.0)
	totalMB := float64(p.total) / (1024 * 1024)
	fmt.Fprintf(p.writer, "\r\033[K%s %s 100%% (%.1f MB)\n", p.message, barView, totalMB)
}

// DownloadTracker tracks download progress, handling both known and unknown sizes.
// It provides a callback function for use with download operations.
type DownloadTracker struct {
	message    string
	total      int64
	downloaded int64
	bar        progress.Model
	writer     io.Writer
	frames     []string
	frameIndex int
	started    bool
	mu         sync.Mutex
}

// NewDownloadTracker creates a new download tracker.
// Pass 0 for initialTotal if the size is unknown.
func NewDownloadTracker(message string, initialTotal int64) *DownloadTracker {
	var bar progress.Model
	if NoColor {
		bar = progress.New(
			progress.WithWidth(30),
			progress.WithoutPercentage(),
		)
	} else {
		bar = progress.New(
			progress.WithDefaultGradient(),
			progress.WithWidth(30),
			progress.WithoutPercentage(),
		)
	}

	frames := DefaultFrames
	if NoColor {
		frames = SimpleFrames
	}

	return &DownloadTracker{
		message: message,
		total:   initialTotal,
		bar:     bar,
		writer:  os.Stderr,
		frames:  frames,
	}
}

// Callback returns a function suitable for passing to download operations.
func (dt *DownloadTracker) Callback() func(downloaded, total int64) {
	return func(downloaded, total int64) {
		dt.Update(downloaded, total)
	}
}

// Update updates the download progress.
func (dt *DownloadTracker) Update(downloaded, total int64) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	dt.started = true
	dt.downloaded = downloaded

	// Update total if we learned it from the download
	if total > 0 && dt.total == 0 {
		dt.total = total
	}

	if dt.total > 0 {
		// Known total: show progress bar
		pct := float64(downloaded) / float64(dt.total)
		currentMB := float64(downloaded) / (1024 * 1024)
		totalMB := float64(dt.total) / (1024 * 1024)
		barView := dt.bar.ViewAs(pct)
		fmt.Fprintf(dt.writer, "\r\033[K%s %s %.1f%% (%.1f / %.1f MB)", dt.message, barView, pct*100, currentMB, totalMB)
	} else {
		// Unknown total: show spinner with bytes downloaded
		frame := dt.frames[dt.frameIndex]
		dt.frameIndex = (dt.frameIndex + 1) % len(dt.frames)
		fmt.Fprintf(dt.writer, "\r\033[K%s %s %s", frame, dt.message, formatBytes(downloaded))
	}
}

// Done marks the download as complete.
func (dt *DownloadTracker) Done() {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	if dt.total > 0 {
		barView := dt.bar.ViewAs(1.0)
		totalMB := float64(dt.total) / (1024 * 1024)
		fmt.Fprintf(dt.writer, "\r\033[K%s %s 100%% (%.1f MB)\n", dt.message, barView, totalMB)
	} else if dt.downloaded > 0 {
		checkmark := "✓"
		if NoColor {
			checkmark = "[OK]"
		}
		fmt.Fprintf(dt.writer, "\r\033[K%s %s %s\n", Success(checkmark), dt.message, formatBytes(dt.downloaded))
	} else {
		fmt.Fprintf(dt.writer, "\r\033[K\n")
	}
}

// formatBytes formats bytes into human-readable form.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
