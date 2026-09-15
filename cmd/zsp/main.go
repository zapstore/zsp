package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/help"
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
				Error: cliErrorBody{Code: "invalid_arguments", Summary: "The command arguments are invalid.", Retryable: false, NextSteps: []cliNextStep{}},
			})
		} else {
			ui.WritePanel(os.Stderr, "error", "Invalid command arguments", nil, []string{options.FlagParseError.Error(), "Run zsp --help to see available commands."})
		}
		return 1
	}
	if options.UnknownSubcommand != "" {
		if options.Global.JSON {
			if err := json.NewEncoder(os.Stderr).Encode(cliErrorDocument{
				OK: false, Operation: options.UnknownSubcommand,
				Error: cliErrorBody{Code: "invalid_arguments", Summary: "The command is not supported.", Retryable: false, NextSteps: []cliNextStep{}},
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
					OK: false, Operation: options.UnknownSubcommand,
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
			case cli.CommandUtils:
				text = help.UtilsHelp()
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
	switch options.Command {
	case cli.CommandPublish:
		return publishCommand(ctx, options)
	case cli.CommandUtils:
		return utilsCommand(options)
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

func utilsCommand(options *cli.Options) int {
	if options.Utils.Operation != "extract-apk" || len(options.Args) != 1 {
		if options.Global.JSON {
			_ = json.NewEncoder(os.Stderr).Encode(cliErrorDocument{
				OK: false, Operation: "extract_apk",
				Error: cliErrorBody{Code: "invalid_arguments", Summary: "Provide exactly one APK path.", Retryable: false, NextSteps: []cliNextStep{}},
			})
			return 1
		}
		ui.WritePanel(os.Stderr, "error", "APK path required", nil, []string{ui.RenderCommand("zsp utils extract-apk <app.apk>")})
		return 1
	}
	if err := extractAPKMetadata(options.Args[0], options.Global.JSON); err != nil {
		if options.Global.JSON {
			_ = json.NewEncoder(os.Stderr).Encode(cliErrorDocument{
				OK: false, Operation: "extract_apk",
				Error: cliErrorBody{Code: "invalid_apk", Summary: "The APK could not be verified.", Retryable: false, NextSteps: []cliNextStep{}},
			})
			return 1
		}
		ui.WriteError(os.Stderr, ui.SanitizeErrorMessage(err))
		return 1
	}
	return 0
}

type apkMetadataDocument struct {
	PackageID       string   `json:"package_id"`
	VersionName     string   `json:"version_name"`
	VersionCode     int64    `json:"version_code"`
	MinSDK          int32    `json:"min_sdk"`
	TargetSDK       int32    `json:"target_sdk"`
	Label           string   `json:"label"`
	Architectures   []string `json:"architectures"`
	CertificateHash string   `json:"certificate_hash"`
	SHA256          string   `json:"sha256"`
	Size            int64    `json:"size"`
}

func extractAPKMetadata(path string, envelope bool) error {
	info, err := apk.Parse(path)
	if err != nil {
		return fmt.Errorf("parse APK: %w", err)
	}
	output := apkMetadataDocument{
		PackageID: info.PackageID, VersionName: info.VersionName, VersionCode: info.VersionCode,
		MinSDK: info.MinSDK, TargetSDK: info.TargetSDK, Label: info.Label,
		Architectures: info.Architectures, CertificateHash: info.CertFingerprint,
		SHA256: info.SHA256, Size: info.FileSize,
	}
	if envelope {
		return json.NewEncoder(os.Stdout).Encode(struct {
			OK        bool                `json:"ok"`
			Operation string              `json:"operation"`
			APK       apkMetadataDocument `json:"apk"`
		}{true, "extract_apk", output})
	}
	return json.NewEncoder(os.Stdout).Encode(output)
}
