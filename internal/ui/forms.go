package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
)

// PromptField collects a single terminal value with a consistent Huh form.
func PromptField(title, description string, secret, required bool) (string, error) {
	return PromptFieldDefault(title, description, "", secret, required)
}

// PromptFieldDefault collects a terminal value with a prefilled default.
// Clearing a prefilled optional value removes it from the caller's config.
func PromptFieldDefault(title, description, defaultValue string, secret, required bool) (string, error) {
	value := defaultValue
	input := huh.NewInput().
		Title(title).
		Description(description).
		Value(&value)
	if secret {
		input = input.EchoMode(huh.EchoModePassword)
	}
	if required {
		input = input.Validate(func(value string) error {
			if strings.TrimSpace(value) == "" {
				return RequiredError{}
			}
			return nil
		})
	}
	if err := huh.NewForm(huh.NewGroup(input)).Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

// PromptPath collects a filesystem path and offers Tab completion of files
// and directories under the typed prefix.
func PromptPath(title, description string, required bool) (string, error) {
	return PromptPathDefault(title, description, "", required)
}

// PromptPathDefault collects a filesystem path with Tab completion and a
// prefilled default directory or file.
func PromptPathDefault(title, description, defaultValue string, required bool) (string, error) {
	value := defaultValue
	input := huh.NewInput().
		Title(title).
		Description(description).
		Value(&value).
		SuggestionsFunc(func() []string {
			return pathCompletions(value)
		}, &value)
	if required {
		input = input.Validate(func(value string) error {
			if strings.TrimSpace(value) == "" {
				return RequiredError{}
			}
			return nil
		})
	}
	if err := huh.NewForm(huh.NewGroup(input)).WithKeyMap(pathInputKeyMap()).Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func pathInputKeyMap() *huh.KeyMap {
	keymap := huh.NewDefaultKeyMap()
	keymap.Input.AcceptSuggestion = key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab / ↑↓", "complete / choose"),
	)
	keymap.Input.Next = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "next"),
	)
	return keymap
}

// SelectField presents a terminal selection with a title and supporting copy.
func SelectField(title, description string, values []string) (string, error) {
	options := make([]huh.Option[string], len(values))
	for i, value := range values {
		options[i] = huh.NewOption(value, value)
	}
	var selected string
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(title).
			Description(description).
			Options(options...).
			Value(&selected),
	)).Run(); err != nil {
		return "", err
	}
	return selected, nil
}

// RequiredError keeps validation feedback concise without exposing form internals.
type RequiredError struct{}

func (RequiredError) Error() string { return "required" }
