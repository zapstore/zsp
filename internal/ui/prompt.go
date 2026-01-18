package ui

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// Prompt asks for user input with a prompt message.
func Prompt(message string) (string, error) {
	fmt.Print(message)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
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

// PromptSecret asks for secret input (like passwords or keys).
// Note: This doesn't actually hide input - for that we'd need terminal raw mode.
func PromptSecret(message string) (string, error) {
	fmt.Print(message + ": ")
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

// PromptPassword asks for password input with hidden characters.
// Handles Ctrl+C gracefully by restoring terminal state.
func PromptPassword(message string) (string, error) {
	fmt.Print(message + ": ")

	// Check if stdin is a terminal
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Fall back to regular input if not a terminal
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(input), nil
	}

	// Save terminal state before reading password
	oldState, err := term.GetState(fd)
	if err != nil {
		return "", err
	}

	// Set up signal handler to restore terminal on Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

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
	case sig := <-sigCh:
		// Restore terminal state before exiting
		term.Restore(fd, oldState)
		fmt.Println() // Print newline
		// Re-raise the signal so the main handler can catch it
		signal.Stop(sigCh)
		p, _ := os.FindProcess(os.Getpid())
		p.Signal(sig)
		return "", fmt.Errorf("interrupted")
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
