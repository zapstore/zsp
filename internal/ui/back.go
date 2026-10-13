package ui

import (
	"errors"
	"os"

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

// runForm runs form. With back, Esc ends it with ErrBack; without, Esc does
// nothing, as in Huh.
func runForm(form *huh.Form, back bool) error {
	if !back {
		return form.Run()
	}
	form.SubmitCmd = tea.Quit
	form.CancelCmd = tea.Interrupt
	model := &escForm{form: form}
	_, err := tea.NewProgram(model, tea.WithOutput(os.Stderr), tea.WithReportFocus(), tea.WithContext(GetContext())).Run()
	if model.back {
		return ErrBack
	}
	if form.State == huh.StateAborted || errors.Is(err, tea.ErrInterrupted) || errors.Is(err, tea.ErrProgramKilled) {
		return huh.ErrUserAborted
	}
	return err
}

// SelectFieldBack is SelectField where Esc returns ErrBack.
func SelectFieldBack(title, description string, values []string) (string, error) {
	var selected string
	options := make([]huh.Option[string], len(values))
	for i, value := range values {
		options[i] = huh.NewOption(value, value)
	}
	err := runForm(huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title(title).Description(description).Options(options...).Value(&selected),
	)), true)
	return selected, err
}

// PromptFieldBack is PromptField where Esc returns ErrBack.
func PromptFieldBack(title, description string, secret, required bool) (string, error) {
	return promptField(title, description, "", secret, required, true)
}

// PromptPathBack is PromptPath where Esc returns ErrBack.
func PromptPathBack(title, description string, required bool) (string, error) {
	return promptPath(title, description, "", required, true)
}

// ConfirmBack is Confirm where Esc returns ErrBack.
func ConfirmBack(title, description string, defaultYes bool) (bool, error) {
	return confirm(title, description, defaultYes, true)
}
