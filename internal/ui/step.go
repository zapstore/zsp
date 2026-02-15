package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Logo is the ASCII art logo for Zapstore.
const Logo = `
 _____                _
/ _  / __ _ _ __  ___| |_ ___  _ __ ___
\// / / _` + "`" + ` | '_ \/ __| __/ _ \| '__/ _ \
 / //\ (_| | |_) \__ \ || (_) | | |  __/
/____/\__,_| .__/|___/\__\___/|_|  \___|
           |_|
`

// Version holds the application version, set at startup.
var Version = "dev"

// SetVersion sets the application version for logo rendering.
func SetVersion(v string) {
	Version = v
}

// RenderLogo returns the styled logo with version underneath.
func RenderLogo() string {
	var result strings.Builder
	for _, line := range strings.Split(Logo, "\n") {
		if line != "" {
			result.WriteString(LogoStyle.Render(line) + "\n")
		}
	}
	// Add blank line before version
	result.WriteString("\n")
	// Add "v" prefix only if not already present
	v := Version
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	result.WriteString(v + "\n")
	// Add blank line after version
	result.WriteString("\n")
	return result.String()
}

// StepTracker tracks progress through numbered steps in the CLI flow.
type StepTracker struct {
	current int
	total   int
	writer  io.Writer
}

// NewStepTracker creates a new step tracker with the given total number of steps.
func NewStepTracker(total int) *StepTracker {
	return &StepTracker{
		current: 0,
		total:   total,
		writer:  os.Stderr,
	}
}

// StartStep begins a new step with the given name.
// It prints a visual header for the step.
func (s *StepTracker) StartStep(name string) {
	s.current++
	s.printStepHeader(name)
}

// printStepHeader prints a formatted step header.
func (s *StepTracker) printStepHeader(name string) {
	fmt.Fprintln(s.writer)

	if NoColor {
		fmt.Fprintf(s.writer, "=== STEP %s: %s ===\n",
			fmt.Sprintf("%d/%d", s.current, s.total), strings.ToUpper(name))
	} else {
		// Compact step header: " 1/4 > FETCH ASSETS"
		header := fmt.Sprintf(" %d/%d > %s", s.current, s.total, strings.ToUpper(name))
		fmt.Fprintln(s.writer, BoldStyle.Render(header))
	}
}

// SetTotal updates the total number of steps (useful when steps are conditional).
func (s *StepTracker) SetTotal(total int) {
	s.total = total
}

// Current returns the current step number.
func (s *StepTracker) Current() int {
	return s.current
}

// Total returns the total number of steps.
func (s *StepTracker) Total() int {
	return s.total
}

// Skip skips the current step number without printing anything.
// Use this when a step is conditionally skipped.
func (s *StepTracker) Skip() {
	s.current++
}

// PrintSubStep prints a sub-step item under the current step.
func (s *StepTracker) PrintSubStep(message string) {
	fmt.Fprintf(s.writer, "  %s\n", message)
}

// PrintStepSummary prints a summary section with key-value pairs (to stderr).
func PrintStepSummary(items map[string]string) {
	for key, value := range items {
		fmt.Fprintf(os.Stderr, "  %s: %s\n", Bold(key), value)
	}
}

// PrintStepSummaryOrdered prints a summary section with key-value pairs in order (to stderr).
func PrintStepSummaryOrdered(items []KeyValue) {
	for _, item := range items {
		fmt.Fprintf(os.Stderr, "  %s: %s\n", Bold(item.Key), item.Value)
	}
}

// KeyValue represents a key-value pair for ordered summary output.
type KeyValue struct {
	Key   string
	Value string
}

// PrintSectionHeader prints a minor section header (legacy; prefer Status()).
func PrintSectionHeader(name string) {
	Status("Summary", name)
}

// PrintCompletionSummary prints a final summary (legacy; prefer Status/ErrorStatus).
func PrintCompletionSummary(success bool, message string) {
	fmt.Fprintln(os.Stderr)
	if success {
		Status("Finished", message)
	} else {
		ErrorStatus("Error", message)
	}
}

