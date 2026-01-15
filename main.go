package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/help"
	"github.com/zapstore/zsp/internal/picker"
	"github.com/zapstore/zsp/internal/source"
	"github.com/zapstore/zsp/internal/ui"
	"github.com/zapstore/zsp/internal/workflow"
)

var version = "dev"

func main() {
	// Set up signal handler first - this handles Ctrl+C globally
	sigHandler := cli.NewSignalHandler()
	defer sigHandler.Stop()

	// Run the main logic
	exitCode := run(sigHandler)

	// Exit with appropriate code
	os.Exit(exitCode)
}

func run(sigHandler *cli.SignalHandler) int {
	ctx := sigHandler.Context()

	// Parse CLI flags
	opts, args := cli.ParseFlags()

	// Handle help flag
	if opts.Help {
		help.HandleHelp(nil)
		return 0
	}

	// Handle version flag
	if opts.Version {
		fmt.Print(ui.Title(ui.Logo))
		fmt.Printf("zsp version %s\n", version)
		return 0
	}

	// Handle no-color flag
	if opts.NoColor {
		ui.SetNoColor(true)
	}

	// Handle extract-apk flag
	if opts.ExtractAPK {
		if len(args) == 0 || !strings.HasSuffix(strings.ToLower(args[0]), ".apk") {
			fmt.Fprintln(os.Stderr, "Error: --extract-apk requires a local APK file as argument")
			return 1
		}
		if err := extractAPKMetadata(args[0]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	// Handle check-apk flag
	if opts.CheckAPK {
		if err := checkAPK(ctx, opts, args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

	// Load configuration
	cfg, err := loadConfig(opts, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid configuration: %v\n", err)
		return 1
	}

	// Apply CLI flag overrides
	if opts.Match != "" {
		cfg.Match = opts.Match
	}
	if opts.Commit != "" {
		cfg.Commit = opts.Commit
	}

	// Run the publish workflow
	if err := runPublish(ctx, opts, cfg); err != nil {
		if errors.Is(err, workflow.ErrNothingToDo) {
			return 0
		}
		if errors.Is(err, context.Canceled) {
			return 130 // Standard exit code for Ctrl+C
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	return 0
}

// runPublish executes the publish workflow.
func runPublish(ctx context.Context, opts *cli.Options, cfg *config.Config) error {
	publisher, err := workflow.NewPublisher(opts, cfg)
	if err != nil {
		return err
	}
	defer publisher.Close()

	return publisher.Execute(ctx)
}

// loadConfig loads configuration from various sources.
func loadConfig(opts *cli.Options, args []string) (*config.Config, error) {
	// --wizard flag: run wizard with optional existing config as defaults
	if opts.Wizard {
		if opts.Quiet {
			return nil, fmt.Errorf("--wizard cannot be used with --quiet")
		}
		var defaults *config.Config
		configPath := "zapstore.yaml"
		if len(args) > 0 && !strings.HasSuffix(strings.ToLower(args[0]), ".apk") {
			configPath = args[0]
		}
		if cfg, err := config.Load(configPath); err == nil {
			defaults = cfg
		}
		return config.RunWizard(defaults)
	}

	// Quick mode with APK file as positional argument
	if len(args) > 0 && strings.HasSuffix(strings.ToLower(args[0]), ".apk") {
		return loadAPKConfig(opts, args[0])
	}

	// Quick mode with -r flag only (no APK)
	if opts.RepoURL != "" {
		return loadRepoConfig(opts)
	}

	// Config file as positional argument
	if len(args) > 0 {
		return config.Load(args[0])
	}

	// Check for stdin
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		return config.Parse(os.Stdin)
	}

	// Look for default config file
	if _, err := os.Stat("zapstore.yaml"); err == nil {
		return config.Load("zapstore.yaml")
	}

	// Launch interactive wizard
	if opts.Quiet {
		return nil, fmt.Errorf("no configuration provided. Use 'zsp <config.yaml>' or 'zsp -r <repo-url>'")
	}

	return config.RunWizard(nil)
}

// loadAPKConfig creates config from a local APK path with optional -r and -s flags.
func loadAPKConfig(opts *cli.Options, apkPath string) (*config.Config, error) {
	cfg := &config.Config{
		Local: apkPath,
	}

	if opts.RepoURL != "" {
		repoURL := normalizeRepoURL(opts.RepoURL)
		if err := config.ValidateURL(repoURL); err != nil {
			return nil, fmt.Errorf("invalid -r URL: %w", err)
		}
		cfg.Repository = repoURL
	}

	if opts.ReleaseSource != "" {
		sourceURL := normalizeRepoURL(opts.ReleaseSource)
		if err := config.ValidateURL(sourceURL); err != nil {
			return nil, fmt.Errorf("invalid -s URL: %w", err)
		}
		cfg.ReleaseSource = &config.ReleaseSource{URL: sourceURL}
	}

	return cfg, nil
}

// loadRepoConfig creates config from -r flag.
func loadRepoConfig(opts *cli.Options) (*config.Config, error) {
	repoURL := normalizeRepoURL(opts.RepoURL)
	if err := config.ValidateURL(repoURL); err != nil {
		return nil, fmt.Errorf("invalid -r URL: %w", err)
	}

	cfg := &config.Config{
		Repository: repoURL,
	}

	if opts.ReleaseSource != "" {
		sourceURL := normalizeRepoURL(opts.ReleaseSource)
		if err := config.ValidateURL(sourceURL); err != nil {
			return nil, fmt.Errorf("invalid -s URL: %w", err)
		}
		cfg.ReleaseSource = &config.ReleaseSource{URL: sourceURL}
	}

	return cfg, nil
}

// normalizeRepoURL ensures the repository URL has a scheme.
func normalizeRepoURL(url string) string {
	if !strings.Contains(url, "://") {
		return "https://" + url
	}
	return url
}

// extractAPKMetadata parses an APK and outputs its metadata as JSON.
func extractAPKMetadata(apkPath string) error {
	apkInfo, err := apk.Parse(apkPath)
	if err != nil {
		return fmt.Errorf("failed to parse APK: %w", err)
	}

	output := map[string]any{
		"package_id":       apkInfo.PackageID,
		"version_name":     apkInfo.VersionName,
		"version_code":     apkInfo.VersionCode,
		"min_sdk":          apkInfo.MinSDK,
		"target_sdk":       apkInfo.TargetSDK,
		"label":            apkInfo.Label,
		"architectures":    apkInfo.Architectures,
		"cert_fingerprint": apkInfo.CertFingerprint,
		"file_path":        apkInfo.FilePath,
		"file_size":        apkInfo.FileSize,
		"sha256":           apkInfo.SHA256,
	}

	if apkInfo.Icon != nil {
		apkBase := strings.TrimSuffix(apkPath, ".apk")
		iconPath := apkBase + "_icon.png"
		if err := os.WriteFile(iconPath, apkInfo.Icon, 0644); err != nil {
			return fmt.Errorf("failed to write icon: %w", err)
		}
		output["icon"] = iconPath
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

// checkAPK verifies that a configuration correctly fetches and processes an arm64-v8a APK.
func checkAPK(ctx context.Context, opts *cli.Options, args []string) error {
	cfg, err := loadConfig(opts, args)
	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	src, err := source.NewWithOptions(cfg, source.Options{
		BaseDir:   cfg.BaseDir,
		SkipCache: true,
	})
	if err != nil {
		return err
	}

	release, err := src.FetchLatestRelease(ctx)
	if err != nil {
		return err
	}

	apkAssets := picker.FilterAPKs(release.Assets)
	if len(apkAssets) == 0 {
		return fmt.Errorf("no APK files found in release")
	}

	if cfg.Match != "" {
		apkAssets, err = picker.FilterByMatch(apkAssets, cfg.Match)
		if err != nil {
			return err
		}
		if len(apkAssets) == 0 {
			return fmt.Errorf("no APK files match pattern: %s", cfg.Match)
		}
	}

	var selectedAsset *source.Asset
	if len(apkAssets) == 1 {
		selectedAsset = apkAssets[0]
	} else {
		ranked := picker.DefaultModel.RankAssets(apkAssets)
		selectedAsset = ranked[0].Asset
	}

	var apkPath string
	if selectedAsset.LocalPath != "" {
		apkPath = selectedAsset.LocalPath
	} else {
		apkPath, err = src.Download(ctx, selectedAsset, "", nil)
		if err != nil {
			return err
		}
	}

	apkInfo, err := apk.Parse(apkPath)
	if err != nil {
		return err
	}

	if !apkInfo.IsArm64() {
		return fmt.Errorf("APK does not support arm64-v8a architecture (found: %v)", apkInfo.Architectures)
	}

	fmt.Println(apkInfo.PackageID)
	return nil
}
