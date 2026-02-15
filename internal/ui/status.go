package ui

import (
	"fmt"
	"os"
)

// VerbWidth is the fixed width for right-aligned action verbs in status lines.
const VerbWidth = 12

// Verbosity levels (matching design guide).
const (
	VerbQuiet   = -1 // -q: results + errors only
	VerbNormal  = 0  // default: status + results + errors
	VerbVerbose = 1  // -v: above + detail (hashes, URLs)
	VerbDebug   = 2  // -vv: above + debug (protocol data)
)

// Verbosity and JSONMode control what gets printed. Set by main from CLI options.
var (
	Verbosity int  // See VerbQuiet..VerbDebug
	QuietMode bool // When true, suppress all status (set from Publish.Quiet or -q)
	JSONMode  bool // When true, suppress spinners and avoid color in status
)

// SetVerbosity sets the package verbosity level.
func SetVerbosity(v int) {
	Verbosity = v
}

// SetQuietMode sets whether status output is suppressed (quiet / -q).
func SetQuietMode(q bool) {
	QuietMode = q
}

// SetJSONMode sets whether output is machine-readable JSON (suppress spinners, etc.).
func SetJSONMode(json bool) {
	JSONMode = json
}

// statusLine renders a verb-aligned line to w. Verb is right-padded to VerbWidth
// and styled with the given lipgloss style.
func statusLine(verb, detail string) string {
	styled := AccentStyle.Render(fmt.Sprintf("%*s", VerbWidth, verb))
	return fmt.Sprintf("%s  %s", styled, detail)
}

// Status prints a status line with a right-aligned verb and detail to stderr.
// Suppressed in quiet mode. Used for step progress (e.g. "     Found  release v1.2.3").
func Status(verb, detail string) {
	if QuietMode {
		return
	}
	fmt.Fprintln(os.Stderr, statusLine(verb, detail))
}

// Detail prints a status line only when verbosity >= 1 (verbose / -v).
func Detail(verb, detail string) {
	if QuietMode || Verbosity < VerbVerbose {
		return
	}
	fmt.Fprintln(os.Stderr, statusLine(verb, detail))
}

// Debugf prints a debug line only when verbosity >= 2 (debug / -vv).
func Debugf(format string, args ...interface{}) {
	if Verbosity < VerbDebug {
		return
	}
	fmt.Fprintf(os.Stderr, format, args...)
	if len(format) > 0 && format[len(format)-1] != '\n' {
		fmt.Fprintln(os.Stderr)
	}
}

// Result writes scriptable output to stdout. Always prints (even in quiet mode).
func Result(s string) {
	fmt.Fprintln(os.Stdout, s)
}

// WarningStatus prints a warning-colored verb-prefix line to stderr.
// Shown even in quiet mode (warnings are important).
func WarningStatus(verb, detail string) {
	styled := WarningStyle.Render(fmt.Sprintf("%*s", VerbWidth, verb))
	fmt.Fprintf(os.Stderr, "%s  %s\n", styled, detail)
}

// ErrorStatus prints an error-colored verb-prefix line to stderr.
// Shown even in quiet mode.
func ErrorStatus(verb, detail string) {
	styled := ErrorStyle.Render(fmt.Sprintf("%*s", VerbWidth, verb))
	fmt.Fprintf(os.Stderr, "%s  %s\n", styled, detail)
}

// FormatError builds a multi-line error message in "Error -> why -> fix" form.
// Use when the error has an actionable suggestion. Empty why/fix are omitted.
func FormatError(what, why, fix string) string {
	out := "Error: " + what
	if why != "" {
		out += "\n  " + string('\u2192') + " " + why
	}
	if fix != "" {
		out += "\n  " + string('\u2192') + " " + fix
	}
	return out
}
