package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// selectModel is the bubbletea model for the selector.
type selectModel struct {
	title       string
	options     []string
	cursor      int
	recommended int
	selected    int
	aborted     bool
	styles      selectStyles
}

// selectStyles holds the styles for the selector.
type selectStyles struct {
	title       lipgloss.Style
	cursor      lipgloss.Style
	selected    lipgloss.Style
	unselected  lipgloss.Style
	recommended lipgloss.Style
	dim         lipgloss.Style
}

// newSelectModel creates a new select model.
func newSelectModel(title string, options []string, recommended int) selectModel {
	styles := selectStyles{
		title:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e0e0e0")),
		cursor:      lipgloss.NewStyle().Foreground(lipgloss.Color("#6b8c6b")), // Muted green
		selected:    lipgloss.NewStyle().Foreground(lipgloss.Color("#e0e0e0")).Bold(true),
		unselected:  lipgloss.NewStyle().Foreground(lipgloss.Color("#808080")),
		recommended: lipgloss.NewStyle().Foreground(lipgloss.Color("#606060")).Italic(true),
		dim:         lipgloss.NewStyle().Foreground(lipgloss.Color("#505050")),
	}

	if NoColor {
		styles = selectStyles{
			title:       lipgloss.NewStyle(),
			cursor:      lipgloss.NewStyle(),
			selected:    lipgloss.NewStyle().Bold(true),
			unselected:  lipgloss.NewStyle(),
			recommended: lipgloss.NewStyle(),
			dim:         lipgloss.NewStyle(),
		}
	}

	// Start cursor at recommended position if valid
	startCursor := 0
	if recommended >= 0 && recommended < len(options) {
		startCursor = recommended
	}

	return selectModel{
		title:       title,
		options:     options,
		cursor:      startCursor,
		recommended: recommended,
		selected:    -1,
		styles:      styles,
	}
}

// Init implements tea.Model.
func (m selectModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.options)-1 {
				m.cursor++
			}
		case "enter", " ":
			m.selected = m.cursor
			return m, tea.Quit
		case "ctrl+c", "q", "esc":
			m.aborted = true
			return m, tea.Quit
		// Number keys for quick selection
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			idx := int(msg.String()[0] - '1')
			if idx >= 0 && idx < len(m.options) {
				m.selected = idx
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

// View implements tea.Model.
func (m selectModel) View() string {
	var b strings.Builder

	// Title
	if m.title != "" {
		b.WriteString(m.styles.title.Render(m.title))
		b.WriteString("\n")
	}

	// Hint
	hint := "↑/↓ navigate • enter select • q quit"
	if NoColor {
		hint = "up/down navigate, enter select, q quit"
	}
	b.WriteString(m.styles.dim.Render(hint))
	b.WriteString("\n\n")

	// Options
	for i, opt := range m.options {
		// Cursor
		cursor := "  "
		if i == m.cursor {
			if NoColor {
				cursor = "> "
			} else {
				cursor = m.styles.cursor.Render("› ")
			}
		}
		b.WriteString(cursor)

		// Option text
		optText := opt
		if i == m.cursor {
			optText = m.styles.selected.Render(opt)
		} else {
			optText = m.styles.unselected.Render(opt)
		}
		b.WriteString(optText)

		// Recommended badge
		if i == m.recommended {
			badge := " [recommended]"
			if NoColor {
				b.WriteString(badge)
			} else {
				b.WriteString(m.styles.recommended.Render(badge))
			}
		}

		b.WriteString("\n")
	}

	return b.String()
}

// Select presents a list of options for arrow-key selection.
// Returns the selected index and nil error on success, or -1 and error if aborted.
func Select(title string, options []string, recommended int) (int, error) {
	if len(options) == 0 {
		return -1, fmt.Errorf("no options provided")
	}

	m := newSelectModel(title, options, recommended)
	p := tea.NewProgram(m)

	finalModel, err := p.Run()
	if err != nil {
		return -1, fmt.Errorf("selector failed: %w", err)
	}

	result := finalModel.(selectModel)
	if result.aborted {
		return -1, fmt.Errorf("selection aborted")
	}

	return result.selected, nil
}

// SelectMultipleWithArrows presents a list of options for multiple selection with arrow keys.
// Space toggles selection, Enter confirms.
func SelectMultipleWithArrows(title string, options []string) ([]int, error) {
	if len(options) == 0 {
		return nil, fmt.Errorf("no options provided")
	}

	m := newMultiSelectModel(title, options)
	p := tea.NewProgram(m)

	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("selector failed: %w", err)
	}

	result := finalModel.(multiSelectModel)
	if result.aborted {
		return nil, fmt.Errorf("selection aborted")
	}

	return result.getSelected(), nil
}

// multiSelectModel is the bubbletea model for multi-selection.
type multiSelectModel struct {
	title    string
	options  []string
	cursor   int
	selected map[int]bool
	aborted  bool
	styles   selectStyles
}

