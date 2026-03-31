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

func TestParseCommand_UnknownSubcommandSetsHelpAndMarker(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "typo"}

	opts := ParseCommand()
	if !opts.Global.Help {
		t.Fatal("expected Global.Help for unknown subcommand")
	}
	if opts.UnknownSubcommand != "typo" {
		t.Fatalf("UnknownSubcommand = %q, want typo", opts.UnknownSubcommand)
	}
}
