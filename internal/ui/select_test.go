package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSelectModel(t *testing.T) {
	options := []string{"Option A", "Option B", "Option C"}
	m := newSelectModel("Test", options, 0)

	// Test initial state
	if m.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", m.cursor)
	}
	if m.selected != -1 {
		t.Errorf("expected selected -1, got %d", m.selected)
	}

	// Test down navigation with j key
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = model.(selectModel)
	if m.cursor != 1 {
		t.Errorf("expected cursor at 1 after down, got %d", m.cursor)
	}

	// Test down navigation with arrow
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(selectModel)
	if m.cursor != 2 {
		t.Errorf("expected cursor at 2 after down, got %d", m.cursor)
	}

	// Test boundary (can't go below last option)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(selectModel)
	if m.cursor != 2 {
		t.Errorf("expected cursor at 2 (clamped), got %d", m.cursor)
	}

	// Test up navigation
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = model.(selectModel)
	if m.cursor != 1 {
		t.Errorf("expected cursor at 1 after up, got %d", m.cursor)
	}

	// Test enter selection
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(selectModel)
	if m.selected != 1 {
		t.Errorf("expected selected 1, got %d", m.selected)
	}
	if cmd == nil {
		t.Error("expected quit command")
	}
}

func TestSelectModelView(t *testing.T) {
	options := []string{"First", "Second", "Third"}
	m := newSelectModel("Pick one:", options, 1)

	view := m.View()
	if view == "" {
		t.Error("expected non-empty view")
	}
	// Check that the view contains our options
	if !strings.Contains(view, "First") {
		t.Error("view should contain 'First'")
	}
	if !strings.Contains(view, "recommended") {
		t.Error("view should contain 'recommended' for index 1")
	}
}

func TestMultiSelectModel(t *testing.T) {
	options := []string{"A", "B", "C"}
	m := newMultiSelectModel("Select items:", options)

	// Test toggle selection with space
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = model.(multiSelectModel)
	if !m.selected[0] {
		t.Error("expected item 0 to be selected after space")
	}

	// Navigate down and select another
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(multiSelectModel)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = model.(multiSelectModel)
	if !m.selected[1] {
		t.Error("expected item 1 to be selected")
	}

	// Test select all
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = model.(multiSelectModel)
	if len(m.selected) != 3 {
		t.Errorf("expected 3 items selected after 'a', got %d", len(m.selected))
	}

	// Test toggle all off
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = model.(multiSelectModel)
	if len(m.selected) != 0 {
		t.Errorf("expected 0 items selected after second 'a', got %d", len(m.selected))
	}
}

func TestSelectModelNumberKeys(t *testing.T) {
	options := []string{"One", "Two", "Three"}
	m := newSelectModel("Pick:", options, -1)

	// Test quick selection with number key
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m = model.(selectModel)
	if m.selected != 1 {
		t.Errorf("expected selected 1 (second option), got %d", m.selected)
	}
	if cmd == nil {
		t.Error("expected quit command after number selection")
	}
}

func TestSelectModelAbort(t *testing.T) {
	options := []string{"A", "B"}
	m := newSelectModel("Test", options, 0)

	// Test escape key aborts
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = model.(selectModel)
	if !m.aborted {
		t.Error("expected aborted to be true after escape")
	}
	if cmd == nil {
		t.Error("expected quit command after abort")
	}
}
