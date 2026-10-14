package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/sanitize"
)

// ProgressReporter translates library progress events into terminal output.
// It is intentionally stateful so a TTY gets one active operation at a time.
type ProgressReporter struct {
	writer      io.Writer
	interactive bool
	spinner     *Spinner
	tracker     *DownloadTracker
	phase       string
	message     string
}

// NewProgressReporter creates a reporter suitable for FetchOptions and PublishOptions.
func NewProgressReporter(writer io.Writer, interactive bool) *ProgressReporter {
	return &ProgressReporter{writer: writer, interactive: interactive}
}

// Report renders a single progress event.
func (r *ProgressReporter) Report(update zsp.Progress) {
	if update.Phase == "warning" {
		r.finishActive(false)
		fmt.Fprintln(r.writer, StatusLine("warning", update.Target))
		return
	}
	message := progressMessage(update)
	if !r.interactive {
		if update.Completed == 0 || update.Total > 0 && update.Completed >= update.Total {
			fmt.Fprintln(r.writer, StatusLine("info", message))
		}
		return
	}
	if update.Total > 0 {
		if r.tracker == nil || r.phase != update.Phase || r.message != message {
			// A target change without completion abandoned the old download.
			r.finishActive(r.tracker == nil || r.phase != update.Phase)
			r.phase, r.message = update.Phase, message
			r.tracker = NewDownloadTrackerWithWriter(r.writer, message, update.Total)
		}
		r.tracker.Update(update.Completed, update.Total)
		if update.Completed >= update.Total {
			r.tracker.Done()
			r.tracker = nil
		}
		return
	}
	if r.spinner == nil || r.phase != update.Phase {
		r.finishActive(true)
		r.phase, r.message = update.Phase, message
		r.spinner = NewSpinnerWithWriter(r.writer, message)
		r.spinner.Start()
		return
	}
	if r.message != message {
		r.message = message
		r.spinner.UpdateMessage(message)
	}
}

// finishActive stops the active tracker or spinner. With completed it leaves
// a success line; without, it only erases the progress line.
func (r *ProgressReporter) finishActive(completed bool) {
	if r.tracker != nil {
		if completed {
			r.tracker.Done()
		} else {
			r.tracker.Clear()
		}
		r.tracker = nil
	}
	if r.spinner != nil {
		if completed {
			r.spinner.StopWithSuccess(r.message)
		} else {
			r.spinner.Stop()
		}
		r.spinner = nil
	}
}

// Finish stops any dynamic terminal rendering and leaves a completed final line.
func (r *ProgressReporter) Finish() {
	r.finishActive(true)
}

func progressMessage(update zsp.Progress) string {
	phase := sanitize.Text(strings.TrimSpace(update.Phase))
	target := sanitize.Truncate(sanitize.Text(strings.TrimSpace(update.Target)), 120)
	if target == "" {
		return strings.Title(phase)
	}
	if phase == "" {
		return target
	}
	return strings.ToUpper(phase[:1]) + phase[1:] + " " + target
}
