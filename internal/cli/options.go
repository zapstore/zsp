// Package cli handles command-line interface concerns.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Command represents the active subcommand.
type Command string

const (
	CommandNone    Command = ""
	CommandWizard  Command = "wizard"
	CommandCheck   Command = "check"
	CommandPublish Command = "publish"
	CommandSkill   Command = "skill"
)

// GlobalOptions holds flags available at root level and shared across subcommands.
type GlobalOptions struct {
	Verbose bool
	NoColor bool
	Version bool
	Help    bool
	JSON    bool // Machine-readable output: --json, or stdout is not a terminal.
}

// PublishOptions holds flags for check and publish. Check reads only the
// source flags.
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
	Quiet               bool // No progress output and no preview
	SkipPreview         bool
	OverwriteRelease    bool
	OverwriteAppEvent   bool
	AppCreatedAtRelease bool // Use release date for kind 32267 created_at
	SkipMetadata        bool
	SkipAppEvent        bool // Publish only release events (kind 30063/3063), skip kind 32267
	NoCompress          bool // Preserve original icon and screenshot bytes

	// Server options
	Port int
}

// Options holds all CLI configuration options.
type Options struct {
	Command Command
	Args    []string // Remaining positional arguments

	// FlagParseError is set when a subcommand's flag set fails to parse (e.g. unknown flag).
	// Distinct from Global.Help: callers must exit 1 without treating this as a help request.
	FlagParseError error

	// UnknownSubcommand is the token the user passed when it is not a known command.
	// When non-empty, Global.Help is also set; callers should show help and exit 1.
	UnknownSubcommand string

	Global  GlobalOptions
	Publish PublishOptions
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
	opts := parseArgs(os.Args[1:])
	if opts.Command != CommandWizard && !opts.Global.Help && !opts.Global.Version && !term.IsTerminal(int(os.Stdout.Fd())) {
		opts.Global.JSON = true
	}
	return opts
}

func parseArgs(args []string) *Options {
	opts := &Options{}
	if len(args) == 0 {
		opts.Command = CommandWizard
		return opts
	}

	for len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "-help":
			opts.Global.Help = true
			opts.Args = args[1:]
			return opts
		case "-v", "--version", "-version":
			opts.Global.Version = true
			return opts
		case "--verbose":
			opts.Global.Verbose = true
			args = args[1:]
		case "--no-color":
			opts.Global.NoColor = true
			args = args[1:]
		case "--json":
			opts.Global.JSON = true
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
	switch first := args[0]; first {
	case "check":
		opts.Command = CommandCheck
		parseCommandFlags(opts, args[1:], false)
	case "publish":
		opts.Command = CommandPublish
		parseCommandFlags(opts, args[1:], true)
	case "skill":
		opts.Command = CommandSkill
		if len(args) > 1 {
			opts.FlagParseError = fmt.Errorf("skill takes no arguments")
		}
	default:
		opts.UnknownSubcommand = first
		opts.Args = args
	}
	if opts.Global.Help {
		opts.Global.JSON = false
	}
	return opts
}

// parseCommandFlags parses flags for check, or for publish when publish is
// true.
func parseCommandFlags(opts *Options, args []string, publish bool) {
	fs := flag.NewFlagSet(string(opts.Command), flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&opts.Publish.RepoURL, "r", "", "Repository URL (GitHub/GitLab/F-Droid)")
	fs.StringVar(&opts.Publish.ReleaseSource, "s", "", "Release source URL (defaults to -r)")
	fs.StringVar(&opts.Publish.Match, "match", "", "Regex pattern to filter APK assets")
	fs.StringVar(&opts.Publish.ReleaseFilter, "release-filter", "", "Regex pattern to filter releases")
	fs.StringVar(&opts.Publish.APKHash, "apk-hash", "", "Select a verified APK by hash")
	fs.StringVar(&opts.Publish.Channel, "channel", "", "Release channel")
	fs.StringVar(&opts.Publish.PrereleaseChannel, "prerelease-channel", "", "Prerelease source channel")
	fs.BoolVar(&opts.Global.Verbose, "verbose", opts.Global.Verbose, "Debug output")
	fs.BoolVar(&opts.Global.NoColor, "no-color", opts.Global.NoColor, "Disable colored output")
	fs.BoolVar(&opts.Global.JSON, "json", opts.Global.JSON, "JSON output")

	var metadataFlags stringSliceFlag
	if publish {
		fs.Var(&metadataFlags, "m", "Fetch metadata from source (repeatable: -m github -m fdroid)")
		fs.StringVar(&opts.Publish.Commit, "commit", "", "Git commit hash for reproducible builds")
		fs.BoolVar(&opts.Publish.Quiet, "quiet", false, "No progress output and no preview")
		fs.BoolVar(&opts.Publish.Quiet, "q", false, "No progress output and no preview")
		fs.BoolVar(&opts.Publish.SkipPreview, "skip-preview", false, "Skip the browser preview")
		fs.IntVar(&opts.Publish.Port, "port", 0, "Custom port for browser preview")
		fs.BoolVar(&opts.Publish.OverwriteRelease, "overwrite-release", false, "Allow an equal version code")
		fs.BoolVar(&opts.Publish.OverwriteAppEvent, "overwrite-app-event", false, "Publish kind 32267 even if the application event is unchanged")
		fs.BoolVar(&opts.Publish.AppCreatedAtRelease, "app-created-at-release", false, "Use release date for kind 32267 created_at")
		fs.BoolVar(&opts.Publish.SkipMetadata, "skip-metadata", false, "Skip fetching metadata from external sources")
		fs.BoolVar(&opts.Publish.SkipAppEvent, "skip-app-event", false, "Publish only release events, skip app metadata (kind 32267)")
		fs.BoolVar(&opts.Publish.NoCompress, "no-compress", false, "Preserve original icon and screenshot bytes")
	}
	var showHelp bool
	fs.BoolVar(&showHelp, "h", false, "Show help")
	fs.BoolVar(&showHelp, "help", false, "Show help")

	if err := fs.Parse(reorderArgsForFlagSet(args, fs)); err != nil {
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

// reorderArgsForFlagSet moves flags before positional arguments so flags may
// follow them, as in `zsp publish app.yaml -r URL`. A flag that takes a value
// always consumes the next argument, even one starting with '-', matching the
// flag package. Arguments after "--" stay positional.
func reorderArgsForFlagSet(args []string, fs *flag.FlagSet) []string {
	valued := valuedFlags(fs)
	var flags, positional []string
	endOfFlags := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			endOfFlags = true
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		if !strings.Contains(name, "=") && valued[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	if endOfFlags {
		flags = append(flags, "--")
	}
	return append(flags, positional...)
}

// valuedFlags names the flags in fs that take a value. The flag package
// accepts each flag with one or two dashes, so names are kept without them.
func valuedFlags(fs *flag.FlagSet) map[string]bool {
	valued := make(map[string]bool)
	fs.VisitAll(func(f *flag.Flag) {
		if boolean, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			return
		}
		valued[f.Name] = true
	})
	return valued
}

// ShouldShowSpinners returns true if spinners/progress should be shown.
// False when --quiet or JSON output is active (both require clean stderr).
func (o *Options) ShouldShowSpinners() bool {
	return !o.Publish.Quiet && !o.Global.JSON
}
