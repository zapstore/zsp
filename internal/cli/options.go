// Package cli handles command-line interface concerns.
package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/zapstore/zsp/internal/help"
)

// Options holds all CLI configuration options.
type Options struct {
	// Source options
	RepoURL       string
	ReleaseSource string
	Metadata      []string
	Match         string
	Commit        string
	ConfigPath    string

	// Behavior flags
	Yes              bool
	DryRun           bool
	Quiet            bool
	Verbose          bool
	NoColor          bool
	SkipPreview      bool
	OverwriteRelease bool
	OverwriteApp     bool
	Legacy           bool

	// Mode flags
	ExtractAPK bool
	CheckAPK   bool
	Wizard     bool
	Version    bool
	Help       bool

	// Server options
	Port int
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

// ParseFlags parses command-line flags and returns Options.
// Returns the options and any remaining positional arguments.
func ParseFlags() (*Options, []string) {
	opts := &Options{}
	var metadataFlags stringSliceFlag

	flag.StringVar(&opts.RepoURL, "r", "", "Repository URL (quick mode)")
	flag.StringVar(&opts.ReleaseSource, "s", "", "Release source URL (defaults to -r)")
	flag.Var(&metadataFlags, "m", "Fetch metadata from source (can be repeated: -m github -m fdroid)")
	flag.StringVar(&opts.Match, "match", "", "Regex pattern to filter APK assets")
	flag.StringVar(&opts.Commit, "commit", "", "Git commit hash for reproducible builds")
	flag.BoolVar(&opts.Yes, "y", false, "Skip confirmations (auto-yes)")
	flag.BoolVar(&opts.DryRun, "dry-run", false, "Do everything except upload/publish")
	flag.BoolVar(&opts.DryRun, "n", false, "Do everything except upload/publish (alias for --dry-run)")
	flag.BoolVar(&opts.Quiet, "quiet", false, "Minimal output, no prompts (implies -y)")
	flag.BoolVar(&opts.Verbose, "verbose", false, "Debug output")
	flag.BoolVar(&opts.NoColor, "no-color", false, "Disable colored output")
	flag.BoolVar(&opts.ExtractAPK, "extract-apk", false, "Extract APK metadata as JSON (local APK only)")
	flag.BoolVar(&opts.CheckAPK, "check-apk", false, "Verify config fetches and parses an arm64-v8a APK (exit 0 on success)")
	flag.BoolVar(&opts.SkipPreview, "skip-preview", false, "Skip the browser preview prompt")
	flag.IntVar(&opts.Port, "port", 0, "Custom port for browser preview/signing (default: 17007 for signing, 17008 for preview)")
	flag.BoolVar(&opts.OverwriteRelease, "overwrite-release", false, "Bypass cache and re-publish even if release unchanged")
	flag.BoolVar(&opts.OverwriteApp, "overwrite-app", false, "Re-fetch metadata even if app already exists on relays")
	flag.BoolVar(&opts.Wizard, "wizard", false, "Run interactive wizard (uses existing config as defaults)")
	flag.BoolVar(&opts.Legacy, "legacy", true, "Use legacy event format for relay.zapstore.dev compatibility")
	flag.BoolVar(&opts.Version, "v", false, "Print version and exit")
	flag.BoolVar(&opts.Version, "version", false, "Print version and exit")
	flag.BoolVar(&opts.Help, "h", false, "Show help")
	flag.BoolVar(&opts.Help, "help", false, "Show help")

	flag.Usage = func() {
		fmt.Fprint(os.Stderr, help.QuickReference())
	}

	// Handle --help before flag parsing to support search queries
	if helpArgs := extractHelpArgs(); helpArgs != nil {
		help.HandleHelp(helpArgs)
		os.Exit(0)
	}

	reorderArgs()
	flag.Parse()

	opts.Metadata = metadataFlags

	// Quiet implies yes
	if opts.Quiet {
		opts.Yes = true
	}

	return opts, flag.Args()
}

// extractHelpArgs checks if --help or -h is in args and returns any search query.
// Returns nil if no help flag found, empty slice for help without query.
func extractHelpArgs() []string {
	args := os.Args[1:]

	for i, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "-help" {
			// Collect any arguments after the help flag as search query
			if i+1 < len(args) {
				return args[i+1:]
			}
			return []string{} // Help without query
		}
	}

	return nil // No help flag
}

// reorderArgs moves flags before positional arguments so flag.Parse() works
// regardless of argument order (e.g., "zsp config.yaml --dry-run" works).
func reorderArgs() {
	args := os.Args[1:]
	var flags, positional []string

	// Flags that take a value argument
	valuedFlags := map[string]bool{
		"-r": true, "-s": true, "-m": true, "--match": true, "--commit": true, "--port": true,
	}

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

	os.Args = append([]string{os.Args[0]}, append(flags, positional...)...)
}

// IsInteractive returns true if the CLI should be interactive.
func (o *Options) IsInteractive() bool {
	return !o.Quiet && !o.Yes
}

// ShouldShowSpinners returns true if spinners/progress should be shown.
func (o *Options) ShouldShowSpinners() bool {
	return !o.Quiet
}
