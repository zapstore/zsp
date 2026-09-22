// Package cli handles command-line interface concerns.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Command represents the active subcommand.
type Command string

const (
	CommandNone    Command = ""
	CommandWizard  Command = "wizard"
	CommandPublish Command = "publish"
	CommandUtils   Command = "utils"
)

// GlobalOptions holds flags available at root level and shared across subcommands.
type GlobalOptions struct {
	Verbose bool
	NoColor bool
	Version bool
	Help    bool
	JSON    bool // Machine-readable output for operational invocations.
}

// PublishOptions holds flags specific to the publish subcommand.
type PublishOptions struct {
	// Source options
	RepoURL           string
	ReleaseSource     string
	Metadata          []string
	Match             string
	ReleaseFilter     string
	APKHash           string
	Channel           string
	PrereleaseChannel string

	// Release-specific options (CLI-only, not in config)
	Commit string // Git commit hash for reproducible builds

	// Behavior flags
	Quiet             bool // No prompts or progress output
	SkipPreview       bool
	OverwriteRelease  bool
	OverwriteAppEvent bool
	SkipMetadata      bool
	SkipAppEvent      bool // Publish only release events (kind 30063/3063), skip kind 32267
	NoCompress        bool // Preserve original icon and screenshot bytes
	Check             bool // Run the complete validation pipeline without publishing

	// Server options
	Port int
}

// UtilsOptions holds flags specific to the utils subcommand.
type UtilsOptions struct {
	Operation string // "extract-apk"
}

// Options holds all CLI configuration options.
type Options struct {
	Command Command
	Args    []string // Remaining positional arguments

	// FlagParseError is set when a subcommand's flag set fails to parse (e.g. unknown flag).
	// Distinct from Global.Help: callers must exit 1 without treating this as a help request.
	FlagParseError error

	// UnknownSubcommand is the token the user passed when it is not a known command (publish, identity, utils).
	// When non-empty, Global.Help is also set; callers should show help and exit 1.
	UnknownSubcommand string

	Global  GlobalOptions
	Publish PublishOptions
	Utils   UtilsOptions
}

// stringSliceFlag implements flag.Value to accumulate multiple flag values.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// ParseCommand parses command-line arguments and returns Options.
func ParseCommand() *Options {
	opts := &Options{}

	args := os.Args[1:]
	if len(args) == 0 {
		opts.Command = CommandWizard
		return opts
	}
	opts.Global.JSON = true

	for len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "-help":
			opts.Global.JSON = false
			opts.Global.Help = true
			opts.Args = args[1:]
			return opts
		case "-v", "--version", "-version":
			opts.Global.JSON = false
			opts.Global.Version = true
			return opts
		case "--verbose":
			opts.Global.Verbose = true
			args = args[1:]
		case "--no-color":
			opts.Global.NoColor = true
			args = args[1:]
		default:
			goto dispatch
		}
	}
	if len(args) == 0 {
		opts.FlagParseError = fmt.Errorf("command is required")
		return opts
	}

dispatch:
	first := args[0]
	// Dispatch to subcommand
	switch first {
	case "publish":
		opts.Command = CommandPublish
		parsePublishFlags(opts, args[1:])
	case "utils":
		opts.Command = CommandUtils
		parseUtilsArgs(opts, args[1:])
	default:
		opts.UnknownSubcommand = first
		opts.Args = args
	}
	if opts.Global.Help {
		opts.Global.JSON = false
	}
	return opts
}

