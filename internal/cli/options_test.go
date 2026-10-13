package cli

import (
	"os"
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
	if opts := parseArgs([]string{"utils", "extract-apk", "app.apk"}); opts.UnknownSubcommand != "utils" {
		t.Fatalf("utils = %+v, want unknown", opts)
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
