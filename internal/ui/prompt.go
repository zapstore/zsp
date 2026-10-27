package ui

import (
	"fmt"

	"github.com/zapstore/zsp/v2/internal/sanitize"
)

// PrintSuccess prints a success message.
func PrintSuccess(message string) {
	checkmark := "✓"
	fmt.Printf("%s %s\n", Success(checkmark), sanitize.Text(trimMessage(message)))
}

// PrintInfo prints an info message.
func PrintInfo(message string) {
	info := "ℹ"
	fmt.Printf("%s %s\n", Info(info), sanitize.Text(trimMessage(message)))
}
