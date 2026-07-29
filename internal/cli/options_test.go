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

func TestParseCommand_IdentityKeyAlias(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "identity", "--link-key", "release.jks", "--key-alias", "release"}

	opts := ParseCommand()
	if opts.FlagParseError != nil {
		t.Fatalf("ParseCommand() error: %v", opts.FlagParseError)
	}
	if opts.Identity.KeyAlias != "release" {
		t.Fatalf("KeyAlias = %q, want release", opts.Identity.KeyAlias)
	}
}

func TestParseCommand_IndexerModeImpliesQuietJSONSkipCert(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"zsp", "publish", "--indexer-mode", "--verbose", "app.yaml"}

	opts := ParseCommand()
	if opts.FlagParseError != nil {
		t.Fatalf("ParseCommand() error: %v", opts.FlagParseError)
	}
	if !opts.Publish.IndexerMode {
		t.Fatal("expected Publish.IndexerMode")
	}
	if !opts.Publish.Quiet {
		t.Error("--indexer-mode should imply --quiet")
	}
	if !opts.Publish.SkipCertificateLinking {
		t.Error("--indexer-mode should imply --skip-certificate-linking")
	}
	if !opts.Global.JSON {
		t.Error("--indexer-mode should enable JSON error reporting")
	}
	if !opts.Global.NoColor {
		t.Error("--indexer-mode should imply --no-color")
	}
	if opts.IsInteractive() {
		t.Error("--indexer-mode should not be interactive")
	}
	if opts.ShouldShowSpinners() {
		t.Error("--indexer-mode should not show spinners")
	}
	if opts.Global.Verbose {
		t.Error("--indexer-mode should suppress verbose diagnostics")
	}
	if len(opts.Args) != 1 || opts.Args[0] != "app.yaml" {
		t.Fatalf("Args = %v, want [app.yaml]", opts.Args)
	}
}

func TestPublishOptionsValidateIndexerMode(t *testing.T) {
	tests := []struct {
		name    string
		options PublishOptions
		wantErr bool
	}{
		{name: "not CI"},
		{name: "online publish", options: PublishOptions{IndexerMode: true}},
		{name: "check", options: PublishOptions{IndexerMode: true, Check: true}, wantErr: true},
		{name: "offline", options: PublishOptions{IndexerMode: true, Offline: true}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.options.ValidateIndexerMode()
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateIndexerMode() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
