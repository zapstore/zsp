// Package cli handles command-line interface concerns.
package cli

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Command represents the active subcommand.
type Command string

const (
	CommandNone        Command = ""
	CommandPublish     Command = "publish"
	CommandLinkIdentity Command = "link-identity"
	CommandExtract     Command = "extract"
)

// GlobalOptions holds flags available at root level and shared across subcommands.
type GlobalOptions struct {
	Verbosity int  // 0=normal, 1=verbose (-v), 2=debug (-vv)
	NoColor   bool
	Version   bool
	Help      bool
	JSON      bool // Machine-readable JSON output
	Yes       bool // Skip confirmation prompts (-y/--yes)
}

// PublishOptions holds flags specific to the publish subcommand.
type PublishOptions struct {
	// Source options
	RepoURL       string
	ReleaseSource string
	Metadata      []string
	Match         string
	ConfigFile    string // Config file path (-c flag)

	// Release-specific options (CLI-only, not in config)
	Identifier     string // Explicit app identifier (ignored for APKs which use package ID)
	Version        string // Explicit version for the published asset (overrides auto-detection)
	BinaryVersion  string // Version of the zsp binary (set by main; used as fallback when Version and release have none)
	Commit         string // Git commit hash for reproducible builds
	Channel    string // Release channel: main (default), beta, nightly, dev

	// Behavior flags (Yes is global; Quiet still implies yes for publish)
	Offline             bool // Sign events without uploading/publishing (outputs to stdout)
	Quiet               bool
	SkipPreview         bool
	OverwriteRelease    bool
	IncludePreReleases  bool
	SkipMetadata        bool
	Legacy              bool
	AppCreatedAtRelease bool // Use release timestamp for kind 32267 created_at
	Wizard              bool
	Check               bool // Verify config fetches arm64-v8a APK (exit 0=success)

	// Server options
	Port int
}

// LinkIdentityOptions holds flags specific to the link-identity subcommand.
type LinkIdentityOptions struct {
	Set          string   // Path to certificate file (.p12, .pfx, .pem, .crt) for publishing proof
	SetExpiry    string   // Validity period for identity proof when using --set (e.g., "1y", "6mo", "30d")
	Verify       string   // Verify identity proof (path to certificate or APK)
	Relays       []string // Relays for identity proof operations
	Offline      bool     // Output event JSON to stdout instead of publishing
}

// ExtractOptions holds options for the extract subcommand (positional arg is the APK file).
type ExtractOptions struct {
	// No flags; first positional arg is the APK path
}

// Options holds all CLI configuration options.
type Options struct {
	Command Command
	Args    []string // Remaining positional arguments

	Global        GlobalOptions
	Publish       PublishOptions
	LinkIdentity  LinkIdentityOptions
	Extract       ExtractOptions
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

// DefaultIdentityRelays are the default relays for identity proof operations.
var DefaultIdentityRelays = []string{
	"wss://relay.primal.net",
	"wss://relay.damus.io",
	"wss://relay.zapstore.dev",
}

// ParseCommand parses command-line arguments and returns Options.
func ParseCommand() *Options {
	opts := &Options{}

	// Check for --help or -h at root level (before subcommand)
	// Also check for --version at root
	args := os.Args[1:]
	if len(args) == 0 {
		// No args - show help
		opts.Global.Help = true
		return opts
	}

	// Preprocess: apply global flags from anywhere in args, then find command
	// -v / -vv / --verbose (stackable verbosity), --json, -y/--yes, --no-color
	var command string
	var commandArgs []string
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "-h", "--help", "-help":
			opts.Global.Help = true
			opts.Args = args[i+1:]
			return opts
		case "--version", "-version":
			opts.Global.Version = true
			return opts
		case "-v":
			opts.Global.Verbosity = 1
			i++
			continue
		case "-vv":
			opts.Global.Verbosity = 2
			i++
			continue
		case "--verbose":
			opts.Global.Verbosity = 1
			i++
			continue
		case "--json":
			opts.Global.JSON = true
			i++
			continue
		case "-y", "--yes":
			opts.Global.Yes = true
			i++
			continue
		case "--no-color":
			opts.Global.NoColor = true
			i++
			continue
		}
		// First non-global-flag is the command
		if len(a) > 0 && a[0] != '-' {
			command = a
			commandArgs = args[i+1:]
			break
		}
		// Unknown flag or positional; treat as command so help can show
		command = a
		commandArgs = args[i+1:]
		break
	}
	if command == "" {
		opts.Global.Help = true
		return opts
	}

	// Dispatch to subcommand
	switch command {
	case "publish":
		opts.Command = CommandPublish
		parsePublishFlags(opts, commandArgs)
	case "link-identity":
		opts.Command = CommandLinkIdentity
		parseLinkIdentityFlags(opts, commandArgs)
	case "extract":
		opts.Command = CommandExtract
		parseExtractFlags(opts, commandArgs)
	default:
		// Unknown subcommand - show help
		opts.Global.Help = true
		opts.Args = append([]string{command}, commandArgs...)
	}

	return opts
}

