package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	panelBorderStyle lipgloss.Style
	panelTitleStyle  lipgloss.Style
	labelStyle       lipgloss.Style
	commandStyle     lipgloss.Style
)

func init() {
	initPresentationStyles()
}

func initPresentationStyles() {
	if NoColor {
		panelBorderStyle = lipgloss.NewStyle()
		panelTitleStyle = lipgloss.NewStyle().Bold(true)
		labelStyle = lipgloss.NewStyle().Bold(true)
		commandStyle = lipgloss.NewStyle()
		return
	}

	panelBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#303235"))
	panelTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#dedede")).Bold(true)
	labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#818284"))
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
		mark, style = "→", InfoStyle
	}
	if NoColor {
		return fmt.Sprintf("[%s] %s", strings.ToUpper(kind), message)
	}
	return style.Render(mark) + " " + message
}

// RenderPanel groups a terminal result into a readable, copy-safe summary.
// It deliberately uses no borders in plain mode so redirected output stays stable.
func RenderPanel(kind, title string, fields []KeyValue, notes []string) string {
	var b strings.Builder
	b.WriteString(StatusLine(kind, title))
	for _, field := range fields {
		b.WriteString("\n  ")
		b.WriteString(labelStyle.Render(field.Key + ":"))
		b.WriteString(" ")
		b.WriteString(field.Value)
	}
	for _, note := range notes {
		b.WriteString("\n  ")
		b.WriteString(Dim("→ "))
		b.WriteString(note)
	}
	if NoColor {
		return b.String()
	}
	return panelBorderStyle.Copy().Padding(0, 1).Render(b.String())
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
