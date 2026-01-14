// Package ui provides terminal UI components.
package ui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

var (
	// NoColor disables colored output when true.
	NoColor = false

	// Styles
	TitleStyle   lipgloss.Style
	SuccessStyle lipgloss.Style
	ErrorStyle   lipgloss.Style
	WarningStyle lipgloss.Style
	InfoStyle    lipgloss.Style
	DimStyle     lipgloss.Style
	BoldStyle    lipgloss.Style
	CodeStyle    lipgloss.Style
	LogoStyle    lipgloss.Style
)

func init() {
	// Check for NO_COLOR environment variable
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		NoColor = true
	}

	initStyles()
}

// initStyles initializes the lipgloss styles.
func initStyles() {
	if NoColor {
		TitleStyle = lipgloss.NewStyle()
		SuccessStyle = lipgloss.NewStyle()
		ErrorStyle = lipgloss.NewStyle()
		WarningStyle = lipgloss.NewStyle()
		InfoStyle = lipgloss.NewStyle()
		DimStyle = lipgloss.NewStyle()
		BoldStyle = lipgloss.NewStyle().Bold(true)
		CodeStyle = lipgloss.NewStyle()
		LogoStyle = lipgloss.NewStyle()
		return
	}

	TitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#e0e0e0")) // Light grey

	SuccessStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6b8c6b")) // Muted sage green

	ErrorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#c87070")) // Muted coral red

	WarningStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#c9a866")) // Muted gold

	InfoStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#a0a0a0")) // Medium grey

	DimStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#606060")) // Dark grey

	BoldStyle = lipgloss.NewStyle().
		Bold(true)

	CodeStyle = lipgloss.NewStyle().
		Background(lipgloss.Color("#1a1a1a")).
		Foreground(lipgloss.Color("#c8c8c8")).
		Padding(0, 1)

	LogoStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#5a5a6e")) // Subtle dusty violet
}

// SetNoColor enables or disables colored output.
func SetNoColor(noColor bool) {
	NoColor = noColor
	initStyles()
}

// Title formats text as a title.
func Title(s string) string {
	return TitleStyle.Render(s)
}

// Success formats text as success message.
func Success(s string) string {
	return SuccessStyle.Render(s)
}

// Error formats text as error message.
func Error(s string) string {
	return ErrorStyle.Render(s)
}

// Warning formats text as warning message.
func Warning(s string) string {
	return WarningStyle.Render(s)
}

// Info formats text as info message.
func Info(s string) string {
	return InfoStyle.Render(s)
}

// Dim formats text as dimmed.
func Dim(s string) string {
	return DimStyle.Render(s)
}

// Bold formats text as bold.
func Bold(s string) string {
	return BoldStyle.Render(s)
}

// Code formats text as inline code.
func Code(s string) string {
	return CodeStyle.Render(s)
}

// JSON styles for colorized output
var (
	jsonKeyStyle    lipgloss.Style
	jsonStringStyle lipgloss.Style
	jsonNumberStyle lipgloss.Style
	jsonBoolStyle   lipgloss.Style
	jsonNullStyle   lipgloss.Style
	jsonBracketStyle lipgloss.Style
)

func initJSONStyles() {
	if NoColor {
		jsonKeyStyle = lipgloss.NewStyle()
		jsonStringStyle = lipgloss.NewStyle()
		jsonNumberStyle = lipgloss.NewStyle()
		jsonBoolStyle = lipgloss.NewStyle()
		jsonNullStyle = lipgloss.NewStyle()
		jsonBracketStyle = lipgloss.NewStyle()
		return
	}

	jsonKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0e0e0")).Bold(true)
	jsonStringStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6b8c6b")) // Green for strings
	jsonNumberStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#c9a866")) // Gold for numbers
	jsonBoolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#c9a866"))   // Gold for bools
	jsonNullStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#808080"))   // Grey for null
	jsonBracketStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#808080")) // Grey for brackets
}

// ColorizeJSON adds syntax highlighting to JSON string.
func ColorizeJSON(jsonStr string) string {
	if NoColor {
		return jsonStr
	}

	initJSONStyles()

	var result []byte
	inString := false
	inKey := false
	escaped := false
	afterColon := false

	for i := 0; i < len(jsonStr); i++ {
		c := jsonStr[i]

		if escaped {
			result = append(result, c)
			escaped = false
			continue
		}

		if c == '\\' && inString {
			result = append(result, c)
			escaped = true
			continue
		}

		if c == '"' {
			if !inString {
				// Starting a string
				inString = true
				if afterColon {
					// Value string
					result = append(result, []byte(jsonStringStyle.Render(string(c)))...)
					inKey = false
				} else {
					// Key string
					result = append(result, []byte(jsonKeyStyle.Render(string(c)))...)
					inKey = true
				}
			} else {
				// Ending a string
				if inKey {
					result = append(result, []byte(jsonKeyStyle.Render(string(c)))...)
				} else {
					result = append(result, []byte(jsonStringStyle.Render(string(c)))...)
				}
				inString = false
				inKey = false
			}
			continue
		}

		if inString {
			if inKey {
				result = append(result, []byte(jsonKeyStyle.Render(string(c)))...)
			} else {
				result = append(result, []byte(jsonStringStyle.Render(string(c)))...)
			}
			continue
		}

		// Outside of strings
		switch c {
		case ':':
			result = append(result, c)
			afterColon = true
		case ',', '\n':
			result = append(result, c)
			afterColon = false
		case '{', '}', '[', ']':
			result = append(result, []byte(jsonBracketStyle.Render(string(c)))...)
			if c == '{' || c == '[' {
				afterColon = false
			}
		case 't', 'f': // true, false
			if afterColon && i+4 <= len(jsonStr) {
				word := ""
				if jsonStr[i:i+4] == "true" {
					word = "true"
					i += 3
				} else if i+5 <= len(jsonStr) && jsonStr[i:i+5] == "false" {
					word = "false"
					i += 4
				}
				if word != "" {
					result = append(result, []byte(jsonBoolStyle.Render(word))...)
					continue
				}
			}
			result = append(result, c)
		case 'n': // null
			if afterColon && i+4 <= len(jsonStr) && jsonStr[i:i+4] == "null" {
				result = append(result, []byte(jsonNullStyle.Render("null"))...)
				i += 3
				continue
			}
			result = append(result, c)
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '-', '.':
			if afterColon {
				// Collect the full number
				numStart := i
				for i < len(jsonStr) && (jsonStr[i] >= '0' && jsonStr[i] <= '9' || jsonStr[i] == '.' || jsonStr[i] == '-' || jsonStr[i] == 'e' || jsonStr[i] == 'E' || jsonStr[i] == '+') {
					i++
				}
				result = append(result, []byte(jsonNumberStyle.Render(jsonStr[numStart:i]))...)
				i-- // Compensate for loop increment
				continue
			}
			result = append(result, c)
		default:
			result = append(result, c)
		}
	}

	return string(result)
}

