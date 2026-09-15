package ui

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
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
	value := defaultYes
	input := huh.NewSelect[bool]().
		Title(message).
		Options(
			huh.NewOption("No", false),
			huh.NewOption("Yes", true),
		).
		Height(3).
		Value(&value)
	keymap := huh.NewDefaultKeyMap()
	keymap.Select.Filter.SetEnabled(false)
	if err := huh.NewForm(huh.NewGroup(input)).WithKeyMap(keymap).Run(); err != nil {
		return false, err
	}
	return value, nil
}

// SelectOption presents a list of options with arrow-key navigation.
// Returns the selected index (0-based).
func SelectOption(message string, options []string, recommended int) (int, error) {
	_ = recommended
	selected, err := SelectField(message, "", options)
	if err != nil {
		return 0, err
	}
	for index, option := range options {
		if option == selected {
			return index, nil
		}
	}
	return 0, fmt.Errorf("selected option is unavailable")
}

// SelectMultiple presents a list of options for multiple selection with arrow keys.
// Space toggles selection, Enter confirms.
func SelectMultiple(message string, options []string) ([]int, error) {
	return SelectMultipleWithDefaults(message, options, nil)
}

// SelectMultipleWithDefaults presents a list of options with some pre-selected.
// preselected is a list of indices to pre-select.
func SelectMultipleWithDefaults(message string, options []string, preselected []int) ([]int, error) {
	choices := make([]huh.Option[string], len(options))
	for index, option := range options {
		choices[index] = huh.NewOption(option, option)
	}
	var values []string
	if err := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().Title(message).Options(choices...).Value(&values),
	)).Run(); err != nil {
		return nil, err
	}
	result := make([]int, 0, len(values))
	for index, option := range options {
		for _, value := range values {
			if option == value {
				result = append(result, index)
				break
			}
		}
	}
	return result, nil
}

// PromptSecret asks for secret input (like passwords or keys) with hidden characters.
// The input is not echoed to the terminal for security.
func PromptSecret(message string) (string, error) {
	return PromptPassword(message)
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
		// Convert to string, then zero the byte slice to minimize exposure in memory.
		// Note: The string copy cannot be zeroed (Go strings are immutable), but
		// zeroing the source bytes helps reduce the attack surface.
		str := string(result.password)
		zeroBytes(result.password)
		return str, nil
	}
}

// zeroBytes zeroes a byte slice to clear sensitive data from memory.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
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

// ErrorMessage formats an error consistently across CLI commands.
func ErrorMessage(message string) string {
	cross := "✗"
	if NoColor {
		cross = "[ERROR]"
	}
	return fmt.Sprintf("%s %s", Error(cross), message)
}

// WriteError writes a consistently formatted error to writer.
func WriteError(writer io.Writer, message string) {
	fmt.Fprintln(writer, ErrorMessage(message))
}

// PrintError prints an error message.
func PrintError(message string) {
	WriteError(os.Stdout, message)
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
