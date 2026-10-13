package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/help"
	"github.com/zapstore/zsp/internal/sanitize"
	"github.com/zapstore/zsp/internal/ui"
)

var version = "dev"

func main() {
	handler := cli.NewSignalHandler()
	defer handler.Stop()
	os.Exit(run(handler))
}

func run(handler *cli.SignalHandler) int {
	ctx := handler.Context()
	ui.SetContext(ctx)
	options := cli.ParseCommand()
	if options.FlagParseError != nil {
		if options.Global.JSON {
			_ = json.NewEncoder(os.Stderr).Encode(cliErrorDocument{
				OK: false, Operation: string(options.Command),
				Error: cliErrorBody{Code: "invalid_arguments", Summary: "The command arguments are invalid.", Detail: ui.SanitizeErrorMessage(options.FlagParseError), Retryable: false},
			})
		} else {
			ui.WritePanel(os.Stderr, "error", "Invalid command arguments", nil, []string{options.FlagParseError.Error(), "Run zsp --help to see available commands."})
		}
		return 1
	}
	if options.UnknownSubcommand != "" {
		if options.Global.JSON {
			if err := json.NewEncoder(os.Stderr).Encode(cliErrorDocument{
				OK: false, Operation: sanitize.Text(options.UnknownSubcommand),
				Error: cliErrorBody{Code: "invalid_arguments", Summary: "The command is not supported.", Retryable: false},
			}); err != nil {
				return 1
			}
			return 1
		}
		ui.WritePanel(os.Stderr, "error", "Unknown command", []ui.KeyValue{{Key: "Command", Value: options.UnknownSubcommand}}, []string{"Run zsp --help to see available commands."})
		return 1
	}
	if options.Global.JSON {
		options.Global.NoColor = true
	}
	if options.Global.NoColor {
		ui.SetNoColor(true)
	}
	if options.Global.Verbose && !options.Global.JSON {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
		slog.Debug("parsed command", "command", options.Command)
	}
	if options.Global.Version {
		if options.Global.JSON {
			_ = json.NewEncoder(os.Stdout).Encode(struct {
				OK        bool   `json:"ok"`
				Operation string `json:"operation"`
				Version   string `json:"version"`
			}{true, "version", buildVersion()})
			return 0
		}
		fmt.Println(buildVersion())
		return 0
	}
	if options.Global.Help {
		if options.UnknownSubcommand != "" {
			if options.Global.JSON {
				_ = json.NewEncoder(os.Stderr).Encode(cliErrorDocument{
					OK: false, Operation: sanitize.Text(options.UnknownSubcommand),
					Error: cliErrorBody{Code: "invalid_arguments", Summary: "The command is not supported.", Retryable: false},
				})
				return 1
			}
			ui.WritePanel(os.Stderr, "error", "Unknown command", []ui.KeyValue{{Key: "Command", Value: options.UnknownSubcommand}}, []string{"Run zsp --help to see available commands."})
		}
		if options.Global.JSON {
			text := help.RootHelp()
			command := options.Command
			if command == cli.CommandNone && len(options.Args) > 0 {
				command = cli.Command(options.Args[0])
			}
			switch command {
			case cli.CommandPublish:
				text = help.PublishHelp()
			case cli.CommandCheck:
				text = help.CheckHelp()
			}
			_ = json.NewEncoder(os.Stdout).Encode(struct {
				OK        bool   `json:"ok"`
				Operation string `json:"operation"`
				Help      string `json:"help"`
			}{true, "help", text})
			return 0
		}
		help.HandleHelp(options.Command, options.Args)
		if options.UnknownSubcommand != "" {
			return 1
		}
		return 0
	}
	if options.Command == cli.CommandSkill {
		fmt.Print(zsp.Skill)
		return 0
	}
	if err := config.CheckDotEnvPermissions(); err != nil {
		if options.Global.JSON {
			_ = json.NewEncoder(os.Stderr).Encode(cliErrorDocument{
				OK: false, Operation: string(options.Command),
				Error: cliErrorBody{Code: "insecure_env", Summary: ".env is readable by other users.", Detail: err.Error(), Retryable: false},
			})
		} else {
			ui.WritePanel(os.Stderr, "error", ".env is readable by other users", nil, []string{err.Error()})
		}
		return 1
	}
	switch options.Command {
	case cli.CommandPublish:
		return publishCommand(ctx, options)
	case cli.CommandCheck:
		return checkCommand(ctx, options)
	case cli.CommandWizard:
		return runWizard(ctx)
	default:
		help.HandleHelp(cli.CommandNone, nil)
		return 0
	}
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}
