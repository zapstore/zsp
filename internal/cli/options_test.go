package cli

import (
	"flag"
	"os"
	"slices"
	"testing"
)

func TestParseCommand_InvalidPublishFlagSetsFlagParseError(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "publish", "--not-a-defined-flag"}

	opts := ParseCommand()
	if opts.FlagParseError == nil {
		t.Fatal("expected FlagParseError for unknown flag")
	}
	if opts.Global.Help {
		t.Error("invalid flag must not set Global.Help (would exit 0 as help)")
	}
}

func TestParseCommand_NoArgumentsStartsWizard(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp"}

	opts := ParseCommand()
	if opts.Command != CommandWizard {
		t.Fatalf("Command = %q, want %q", opts.Command, CommandWizard)
	}
	if opts.Global.Help || opts.Global.JSON {
		t.Fatalf("no-argument wizard must remain interactive: %+v", opts.Global)
	}
}

func TestParseCommand_OperationalCallsUseJSONOnInvalidArguments(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "publish", "--not-a-defined-flag"}

	opts := ParseCommand()
	if opts.FlagParseError == nil {
		t.Fatal("expected FlagParseError for unknown flag")
	}
	if !opts.Global.JSON {
		t.Fatal("JSON must remain enabled so the runner can emit a structured error")
	}
}

func TestParseCommand_AcceptsRootFlags(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "--no-color", "--verbose", "check", "app.apk"}

	opts := ParseCommand()
	if opts.FlagParseError != nil {
		t.Fatal(opts.FlagParseError)
	}
	if opts.Command != CommandCheck || !opts.Global.JSON || !opts.Global.NoColor || !opts.Global.Verbose || len(opts.Args) != 1 || opts.Args[0] != "app.apk" {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestParseArgs_JSONOnlyWhenAskedOnATerminal(t *testing.T) {
	if opts := parseArgs([]string{"check"}); opts.Global.JSON {
		t.Fatal("check without --json must not force JSON; ParseCommand adds it when stdout is not a terminal")
	}
	for _, args := range [][]string{{"--json", "check"}, {"check", "--json"}, {"publish", "--json", "zapstore.yaml"}} {
		if opts := parseArgs(args); !opts.Global.JSON || opts.FlagParseError != nil {
			t.Fatalf("parseArgs(%v) = %+v, want JSON", args, opts)
		}
	}
}

func TestParseArgs_QuietShortFlag(t *testing.T) {
	opts := parseArgs([]string{"publish", "-q"})
	if opts.FlagParseError != nil || !opts.Publish.Quiet {
		t.Fatalf("parseArgs(publish -q) = %+v", opts)
	}
}

func TestParseArgs_CheckRejectsPublishOnlyFlags(t *testing.T) {
	for _, flag := range []string{"-q", "--skip-preview", "--overwrite-release", "-m"} {
		if opts := parseArgs([]string{"check", flag}); opts.FlagParseError == nil {
			t.Errorf("check accepted %s", flag)
		}
	}
	opts := parseArgs([]string{"check", "--apk-hash", "abc", "-r", "github.com/example/app", "zapstore.yaml"})
	if opts.FlagParseError != nil || opts.Publish.APKHash != "abc" || opts.Publish.RepoURL != "github.com/example/app" {
		t.Fatalf("check source flags = %+v", opts)
	}
}

func TestParseArgs_RemovedCommands(t *testing.T) {
	opts := parseArgs([]string{"utils", "extract-apk", "--icon-path", "icon.png", "app.apk"})
	if opts.FlagParseError != nil || opts.Command != CommandUtils || opts.Utils.Operation != "extract-apk" || opts.Utils.IconPath != "icon.png" || len(opts.Args) != 1 || opts.Args[0] != "app.apk" {
		t.Fatalf("extract-apk = %+v", opts)
	}
	if opts := parseArgs([]string{"utils", "find-unsigned", "build"}); opts.FlagParseError != nil || opts.Utils.Operation != "find-unsigned" || len(opts.Args) != 1 {
		t.Fatalf("find-unsigned = %+v", opts)
	}
	if opts := parseArgs([]string{"publish", "--check"}); opts.FlagParseError == nil {
		t.Fatal("publish --check must be rejected")
	}
}

func TestParseCommand_AcceptsAppCreatedAtRelease(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "publish", "--app-created-at-release", "zapstore.yaml"}

	opts := ParseCommand()
	if opts.FlagParseError != nil {
		t.Fatal(opts.FlagParseError)
	}
	if !opts.Publish.AppCreatedAtRelease {
		t.Fatal("expected AppCreatedAtRelease")
	}
}

func TestParseCommand_AcceptsOverwriteAppEvent(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "publish", "--overwrite-app-event", "zapstore.yaml"}

	opts := ParseCommand()
	if opts.FlagParseError != nil {
		t.Fatal(opts.FlagParseError)
	}
	if !opts.Publish.OverwriteAppEvent {
		t.Fatal("expected OverwriteAppEvent")
	}
}

