package ui

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// HasDisplay returns true if a graphical display is available.
// On Linux, this checks for DISPLAY or WAYLAND_DISPLAY environment variables.
// On macOS and Windows, it generally assumes a display is available.
func HasDisplay() bool {
	switch runtime.GOOS {
	case "linux":
		// Linux requires X11 or Wayland display
		if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
			return true
		}
		return false
	case "darwin":
		// macOS always has a display available unless in a headless server environment.
		// Check if we're in a pure SSH session without any display access.
		// SSH_TTY being set without DISPLAY suggests headless SSH access.
		if os.Getenv("SSH_TTY") != "" && os.Getenv("DISPLAY") == "" {
			return false
		}
		return true
	case "windows":
		// Windows generally always has a display available
		return true
	default:
		// For other platforms, check DISPLAY as a fallback
		return os.Getenv("DISPLAY") != ""
	}
}

// readLineResult holds the result of a non-blocking line read.
type readLineResult struct {
	line string
	err  error
}

// readLineAsync reads a line from stdin in a goroutine using raw os.Stdin.Read().
// Does NOT use bufio to avoid buffering conflicts with bubbletea.
// The goroutine is abandoned if context is cancelled (Go stdin reads cannot be interrupted).
func readLineAsync() <-chan readLineResult {
	ch := make(chan readLineResult, 1)
	go func() {
		var line []byte
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				if err == io.EOF && len(line) > 0 {
					ch <- readLineResult{line: strings.TrimSpace(string(line))}
					return
				}
				ch <- readLineResult{err: err}
				return
			}
			if n > 0 {
				if buf[0] == '\n' {
					ch <- readLineResult{line: strings.TrimSpace(string(line))}
					return
				}
				if buf[0] != '\r' { // Skip carriage return
					line = append(line, buf[0])
				}
			}
		}
	}()
	return ch
}

// Prompt asks for user input with a prompt message.
// Returns ErrInterrupted if Ctrl+C is pressed.
func Prompt(message string) (string, error) {
	ctx := GetContext()

	// Check if already interrupted before prompting
	if IsInterrupted() {
		return "", ErrInterrupted
	}

	fmt.Print(message)

	// Read in goroutine so we can select on context cancellation
	resultCh := readLineAsync()

	select {
	case <-ctx.Done():
		fmt.Println() // Print newline for clean output
		return "", ErrInterrupted
	case result := <-resultCh:
		if result.err != nil {
			return "", result.err
		}
		return result.line, nil
	}
}

// PromptDefault asks for user input with a default value.
func PromptDefault(message, defaultValue string) (string, error) {
	if defaultValue != "" {
		message = fmt.Sprintf("%s [%s]: ", message, defaultValue)
	} else {
		message = message + ": "
	}

	input, err := Prompt(message)
	if err != nil {
		return "", err
	}

	if input == "" {
		return defaultValue, nil
	}
	return input, nil
}

// Confirm asks for yes/no confirmation.
func Confirm(message string, defaultYes bool) (bool, error) {
	suffix := " [y/N]: "
	if defaultYes {
		suffix = " [Y/n]: "
	}

	input, err := Prompt(message + suffix)
	if err != nil {
		return false, err
	}

	input = strings.ToLower(strings.TrimSpace(input))

	if input == "" {
		return defaultYes, nil
	}

	return input == "y" || input == "yes", nil
}

// SelectOption presents a list of options with arrow-key navigation.
// Returns the selected index (0-based).
func SelectOption(message string, options []string, recommended int) (int, error) {
	return Select(message, options, recommended)
}

// SelectMultiple presents a list of options for multiple selection with arrow keys.
// Space toggles selection, Enter confirms.
func SelectMultiple(message string, options []string) ([]int, error) {
	return SelectMultipleWithArrows(message, options)
}

// SelectMultipleWithDefaults presents a list of options with some pre-selected.
// preselected is a list of indices to pre-select.
func SelectMultipleWithDefaults(message string, options []string, preselected []int) ([]int, error) {
	return SelectMultiplePreselected(message, options, preselected)
}

// PromptSecret asks for secret input (like passwords or keys).
// Note: This doesn't actually hide input - for that we'd need terminal raw mode.
func PromptSecret(message string) (string, error) {
	return Prompt(message + ": ")
}

// PromptPassword asks for password input with hidden characters.
// Returns ErrInterrupted if Ctrl+C is pressed.
func PromptPassword(message string) (string, error) {
	ctx := GetContext()

	// Check if already interrupted
	if IsInterrupted() {
		return "", ErrInterrupted
	}

	fmt.Print(message + ": ")

	// Check if stdin is a terminal
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Fall back to regular input if not a terminal
		resultCh := readLineAsync()
		select {
		case <-ctx.Done():
			fmt.Println()
			return "", ErrInterrupted
		case result := <-resultCh:
			if result.err != nil {
				return "", result.err
			}
			return result.line, nil
		}
	}

	// Save terminal state before reading password
	oldState, err := term.GetState(fd)
	if err != nil {
		return "", err
	}

	// Channel to receive password result
	resultCh := make(chan struct {
		password []byte
		err      error
	}, 1)

	go func() {
		password, err := term.ReadPassword(fd)
		resultCh <- struct {
			password []byte
			err      error
		}{password, err}
	}()

	select {
	case <-ctx.Done():
		// Restore terminal state before returning
		term.Restore(fd, oldState)
		fmt.Println() // Print newline
		return "", ErrInterrupted
	case result := <-resultCh:
		fmt.Println() // Print newline after password entry
		if result.err != nil {
			return "", result.err
		}
		return string(result.password), nil
	}
}

// PromptInt asks for an integer input with a default value.
func PromptInt(message string, defaultValue int) (int, error) {
	defaultStr := strconv.Itoa(defaultValue)
	input, err := PromptDefault(message, defaultStr)
	if err != nil {
		return 0, err
	}

	value, err := strconv.Atoi(input)
	if err != nil {
		return 0, fmt.Errorf("invalid number: %s", input)
	}
	return value, nil
}

// PrintHeader prints a section header (legacy - use StepTracker for numbered steps).
func PrintHeader(message string) {
	fmt.Println()
	if NoColor {
		fmt.Printf("=== %s ===\n", message)
	} else {
		fmt.Printf("  %s\n", InfoStyle.Render("─── "+message+" ───"))
	}
}

// PrintSuccess prints a success message.
func PrintSuccess(message string) {
	checkmark := "✓"
	if NoColor {
		checkmark = "[OK]"
	}
	fmt.Printf("%s %s\n", Success(checkmark), message)
}

// PrintError prints an error message.
func PrintError(message string) {
	cross := "✗"
	if NoColor {
		cross = "[ERROR]"
	}
	fmt.Printf("%s %s\n", Error(cross), message)
}

// PrintWarning prints a warning message.
func PrintWarning(message string) {
	warning := "⚠"
	if NoColor {
		warning = "[WARN]"
	}
	fmt.Printf("%s %s\n", Warning(warning), message)
}

// PrintInfo prints an info message.
func PrintInfo(message string) {
	info := "ℹ"
	if NoColor {
		info = "[INFO]"
	}
	fmt.Printf("%s %s\n", Info(info), message)
}

// PrintKeyValue prints a key-value pair.
func PrintKeyValue(key, value string) {
	fmt.Printf("  %s: %s\n", Bold(key), value)
}
