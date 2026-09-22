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
	os.Args = []string{"zsp", "--no-color", "--verbose", "publish", "--check", "app.apk"}

	opts := ParseCommand()
	if opts.FlagParseError != nil {
		t.Fatal(opts.FlagParseError)
	}
	if opts.Command != CommandPublish || !opts.Global.JSON || !opts.Global.NoColor || !opts.Global.Verbose || !opts.Publish.Check {
		t.Fatalf("unexpected options: %+v", opts)
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
