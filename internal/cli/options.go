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
	CommandNone     Command = ""
	CommandPublish  Command = "publish"
	CommandIdentity Command = "identity"
	CommandUtils    Command = "utils"
)

// GlobalOptions holds flags available at root level and shared across subcommands.
type GlobalOptions struct {
	Verbose bool
	NoColor bool
	Version bool
	Help    bool
	JSON    bool // Machine-readable output: errors as {"error":"..."} to stderr, events/results as JSONL to stdout
}

// PublishOptions holds flags specific to the publish subcommand.
type PublishOptions struct {
	// Source options
	RepoURL       string
	ReleaseSource string
	Metadata      []string
	Match         string

	// Release-specific options (CLI-only, not in config)
	Commit  string // Git commit hash for reproducible builds
	Channel string // Release channel: main (default), beta, nightly, dev

	// Behavior flags
	Offline             bool // Sign events without uploading/publishing (outputs to stdout)
	Quiet               bool // No prompts, no spinners, auto-yes to all confirmations
	SkipPreview         bool
	OverwriteRelease    bool
	IncludePreReleases  bool
	SkipMetadata        bool
	AppCreatedAtRelease bool // Use release timestamp for kind 32267 created_at
	SkipAppEvent        bool // Publish only release events (kind 30063/3063), skip kind 32267
	SkipCertificateLinking bool // Skip certificate-to-identity linking check
	Wizard              bool
	Check               bool // Verify config fetches arm64-v8a APK (exit 0=success)

	// Server options
	Port int
}

// UtilsOptions holds flags specific to the utils subcommand.
type UtilsOptions struct {
	Operation string // "extract-apk" or "check-releases"
}

// IdentityOptions holds flags specific to the identity subcommand.
type IdentityOptions struct {
	LinkKey       string   // Path to certificate file (.p12, .pfx, .pem, .crt)
	LinkKeyExpiry string   // Validity period for identity proof (e.g., "1y", "6mo", "30d")
	Verify        string   // Verify identity proof (path to certificate or APK)
	Relays        []string // Relays for identity proof operations
	Offline       bool     // Output event JSON to stdout instead of publishing
}

// Options holds all CLI configuration options.
type Options struct {
	Command Command
	Args    []string // Remaining positional arguments

	Global   GlobalOptions
	Publish  PublishOptions
	Identity IdentityOptions
	Utils    UtilsOptions
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

	// Check first arg for global flags or subcommand
	first := args[0]

	// Handle global flags at root
	if first == "-h" || first == "--help" || first == "-help" {
		opts.Global.Help = true
		opts.Args = args[1:] // Pass remaining args for help search
		return opts
	}
	if first == "-v" || first == "--version" || first == "-version" {
		opts.Global.Version = true
		return opts
	}
	if first == "--verbose" {
		opts.Global.Verbose = true
		args = args[1:]
		if len(args) == 0 {
			opts.Global.Help = true
			return opts
		}
		first = args[0]
	}
	if first == "--no-color" {
		opts.Global.NoColor = true
		args = args[1:]
		if len(args) == 0 {
			opts.Global.Help = true
			return opts
		}
		first = args[0]
	}
	if first == "--json" {
		opts.Global.JSON = true
		args = args[1:]
		if len(args) == 0 {
			opts.Global.Help = true
			return opts
		}
		first = args[0]
	}

	// Dispatch to subcommand
	switch first {
	case "publish":
		opts.Command = CommandPublish
		parsePublishFlags(opts, args[1:])
	case "identity":
		opts.Command = CommandIdentity
		parseIdentityFlags(opts, args[1:])
	case "utils":
		opts.Command = CommandUtils
		parseUtilsArgs(opts, args[1:])
	default:
		// Unknown subcommand - show help
		opts.Global.Help = true
		opts.Args = args
	}

	return opts
}

// parsePublishFlags parses flags for the publish subcommand.
func parsePublishFlags(opts *Options, args []string) {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var metadataFlags stringSliceFlag

	fs.StringVar(&opts.Publish.RepoURL, "r", "", "Repository URL (GitHub/GitLab/F-Droid)")
	fs.StringVar(&opts.Publish.ReleaseSource, "s", "", "Release source URL (defaults to -r)")
	fs.Var(&metadataFlags, "m", "Fetch metadata from source (repeatable: -m github -m fdroid)")
	fs.StringVar(&opts.Publish.Match, "match", "", "Regex pattern to filter APK assets")
	fs.StringVar(&opts.Publish.Commit, "commit", "", "Git commit hash for reproducible builds")
	fs.StringVar(&opts.Publish.Channel, "channel", "main", "Release channel: main, beta, nightly, dev")
	fs.BoolVar(&opts.Publish.Offline, "offline", false, "Sign events without uploading/publishing (outputs JSON to stdout)")
	fs.BoolVar(&opts.Publish.Quiet, "quiet", false, "No prompts, no spinners, auto-yes to all confirmations")
	fs.BoolVar(&opts.Publish.Quiet, "q", false, "Alias for --quiet")
	fs.BoolVar(&opts.Global.Verbose, "verbose", false, "Debug output")
	fs.BoolVar(&opts.Global.NoColor, "no-color", false, "Disable colored output")
	fs.BoolVar(&opts.Publish.SkipPreview, "skip-preview", false, "Skip the browser preview prompt")
	fs.IntVar(&opts.Publish.Port, "port", 0, "Custom port for browser preview/signing")
	fs.BoolVar(&opts.Publish.OverwriteRelease, "overwrite-release", false, "Bypass cache and re-publish even if release unchanged")
	fs.BoolVar(&opts.Publish.IncludePreReleases, "pre-release", false, "Include pre-releases when fetching the latest release")
	fs.BoolVar(&opts.Publish.SkipMetadata, "skip-metadata", false, "Skip fetching metadata from external sources")
	fs.BoolVar(&opts.Publish.Wizard, "wizard", false, "Run interactive wizard (uses existing config as defaults)")
	fs.BoolVar(&opts.Publish.AppCreatedAtRelease, "app-created-at-release", false, "Use release date for kind 32267 created_at (indexer compatibility)")
	fs.BoolVar(&opts.Publish.SkipAppEvent, "skip-app-event", false, "Publish only release events, skip app metadata (kind 32267)")
	fs.BoolVar(&opts.Publish.SkipCertificateLinking, "skip-certificate-linking", false, "Skip certificate-to-identity linking check")
	fs.BoolVar(&opts.Publish.Check, "check", false, "Verify config fetches arm64-v8a APK (exit 0=success)")
	fs.BoolVar(&opts.Global.JSON, "json", false, "Machine-readable output (errors as JSON to stderr, events as JSONL to stdout)")

	// Help flag
	var showHelp bool
	fs.BoolVar(&showHelp, "h", false, "Show help")
	fs.BoolVar(&showHelp, "help", false, "Show help")

	// Reorder args to put flags before positional arguments
	reorderedArgs := reorderArgsForFlagSet(args, map[string]bool{
		"-r": true, "-s": true, "-m": true, "--match": true, "--commit": true, "--channel": true, "--port": true,
	})

	if err := fs.Parse(reorderedArgs); err != nil {
		opts.Global.Help = true
		return
	}

	if showHelp {
		opts.Global.Help = true
		return
	}

	opts.Publish.Metadata = metadataFlags
	opts.Args = fs.Args()
}