// parsePublishFlags parses flags for the publish subcommand.
func parsePublishFlags(opts *Options, args []string) {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var metadataFlags stringSliceFlag

	fs.StringVar(&opts.Publish.ConfigFile, "c", "", "Config file path (positional args become asset files)")
	fs.StringVar(&opts.Publish.RepoURL, "r", "", "Repository URL (GitHub/GitLab/F-Droid)")
	fs.StringVar(&opts.Publish.ReleaseSource, "s", "", "Release source URL (defaults to -r)")
	fs.Var(&metadataFlags, "m", "Fetch metadata from source (repeatable: -m github -m fdroid)")
	fs.StringVar(&opts.Publish.Match, "match", "", "Regex pattern to filter assets")
	fs.StringVar(&opts.Publish.Identifier, "id", "", "App identifier for executables (ignored for APKs)")
	fs.StringVar(&opts.Publish.Version, "version", "", "Version for the published asset (overrides auto-detection)")
	fs.StringVar(&opts.Publish.Commit, "commit", "", "Git commit hash for reproducible builds")
	fs.StringVar(&opts.Publish.Channel, "channel", "main", "Release channel: main, beta, nightly, dev")
	fs.BoolVar(&opts.Global.Yes, "y", false, "Skip confirmations (auto-yes)")
	fs.BoolVar(&opts.Global.Yes, "yes", false, "Skip confirmations (auto-yes)")
	fs.BoolVar(&opts.Publish.Offline, "offline", false, "Sign events without uploading/publishing (outputs JSON to stdout)")
	fs.BoolVar(&opts.Publish.Quiet, "quiet", false, "Minimal output, no prompts (implies -y)")
	fs.BoolVar(&opts.Publish.Quiet, "q", false, "Minimal output, no prompts (alias)")
	fs.BoolVar(&opts.Global.JSON, "json", false, "Output JSON to stdout (status to stderr)")
	var verboseBool bool
	fs.BoolVar(&verboseBool, "v", false, "Verbose output")
	fs.BoolVar(&verboseBool, "verbose", false, "Verbose output")
	fs.BoolVar(&opts.Global.NoColor, "no-color", false, "Disable colored output")
	fs.BoolVar(&opts.Publish.SkipPreview, "skip-preview", false, "Skip the browser preview prompt")
	fs.IntVar(&opts.Publish.Port, "port", 0, "Custom port for browser preview/signing")
	fs.BoolVar(&opts.Publish.OverwriteRelease, "overwrite-release", false, "Bypass cache and re-publish even if release unchanged")
	fs.BoolVar(&opts.Publish.IncludePreReleases, "pre-release", false, "Include pre-releases when fetching the latest release")
	fs.BoolVar(&opts.Publish.SkipMetadata, "skip-metadata", false, "Skip fetching metadata from external sources")
	fs.BoolVar(&opts.Publish.Wizard, "wizard", false, "Run interactive wizard (uses existing config as defaults)")
	fs.BoolVar(&opts.Publish.Legacy, "legacy", false, "Use legacy event format for relay.zapstore.dev compatibility")
	fs.BoolVar(&opts.Publish.AppCreatedAtRelease, "app-created-at-release", false, "Use release date for kind 32267 created_at (indexer compatibility)")
	fs.BoolVar(&opts.Publish.Check, "check", false, "Verify config fetches arm64-v8a APK (exit 0=success)")

	// Help flag
	var showHelp bool
	fs.BoolVar(&showHelp, "h", false, "Show help")
	fs.BoolVar(&showHelp, "help", false, "Show help")

	// Reorder args to put flags before positional arguments
	reorderedArgs := reorderArgsForFlagSet(args, map[string]bool{
		"-c": true, "-r": true, "-s": true, "-m": true, "--match": true, "--id": true, "--version": true, "--commit": true, "--channel": true, "--port": true,
	})

	if err := fs.Parse(reorderedArgs); err != nil {
		opts.Global.Help = true
		return
	}

	if showHelp {
		opts.Global.Help = true
		return
	}

	if verboseBool && opts.Global.Verbosity < 1 {
		opts.Global.Verbosity = 1
	}

	opts.Publish.Metadata = metadataFlags
	opts.Args = fs.Args()

	// Quiet implies yes
	if opts.Publish.Quiet {
		opts.Global.Yes = true
	}
}

