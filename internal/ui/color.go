// Package ui provides terminal UI components.
package ui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

var (
	// NoColor disables colored output when true.
	NoColor = false

	// Styles
	TitleStyle   lipgloss.Style
	SuccessStyle lipgloss.Style
	ErrorStyle   lipgloss.Style
	WarningStyle lipgloss.Style
	InfoStyle    lipgloss.Style
	DimStyle     lipgloss.Style
	BoldStyle    lipgloss.Style
	CodeStyle    lipgloss.Style
)

func init() {
	// Check for NO_COLOR environment variable
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		NoColor = true
	}

	initStyles()
}

// initStyles initializes the lipgloss styles.
func initStyles() {
	if NoColor {
		TitleStyle = lipgloss.NewStyle()
		SuccessStyle = lipgloss.NewStyle()
		ErrorStyle = lipgloss.NewStyle()
		WarningStyle = lipgloss.NewStyle()
		InfoStyle = lipgloss.NewStyle()
		DimStyle = lipgloss.NewStyle()
		BoldStyle = lipgloss.NewStyle().Bold(true)
		CodeStyle = lipgloss.NewStyle()
		return
	}

	TitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#9080a0")) // Dark muted purple

	SuccessStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6b8c6b")) // Muted sage green

	ErrorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#c87070")) // Muted coral red

	WarningStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#c9a866")) // Muted gold

	InfoStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#8a9fc9")) // Muted steel blue

	DimStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6a6a74")) // Dark grey

	BoldStyle = lipgloss.NewStyle().
		Bold(true)

	CodeStyle = lipgloss.NewStyle().
		Background(lipgloss.Color("#2a2a30")).
		Foreground(lipgloss.Color("#c8c8d0")).
		Padding(0, 1)
}

// SetNoColor enables or disables colored output.
func SetNoColor(noColor bool) {
	NoColor = noColor
	initStyles()
}

// Title formats text as a title.
func Title(s string) string {
	return TitleStyle.Render(s)
}

// Success formats text as success message.
func Success(s string) string {
	return SuccessStyle.Render(s)
}

// Error formats text as error message.
func Error(s string) string {
	return ErrorStyle.Render(s)
}

// Warning formats text as warning message.
func Warning(s string) string {
	return WarningStyle.Render(s)
}

// Info formats text as info message.
func Info(s string) string {
	return InfoStyle.Render(s)
}

// Dim formats text as dimmed.
func Dim(s string) string {
	return DimStyle.Render(s)
}

// Bold formats text as bold.
func Bold(s string) string {
	return BoldStyle.Render(s)
}

// Code formats text as inline code.
func Code(s string) string {
	return CodeStyle.Render(s)
}

