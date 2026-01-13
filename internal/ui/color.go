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
		Foreground(lipgloss.Color("99")) // Purple

	SuccessStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("42")) // Green

	ErrorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("196")) // Red

	WarningStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("214")) // Orange

	InfoStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")) // Blue

	DimStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("242")) // Gray

	BoldStyle = lipgloss.NewStyle().
		Bold(true)

	CodeStyle = lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("252")).
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

