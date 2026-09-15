package ui

import (
	"bytes"
	"strings"
	"testing"

	publiczsp "github.com/zapstore/zsp"
)

func TestRenderPanelPlainOutputHasNoBorderCharacters(t *testing.T) {
	oldNoColor := NoColor
	SetNoColor(true)
	t.Cleanup(func() { SetNoColor(oldNoColor) })

	output := RenderPanel("success", "C1 proof published", []KeyValue{
		{Key: "Certificate", Value: "abc123"},
	}, []string{"Next step"})

	for _, forbidden := range []string{"╭", "╮", "╰", "╯", "─", "\x1b["} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("plain panel contains %q:\n%s", forbidden, output)
		}
	}
	for _, expected := range []string{"[SUCCESS]", "C1 proof published", "Certificate: abc123", "Next step"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("plain panel missing %q:\n%s", expected, output)
		}
	}
}

func TestProgressReporterNonInteractiveIsLineOriented(t *testing.T) {
	var output bytes.Buffer
	reporter := NewProgressReporter(&output, false)
	reporter.Report(publiczsp.Progress{Phase: "resolve", Target: "release"})
	reporter.Report(publiczsp.Progress{Phase: "download", Target: "app.apk", Completed: 50, Total: 100})
	reporter.Report(publiczsp.Progress{Phase: "download", Target: "app.apk", Completed: 100, Total: 100})

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