// parseLinkIdentityFlags parses flags for the link-identity subcommand.
func parseLinkIdentityFlags(opts *Options, args []string) {
	fs := flag.NewFlagSet("link-identity", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var relaysFlag stringSliceFlag

	fs.StringVar(&opts.LinkIdentity.Set, "set", "", "Publish cryptographic identity proof (NIP-C1 kind 30509)")
	fs.StringVar(&opts.LinkIdentity.SetExpiry, "set-expiry", "1y", "Validity period for identity proof when using --set (e.g., 1y, 6mo, 30d)")
	fs.StringVar(&opts.LinkIdentity.Verify, "verify", "", "Verify identity proof against certificate or APK")
	fs.Var(&relaysFlag, "relays", "Relays for identity proofs (repeatable, overrides defaults)")
	fs.BoolVar(&opts.LinkIdentity.Offline, "offline", false, "Output event JSON to stdout instead of publishing")
	fs.BoolVar(&opts.Global.JSON, "json", false, "Output JSON to stdout (status to stderr)")
	var verboseBool bool
	fs.BoolVar(&verboseBool, "v", false, "Verbose output")
	fs.BoolVar(&verboseBool, "verbose", false, "Verbose output")
	fs.BoolVar(&opts.Global.NoColor, "no-color", false, "Disable colored output")

	// Help flag
	var showHelp bool
	fs.BoolVar(&showHelp, "h", false, "Show help")
	fs.BoolVar(&showHelp, "help", false, "Show help")

	// Reorder args
	reorderedArgs := reorderArgsForFlagSet(args, map[string]bool{
		"--set": true, "--set-expiry": true, "--verify": true, "--relays": true,
	})

	if err := fs.Parse(reorderedArgs); err != nil {
		opts.Global.Help = true
		return
	}

	if showHelp {
		opts.Global.Help = true
		return
	}

	if verboseBool && opts.Global.Verbosity < 1 {
		opts.Global.Verbosity = 1
	}

	// Set identity relays (use defaults if not specified)
	if len(relaysFlag) > 0 {
		opts.LinkIdentity.Relays = relaysFlag
	} else {
		opts.LinkIdentity.Relays = DefaultIdentityRelays
	}

	opts.Args = fs.Args()
}

// parseExtractFlags parses flags for the extract subcommand (positional arg is APK file).
func parseExtractFlags(opts *Options, args []string) {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	fs.BoolVar(&opts.Global.JSON, "json", false, "Output JSON to stdout (status to stderr)")
	var verboseBool bool
	fs.BoolVar(&verboseBool, "v", false, "Verbose output")
	fs.BoolVar(&verboseBool, "verbose", false, "Verbose output")
	fs.BoolVar(&opts.Global.NoColor, "no-color", false, "Disable colored output")

	// Help flag
	var showHelp bool
	fs.BoolVar(&showHelp, "h", false, "Show help")
	fs.BoolVar(&showHelp, "help", false, "Show help")

	if err := fs.Parse(args); err != nil {
		opts.Global.Help = true
		return
	}

	if showHelp {
		opts.Global.Help = true
		return
	}

	if verboseBool && opts.Global.Verbosity < 1 {
		opts.Global.Verbosity = 1
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

// IsPublishInteractive returns true if the publish command should be interactive.
func (o *Options) IsPublishInteractive() bool {
	return !o.Publish.Quiet && !o.Global.Yes
}

// ShouldShowSpinners returns true if spinners/progress should be shown (for publish).
func (o *PublishOptions) ShouldShowSpinners() bool {
	return !o.Quiet
}

// ValidateChannel returns an error if the channel is invalid.
func (o *PublishOptions) ValidateChannel() error {
	validChannels := map[string]bool{"main": true, "beta": true, "nightly": true, "dev": true}
	if !validChannels[o.Channel] {
		return fmt.Errorf("invalid --channel %q: must be one of main, beta, nightly, dev", o.Channel)
	}
	return nil
}

// IsInteractive returns true if the CLI should be interactive (for link-identity).
func (o *LinkIdentityOptions) IsInteractive() bool {
	return true
}

// ShouldShowSpinners returns true if spinners/progress should be shown (for link-identity).
func (o *LinkIdentityOptions) ShouldShowSpinners() bool {
	return true
}

// ParseExpiryDuration parses a human-friendly duration string.
// Supports: y (years), mo (months), d (days), h (hours).
// Note: Use "mo" for months to avoid conflict with Go's "m" for minutes.
// Returns the duration or an error if the format is invalid.
func ParseExpiryDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// Check for our custom format first (before Go's time.ParseDuration)
	// This ensures "6mo" is parsed as months, not passed to Go's parser

	// Try months first (must check before single-char suffixes)
	if strings.HasSuffix(s, "mo") {
		numStr := s[:len(s)-2]
		num, err := strconv.Atoi(numStr)
		if err != nil {
			return 0, fmt.Errorf("invalid duration number: %s", numStr)
		}
		return time.Duration(num) * 30 * 24 * time.Hour, nil // Approximate month
	}

	// Parse single-char suffixes
	if len(s) >= 2 {
		unit := s[len(s)-1]
		numStr := s[:len(s)-1]

		if num, err := strconv.Atoi(numStr); err == nil {
			switch unit {
			case 'y':
				return time.Duration(num) * 365 * 24 * time.Hour, nil
			case 'd':
				return time.Duration(num) * 24 * time.Hour, nil
			}
		}
	}

	// Fall back to Go's standard duration format (e.g., "720h", "30m")
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	return 0, fmt.Errorf("invalid duration format: %s (use y, mo, d, or h)", s)
}
