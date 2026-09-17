package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/zapstore/zsp"
)

// ProgressReporter translates library progress events into terminal output.
// It is intentionally stateful so a TTY gets one active operation at a time.
type ProgressReporter struct {
	writer      io.Writer
	interactive bool
	spinner     *Spinner
	tracker     *DownloadTracker
	phase       string
}

// NewProgressReporter creates a reporter suitable for FetchOptions and PublishOptions.
func NewProgressReporter(writer io.Writer, interactive bool) *ProgressReporter {
	return &ProgressReporter{writer: writer, interactive: interactive}
}

// Report renders a single progress event.
func (r *ProgressReporter) Report(update zsp.Progress) {
	if update.Phase == "warning" {
		r.finishActive()
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
		if r.tracker == nil || r.phase != update.Phase {
			r.finishActive()
			r.phase = update.Phase
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
		r.finishActive()
		r.phase = update.Phase
		r.spinner = NewSpinnerWithWriter(r.writer, message)
		r.spinner.Start()
	}
}

func (r *ProgressReporter) finishActive() {
	if r.tracker != nil {
		r.tracker.Done()
		r.tracker = nil
	}
	if r.spinner != nil {
		r.spinner.StopWithSuccess(r.spinner.message)
		r.spinner = nil
	}
}

// Finish stops any dynamic terminal rendering and leaves a completed final line.
func (r *ProgressReporter) Finish() {
	r.finishActive()
}

func progressMessage(update zsp.Progress) string {
	phase := strings.TrimSpace(update.Phase)
	target := strings.TrimSpace(update.Target)
	if target == "" {
		return strings.Title(phase)
	}
	if phase == "" {
		return target
	}
	return strings.ToUpper(phase[:1]) + phase[1:] + " " + target
}
