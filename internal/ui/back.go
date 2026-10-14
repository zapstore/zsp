package ui

import (
	"context"
	"errors"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// ErrBack is returned when the user presses Esc on a prompt that can return
// to the previous one.
var ErrBack = errors.New("back")

// escForm runs a Huh form and ends it with ErrBack on Esc.
type escForm struct {
	form *huh.Form
	back bool
}

func (m *escForm) Init() tea.Cmd { return m.form.Init() }

func (m *escForm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.Type == tea.KeyEsc {
		m.back = true
		return m, tea.Quit
	}
	model, cmd := m.form.Update(msg)
	if form, ok := model.(*huh.Form); ok {
		m.form = form
	}
	return m, cmd
}

func (m *escForm) View() string {
	if m.back {
		return ""
	}
	return m.form.View()
}

// runBackForm runs form with Esc ending it with ErrBack. Cancelling ctx ends
// the form.
func runBackForm(ctx context.Context, form *huh.Form) error {
	form.SubmitCmd = tea.Quit
	form.CancelCmd = tea.Interrupt
	model := &escForm{form: form}
	_, err := tea.NewProgram(model, tea.WithOutput(os.Stderr), tea.WithReportFocus(), tea.WithContext(ctx)).Run()
	if model.back {
		return ErrBack
	}
	if form.State == huh.StateAborted || errors.Is(err, tea.ErrInterrupted) || errors.Is(err, tea.ErrProgramKilled) {
		return huh.ErrUserAborted
	}
	return err
}

// PromptFieldBack is PromptField where Esc returns ErrBack. Cancelling ctx
// ends the prompt.
func PromptFieldBack(ctx context.Context, title, description string, secret, required bool) (string, error) {
	form, value := fieldForm(title, description, secret, required)
	if err := runBackForm(ctx, form); err != nil {
		return "", err
	}
	return strings.TrimSpace(*value), nil
}

// PromptPathBack is PromptPath where Esc returns ErrBack. Cancelling ctx ends
// the prompt.
func PromptPathBack(ctx context.Context, title, description string, required bool) (string, error) {
	form, value := pathForm(title, description, "", required)
	if err := runBackForm(ctx, form); err != nil {
		return "", err
	}
	return strings.TrimSpace(*value), nil
}

// ConfirmBack asks a yes/no question where Esc returns ErrBack. Cancelling ctx
// ends the prompt.
func ConfirmBack(ctx context.Context, title, description string, defaultYes bool) (bool, error) {
	value := defaultYes
	input := huh.NewSelect[bool]().
		Title(title).
		Options(
			huh.NewOption("Yes", true),
			huh.NewOption("No", false),
		).
		Value(&value)
	if description != "" {
		input = input.Description(description)
	}
	keymap := huh.NewDefaultKeyMap()
	keymap.Select.Filter.SetEnabled(false)
	if err := runBackForm(ctx, huh.NewForm(huh.NewGroup(input)).WithKeyMap(keymap)); err != nil {
		return false, err
	}
	return value, nil
}
