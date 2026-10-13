package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

func TestEscFormEndsWithBackOnEsc(t *testing.T) {
	var value string
	form := huh.NewForm(huh.NewGroup(huh.NewInput().Value(&value)))
	model := &escForm{form: form}
	model.Init()

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if model.back {
		t.Fatal("a typed rune ended the form")
	}
	_ = cmd

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !model.back || cmd == nil {
		t.Fatal("Esc did not end the form with back")
	}
	if model.View() != "" {
		t.Fatal("a form ended with back still renders")
	}
}
