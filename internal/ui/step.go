package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// StepTracker tracks progress through wizard stages.
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

// StartStep begins a new wizard stage.
func (s *StepTracker) StartStep(name string) {
	s.current++
	s.printStepHeader(name)
}

// printStepHeader prints a compact wizard stage heading.
func (s *StepTracker) printStepHeader(name string) {
	fmt.Fprintln(s.writer)
	if NoColor {
		fmt.Fprintf(s.writer, "%s\n", name)
		return
	}
	fmt.Fprintf(s.writer, "%s\n", BoldStyle.Render(name))
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

// PrintStepSummary prints a summary section with key-value pairs.
func PrintStepSummary(items map[string]string) {
	for key, value := range items {
		fmt.Printf("  %s: %s\n", Bold(key), value)
	}
}

// PrintStepSummaryOrdered prints a summary section with key-value pairs in order.
func PrintStepSummaryOrdered(items []KeyValue) {
	for _, item := range items {
		fmt.Printf("  %s: %s\n", Bold(item.Key), item.Value)
	}
}

// KeyValue represents a key-value pair for ordered summary output.
type KeyValue struct {
	Key   string
	Value string
}

// PrintSectionHeader prints a minor section header within a step.
func PrintSectionHeader(name string) {
	fmt.Println()
	if NoColor {
		fmt.Printf("  --- %s ---\n", name)
	} else {
		fmt.Printf("  %s\n", InfoStyle.Render("─── "+name+" ───"))
	}
}

// PrintCompletionSummary prints a final summary when all steps are done.
func PrintCompletionSummary(success bool, message string) {
	lineWidth := 60
	line := strings.Repeat("━", lineWidth)

	fmt.Println()
	if NoColor {
		if success {
			fmt.Printf("=== COMPLETE: %s ===\n", message)
		} else {
			fmt.Printf("=== FAILED: %s ===\n", message)
		}
	} else {
		fmt.Println(DimStyle.Render(line))
		if success {
			fmt.Printf(" %s %s\n", SuccessStyle.Render("✓"), BoldStyle.Render(message))
		} else {
			fmt.Printf(" %s %s\n", ErrorStyle.Render("✗"), BoldStyle.Render(message))
		}
		fmt.Println(DimStyle.Render(line))
	}
	fmt.Println()
}
