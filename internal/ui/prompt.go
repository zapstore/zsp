package ui

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
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

// SelectOption presents a list of options and returns the selected index.
func SelectOption(message string, options []string, recommended int) (int, error) {
	fmt.Println(message)

	for i, opt := range options {
		prefix := "  "
		suffix := ""
		if i == recommended {
			suffix = " " + Dim("[recommended]")
		}
		fmt.Printf("%s%d. %s%s\n", prefix, i+1, opt, suffix)
	}

	for {
		input, err := Prompt("> ")
		if err != nil {
			return -1, err
		}

		// Parse selection
		idx, err := strconv.Atoi(input)
		if err != nil || idx < 1 || idx > len(options) {
			fmt.Printf("Please enter a number between 1 and %d\n", len(options))
			continue
		}

		return idx - 1, nil
	}
}

// SelectMultiple presents a list of options for multiple selection.
func SelectMultiple(message string, options []string) ([]int, error) {
	fmt.Println(message)
	fmt.Println(Dim("(Enter numbers separated by spaces, or 'all' for all options)"))

	for i, opt := range options {
		fmt.Printf("  %d. %s\n", i+1, opt)
	}

	for {
		input, err := Prompt("> ")
		if err != nil {
			return nil, err
		}

		input = strings.TrimSpace(input)
		if input == "" {
			return []int{}, nil
		}

		if strings.ToLower(input) == "all" {
			result := make([]int, len(options))
			for i := range options {
				result[i] = i
			}
			return result, nil
		}

		// Parse selections
		parts := strings.Fields(input)
		result := make([]int, 0, len(parts))
		valid := true

		for _, part := range parts {
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 1 || idx > len(options) {
				fmt.Printf("Invalid selection: %s\n", part)
				valid = false
				break
			}
			result = append(result, idx-1)
		}

		if valid {
			return result, nil
		}
	}
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

// PrintHeader prints a section header.
func PrintHeader(message string) {
	fmt.Println()
	fmt.Println(Title("=== " + message + " ==="))
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