// parsePublishFlags parses flags for the publish subcommand.
func parsePublishFlags(opts *Options, args []string) {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var metadataFlags stringSliceFlag

	fs.StringVar(&opts.Publish.RepoURL, "r", "", "Repository URL (GitHub/GitLab/F-Droid)")
	fs.StringVar(&opts.Publish.ReleaseSource, "s", "", "Release source URL (defaults to -r)")
	fs.Var(&metadataFlags, "m", "Fetch metadata from source (repeatable: -m github -m fdroid)")
	fs.StringVar(&opts.Publish.Match, "match", "", "Regex pattern to filter APK assets")
	fs.StringVar(&opts.Publish.ReleaseFilter, "release-filter", "", "Regex pattern to filter releases")
	fs.StringVar(&opts.Publish.APKHash, "apk-hash", "", "Select a verified APK by SHA-256")
	fs.StringVar(&opts.Publish.Channel, "channel", "", "Release channel")
	fs.StringVar(&opts.Publish.PrereleaseChannel, "prerelease-channel", "", "Prerelease source channel")
	fs.StringVar(&opts.Publish.Commit, "commit", "", "Git commit hash for reproducible builds")
	fs.BoolVar(&opts.Publish.Quiet, "quiet", false, "No prompts, no spinners, auto-yes to all confirmations")
	fs.BoolVar(&opts.Global.Verbose, "verbose", opts.Global.Verbose, "Debug output")
	fs.BoolVar(&opts.Global.NoColor, "no-color", opts.Global.NoColor, "Disable colored output")
	fs.BoolVar(&opts.Publish.SkipPreview, "skip-preview", false, "Skip the browser preview prompt")
	fs.IntVar(&opts.Publish.Port, "port", 0, "Custom port for browser preview")
	fs.BoolVar(&opts.Publish.OverwriteRelease, "overwrite-release", false, "Bypass cache and re-publish even if release unchanged")
	fs.BoolVar(&opts.Publish.OverwriteAppEvent, "overwrite-app-event", false, "Publish kind 32267 even if the application event is unchanged")
	fs.BoolVar(&opts.Publish.SkipMetadata, "skip-metadata", false, "Skip fetching metadata from external sources")
	fs.BoolVar(&opts.Publish.SkipAppEvent, "skip-app-event", false, "Publish only release events, skip app metadata (kind 32267)")
	fs.BoolVar(&opts.Publish.NoCompress, "no-compress", false, "Preserve original icon and screenshot bytes")
	fs.BoolVar(&opts.Publish.Check, "check", false, "Verify config fetches arm64-v8a APK (exit 0=success)")
	// Help flag
	var showHelp bool
	fs.BoolVar(&showHelp, "h", false, "Show help")
	fs.BoolVar(&showHelp, "help", false, "Show help")

	// Reorder args to put flags before positional arguments
	reorderedArgs := reorderArgsForFlagSet(args, map[string]bool{
		"-r": true, "-s": true, "-m": true, "--match": true, "--release-filter": true, "--apk-hash": true, "--channel": true, "--prerelease-channel": true, "--commit": true, "--port": true,
	})

	if err := fs.Parse(reorderedArgs); err != nil {
		opts.FlagParseError = err
		return
	}

	if showHelp {
		opts.Global.Help = true
		return
	}

	opts.Publish.Metadata = metadataFlags
	opts.Args = fs.Args()
}

// parseUtilsArgs parses positional args for the utils subcommand.
// The first positional arg is the operation: "extract-apk".
func parseUtilsArgs(opts *Options, args []string) {
	// Check for help
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			opts.Global.Help = true
			return
		}
	}

	if len(args) == 0 {
		opts.FlagParseError = fmt.Errorf("utils operation is required")
		return
	}

	opts.Utils.Operation = args[0]
	if opts.Utils.Operation != "extract-apk" {
		opts.FlagParseError = fmt.Errorf("unknown utils operation %q", opts.Utils.Operation)
		return
	}
	remaining := args[1:]

	// Parse flags for the operation
	fs := flag.NewFlagSet("utils "+opts.Utils.Operation, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.Global.Verbose, "verbose", opts.Global.Verbose, "Debug output")
	fs.BoolVar(&opts.Global.NoColor, "no-color", opts.Global.NoColor, "Disable colored output")
	// Reorder so flags come before positional args
	reorderedArgs := reorderArgsForFlagSet(remaining, map[string]bool{})
	if err := fs.Parse(reorderedArgs); err != nil {
		opts.FlagParseError = err
		return
	}

	opts.Args = fs.Args()
}

// reorderArgsForFlagSet moves flags before positional arguments.
func reorderArgsForFlagSet(args []string, valuedFlags map[string]bool) []string {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			// Check if this flag takes a value
			if valuedFlags[arg] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, arg)
		}
	}

	return append(flags, positional...)
}

// ShouldShowSpinners returns true if spinners/progress should be shown.
// False when --quiet or --json is active (both require clean stderr).
func (o *Options) ShouldShowSpinners() bool {
	return !o.Publish.Quiet && !o.Global.JSON
}