// parseIdentityFlags parses flags for the identity subcommand.
func parseIdentityFlags(opts *Options, args []string) {
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var relaysFlag stringSliceFlag

	fs.StringVar(&opts.Identity.LinkKey, "link-key", "", "Link signing certificate to your Nostr identity")
	fs.StringVar(&opts.Identity.LinkKeyExpiry, "link-key-expiry", "1y", "Validity period for identity proof (e.g., 1y, 6mo, 30d)")
	fs.StringVar(&opts.Identity.Verify, "verify", "", "Verify identity proof against certificate or APK")
	fs.Var(&relaysFlag, "relays", "Relays for identity proofs (repeatable, overrides defaults)")
	fs.BoolVar(&opts.Identity.Offline, "offline", false, "Output event JSON to stdout instead of publishing")
	fs.BoolVar(&opts.Global.Verbose, "verbose", false, "Debug output")
	fs.BoolVar(&opts.Global.NoColor, "no-color", false, "Disable colored output")
	fs.BoolVar(&opts.Global.JSON, "json", false, "Machine-readable output (errors as JSON to stderr)")

	// Help flag
	var showHelp bool
	fs.BoolVar(&showHelp, "h", false, "Show help")
	fs.BoolVar(&showHelp, "help", false, "Show help")

	// Reorder args
	reorderedArgs := reorderArgsForFlagSet(args, map[string]bool{
		"--link-key": true, "--link-key-expiry": true, "--verify": true, "--relays": true,
	})

	if err := fs.Parse(reorderedArgs); err != nil {
		opts.Global.Help = true
		return
	}

	if showHelp {
		opts.Global.Help = true
		return
	}

	// Set identity relays (use defaults if not specified)
	if len(relaysFlag) > 0 {
		opts.Identity.Relays = relaysFlag
	} else {
		opts.Identity.Relays = DefaultIdentityRelays
	}

	opts.Args = fs.Args()
}

// parseUtilsArgs parses positional args for the utils subcommand.
// The first positional arg is the operation: "extract-apk" or "check-releases".
func parseUtilsArgs(opts *Options, args []string) {
	// Check for help
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			opts.Global.Help = true
			return
		}
	}

	if len(args) == 0 {
		opts.Global.Help = true
		return
	}

	opts.Utils.Operation = args[0]
	remaining := args[1:]

	// Parse flags for the operation
	fs := flag.NewFlagSet("utils "+opts.Utils.Operation, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&opts.Publish.IncludePreReleases, "pre-release", false, "Include pre-releases when fetching the latest release")
	fs.BoolVar(&opts.Global.Verbose, "verbose", false, "Debug output")
	fs.BoolVar(&opts.Global.NoColor, "no-color", false, "Disable colored output")
	fs.BoolVar(&opts.Global.JSON, "json", false, "Machine-readable output (errors as JSON to stderr)")

	// Reorder so flags come before positional args
	reorderedArgs := reorderArgsForFlagSet(remaining, map[string]bool{})
	if err := fs.Parse(reorderedArgs); err != nil {
		opts.Global.Help = true
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

// IsInteractive returns true if the CLI should show interactive prompts.
// False when --quiet or --json is active.
func (o *Options) IsInteractive() bool {
	return !o.Publish.Quiet && !o.Global.JSON
}

// ShouldShowSpinners returns true if spinners/progress should be shown.
// False when --quiet or --json is active (both require clean stderr).
func (o *Options) ShouldShowSpinners() bool {
	return !o.Publish.Quiet && !o.Global.JSON
}

// ValidateChannel returns an error if the channel is invalid.
func (o *PublishOptions) ValidateChannel() error {
	validChannels := map[string]bool{"main": true, "beta": true, "nightly": true, "dev": true}
	if !validChannels[o.Channel] {
		return fmt.Errorf("invalid --channel %q: must be one of main, beta, nightly, dev", o.Channel)
	}
	return nil
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
