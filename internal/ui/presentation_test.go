package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zapstore/zsp"
)

func TestRenderPanelUsesFlatStatusLines(t *testing.T) {
	oldNoColor := NoColor
	SetNoColor(true)
	t.Cleanup(func() { SetNoColor(oldNoColor) })

	output := RenderPanel("error", "Couldn't reach any configured relay.", []KeyValue{
		{Key: "Relay", Value: "localhost:3334"},
	}, []string{"No changes were made."})
	want := "× Couldn't reach any configured relay: localhost:3334\nℹ No changes were made"
	if output != want {
		t.Fatalf("RenderPanel() = %q, want %q", output, want)
	}
	for _, forbidden := range []string{"  ", "→", ".", "\x1b["} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("flat panel contains %q:\n%s", forbidden, output)
		}
	}
}

func TestRenderPanelListsMultipleFieldsAsInfo(t *testing.T) {
	oldNoColor := NoColor
	SetNoColor(true)
	t.Cleanup(func() { SetNoColor(oldNoColor) })

	got := RenderPanel("success", "APK verified", []KeyValue{
		{Key: "File", Value: "app.apk"},
		{Key: "Package", Value: "example.app"},
	}, nil)
	want := "✓ APK verified\nℹ File: app.apk\nℹ Package: example.app"
	if got != want {
		t.Fatalf("RenderPanel() = %q, want %q", got, want)
	}
}

func TestStatusLineInfoUsesInfoMark(t *testing.T) {
	oldNoColor := NoColor
	SetNoColor(false)
	t.Cleanup(func() { SetNoColor(oldNoColor) })

	got := StatusLine("info", "Nothing to do")
	if !strings.Contains(got, "ℹ") {
		t.Fatalf("StatusLine(info) = %q, want info mark", got)
	}
	if strings.Contains(got, "✓") {
		t.Fatalf("StatusLine(info) used a success mark: %q", got)
	}
}

func TestProgressReporterNonInteractiveIsLineOriented(t *testing.T) {
	var output bytes.Buffer
	reporter := NewProgressReporter(&output, false)
	reporter.Report(zsp.Progress{Phase: "resolve", Target: "release"})
	reporter.Report(zsp.Progress{Phase: "download", Target: "app.apk", Completed: 50, Total: 100})
	reporter.Report(zsp.Progress{Phase: "download", Target: "app.apk", Completed: 100, Total: 100})

	text := output.String()
	if strings.Contains(text, "\r") || strings.Contains(text, "\x1b[") {
		t.Fatalf("non-interactive progress must not animate:\n%s", text)
	}
	for _, expected := range []string{"Resolve release", "Download app.apk"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("progress output missing %q:\n%s", expected, text)
		}
	}
}