func newMultiSelectModel(title string, options []string) multiSelectModel {
	styles := selectStyles{
		title:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e0e0e0")),
		cursor:      lipgloss.NewStyle().Foreground(lipgloss.Color("#6b8c6b")), // Muted green
		selected:    lipgloss.NewStyle().Foreground(lipgloss.Color("#6b8c6b")).Bold(true), // Green for selected
		unselected:  lipgloss.NewStyle().Foreground(lipgloss.Color("#808080")),
		recommended: lipgloss.NewStyle().Foreground(lipgloss.Color("#606060")).Italic(true),
		dim:         lipgloss.NewStyle().Foreground(lipgloss.Color("#505050")),
	}

	if NoColor {
		styles = selectStyles{
			title:       lipgloss.NewStyle(),
			cursor:      lipgloss.NewStyle(),
			selected:    lipgloss.NewStyle().Bold(true),
			unselected:  lipgloss.NewStyle(),
			recommended: lipgloss.NewStyle(),
			dim:         lipgloss.NewStyle(),
		}
	}

	return multiSelectModel{
		title:    title,
		options:  options,
		selected: make(map[int]bool),
		styles:   styles,
	}
}

func (m multiSelectModel) Init() tea.Cmd {
	return nil
}

func (m multiSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.options)-1 {
				m.cursor++
			}
		case " ", "x":
			m.selected[m.cursor] = !m.selected[m.cursor]
		case "a":
			// Toggle all
			allSelected := len(m.selected) == len(m.options)
			m.selected = make(map[int]bool)
			if !allSelected {
				for i := range m.options {
					m.selected[i] = true
				}
			}
		case "enter":
			return m, tea.Quit
		case "ctrl+c", "q", "esc":
			m.aborted = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m multiSelectModel) View() string {
	var b strings.Builder

	// Title
	if m.title != "" {
		b.WriteString(m.styles.title.Render(m.title))
		b.WriteString("\n")
	}

	// Hint
	hint := "↑/↓ navigate • space toggle • a all • enter confirm"
	if NoColor {
		hint = "up/down navigate, space toggle, a all, enter confirm"
	}
	b.WriteString(m.styles.dim.Render(hint))
	b.WriteString("\n\n")

	// Options
	for i, opt := range m.options {
		// Cursor
		cursor := "  "
		if i == m.cursor {
			if NoColor {
				cursor = "> "
			} else {
				cursor = m.styles.cursor.Render("› ")
			}
		}
		b.WriteString(cursor)

		// Checkbox
		checkbox := "○ "
		if m.selected[i] {
			if NoColor {
				checkbox = "[x] "
			} else {
				checkbox = m.styles.selected.Render("● ") 
			}
		} else if NoColor {
			checkbox = "[ ] "
		}
		b.WriteString(checkbox)

		// Option text
		optText := opt
		if m.selected[i] {
			optText = m.styles.selected.Render(opt)
		} else if i == m.cursor {
			optText = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0e0e0")).Render(opt)
		} else {
			optText = m.styles.unselected.Render(opt)
		}
		b.WriteString(optText)
		b.WriteString("\n")
	}

	return b.String()
}

func (m multiSelectModel) getSelected() []int {
	var result []int
	for i := range m.options {
		if m.selected[i] {
			result = append(result, i)
		}
	}
	return result
}

// ConfirmWithPort presents a Y/n/port prompt with arrow key navigation.
// Returns: confirmed (bool), port (int), error
// If user selects yes, returns (true, defaultPort, nil)
// If user selects no, returns (false, 0, nil)
// If user enters custom port, returns (true, customPort, nil)
func ConfirmWithPort(message string, defaultPort int) (bool, int, error) {
	options := []string{
		fmt.Sprintf("Yes, use port %d", defaultPort),
		"Enter custom port",
		"No, skip",
	}

	idx, err := Select(message, options, 0)
	if err != nil {
		return false, 0, err
	}

	switch idx {
	case 0:
		return true, defaultPort, nil
	case 1:
		// Prompt for custom port
		portStr, err := Prompt("Enter port number: ")
		if err != nil {
			return false, 0, err
		}
		port, err := parsePort(portStr)
		if err != nil {
			return false, 0, err
		}
		return true, port, nil
	case 2:
		return false, 0, nil
	}

	return false, 0, nil
}

// ConfirmWithPortYesOnly presents a Y/port prompt (no skip option).
// Returns: port (int), error
func ConfirmWithPortYesOnly(message string, defaultPort int) (int, error) {
	options := []string{
		fmt.Sprintf("Yes, use port %d", defaultPort),
		"Enter custom port",
	}

	idx, err := Select(message, options, 0)
	if err != nil {
		return 0, err
	}

	switch idx {
	case 0:
		return defaultPort, nil
	case 1:
		portStr, err := Prompt("Enter port number: ")
		if err != nil {
			return 0, err
		}
		return parsePort(portStr)
	}

	return defaultPort, nil
}

func parsePort(s string) (int, error) {
	s = strings.TrimSpace(s)
	port, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid port number: %s", s)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("port must be between 1 and 65535")
	}
	return port, nil
}

