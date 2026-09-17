package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	commandStyle lipgloss.Style
)

func init() {
	initPresentationStyles()
}

func initPresentationStyles() {
	if NoColor {
		commandStyle = lipgloss.NewStyle()
		return
	}

	commandStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#00d892")).
		Background(lipgloss.Color("#002923")).
		Padding(0, 1)
}

// StatusLine renders a compact, consistent terminal status message.
func StatusLine(kind, message string) string {
	var mark string
	var style lipgloss.Style
	switch kind {
	case "success":
		mark, style = "✓", SuccessStyle
	case "warning":
		mark, style = "!", WarningStyle
	case "error":
		mark, style = "×", ErrorStyle
	default:
		mark, style = "ℹ", InfoStyle
	}
	message = trimMessage(message)
	if NoColor {
		return fmt.Sprintf("%s %s", mark, message)
	}
	return style.Render(mark) + " " + message
}

// RenderPanel renders a readable, copy-safe summary without nested output.
func RenderPanel(kind, title string, fields []KeyValue, notes []string) string {
	var b strings.Builder
	title = trimMessage(title)
	if len(fields) == 1 {
		title += ": " + fields[0].Value
		fields = nil
	}
	b.WriteString(StatusLine(kind, title))
	for _, field := range fields {
		b.WriteString("\n")
		b.WriteString(StatusLine("info", field.Key+": "+field.Value))
	}
	for _, note := range notes {
		b.WriteString("\n")
		b.WriteString(StatusLine("info", note))
	}
	return b.String()
}

// RenderCommand formats a shell command as a distinct, copyable terminal token.
func RenderCommand(command string) string {
	if NoColor {
		return command
	}
	return commandStyle.Render(command)
}

// WritePanel writes a human-facing result panel.
func WritePanel(w io.Writer, kind, title string, fields []KeyValue, notes []string) {
	fmt.Fprintln(w, RenderPanel(kind, title, fields, notes))
}

func trimMessage(message string) string {
	return strings.TrimRight(strings.TrimSpace(message), ".")
}