func TestParseCommand_AcceptsChannelFlags(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "publish", "--channel", "nightly", "--prerelease-channel", "beta"}

	opts := ParseCommand()
	if opts.FlagParseError != nil {
		t.Fatal(opts.FlagParseError)
	}
	if opts.Publish.Channel != "nightly" {
		t.Errorf("Channel = %q, want nightly", opts.Publish.Channel)
	}
	if opts.Publish.PrereleaseChannel != "beta" {
		t.Errorf("PrereleaseChannel = %q, want beta", opts.Publish.PrereleaseChannel)
	}
}

func TestParseCommand_UnknownSubcommandKeepsJSONAndMarker(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "typo"}

	opts := ParseCommand()
	if opts.Global.Help {
		t.Fatal("unknown subcommand must not become a help request")
	}
	if !opts.Global.JSON {
		t.Fatal("unknown operational command must keep JSON enabled")
	}
	if opts.UnknownSubcommand != "typo" {
		t.Fatalf("UnknownSubcommand = %q, want typo", opts.UnknownSubcommand)
	}
}

func TestParseCommand_RejectsLegacyIdentityCommand(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "identity", "create", "--keystore", "release.jks", "--delegate", "npub1example", "--source", "github.com/example/app"}

	opts := ParseCommand()
	if opts.UnknownSubcommand != "identity" {
		t.Fatalf("UnknownSubcommand = %q, want identity", opts.UnknownSubcommand)
	}
}

func TestParseCommand_RejectsRemovedIndexerMode(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "publish", "--indexer-mode", "--verbose", "app.yaml"}

	opts := ParseCommand()
	if opts.FlagParseError == nil {
		t.Fatal("removed --indexer-mode should be rejected")
	}
}

func TestReorderArgsForFlagSet(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"flags after positional", []string{"app.yaml", "-r", "URL"}, []string{"-r", "URL", "app.yaml"}},
		{"single dash long flag consumes value", []string{"-match", "foo", "-r", "URL"}, []string{"-match", "foo", "-r", "URL"}},
		{"double dash short flag consumes value", []string{"--r", "URL", "app.yaml"}, []string{"--r", "URL", "app.yaml"}},
		{"valued flag consumes a dash-prefixed value", []string{"-match", "-r", "URL"}, []string{"-match", "-r", "URL"}},
		{"equals form takes no extra argument", []string{"--match=foo", "app.yaml"}, []string{"--match=foo", "app.yaml"}},
		{"bool flag leaves next argument positional", []string{"-q", "app.yaml"}, []string{"-q", "app.yaml"}},
		{"bool flag between positionals", []string{"a.yaml", "--skip-preview", "b.yaml"}, []string{"--skip-preview", "a.yaml", "b.yaml"}},
		{"repeatable valued flag", []string{"-m", "github", "-m", "fdroid"}, []string{"-m", "github", "-m", "fdroid"}},
		{"double dash ends flags", []string{"-q", "--", "-r", "app.yaml"}, []string{"-q", "--", "-r", "app.yaml"}},
		{"flag after double dash is positional", []string{"app.yaml", "--", "-q"}, []string{"--", "app.yaml", "-q"}},
		{"lone dash is positional", []string{"-", "-q"}, []string{"-q", "-"}},
		{"valued flag at the end", []string{"app.yaml", "-r"}, []string{"-r", "app.yaml"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("publish", flag.ContinueOnError)
			fs.String("r", "", "")
			fs.String("match", "", "")
			fs.Var(new(stringSliceFlag), "m", "")
			fs.Bool("q", false, "")
			fs.Bool("skip-preview", false, "")
			got := reorderArgsForFlagSet(tt.args, fs)
			if !slices.Equal(got, tt.want) {
				t.Errorf("reorderArgsForFlagSet(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestParseArgs_ValuedFlagsBindDashPrefixedValues(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantMatch string
		wantRepo  string
		wantArgs  []string
		wantErr   bool
	}{
		{"single dash match before repo", []string{"publish", "-match", "foo", "-r", "https://example.com/a/b"}, "foo", "https://example.com/a/b", nil, false},
		{"match value starting with dash", []string{"publish", "-match", "-beta", "-r", "https://example.com/a/b"}, "-beta", "https://example.com/a/b", nil, false},
		{"positional before flags", []string{"publish", "app.yaml", "--match", "foo"}, "foo", "", []string{"app.yaml"}, false},
		{"double dash keeps dash-prefixed positional", []string{"publish", "--match", "foo", "--", "-weird.yaml"}, "foo", "", []string{"-weird.yaml"}, false},
		{"unknown flag after positional", []string{"publish", "app.yaml", "--nope"}, "", "", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := parseArgs(tt.args)
			if (opts.FlagParseError != nil) != tt.wantErr {
				t.Fatalf("parseArgs(%q) error = %v, wantErr %v", tt.args, opts.FlagParseError, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if opts.Publish.Match != tt.wantMatch || opts.Publish.RepoURL != tt.wantRepo || !slices.Equal(opts.Args, tt.wantArgs) {
				t.Errorf("parseArgs(%q) = match %q repo %q args %q, want match %q repo %q args %q",
					tt.args, opts.Publish.Match, opts.Publish.RepoURL, opts.Args, tt.wantMatch, tt.wantRepo, tt.wantArgs)
			}
		})
	}
}
