package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/help"
	"github.com/zapstore/zsp/internal/identity"
	nostrpkg "github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/picker"
	"github.com/zapstore/zsp/internal/source"
	"github.com/zapstore/zsp/internal/ui"
	"github.com/zapstore/zsp/internal/workflow"
)

// version is set via -ldflags at build time, or auto-detected from Go module info
var version = "dev"

// getVersion returns the version string, preferring Go's embedded build info
// (set when installed via `go install module@version`), falling back to
// the ldflags-set version, or "dev" if neither is available.
func getVersion() string {
	// If version was set via ldflags to something other than "dev", use it
	if version != "dev" {
		return version
	}

	// Try to get version from Go's embedded build info
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	return version
}

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

	// Set the global UI context so prompts respect Ctrl+C
	ui.SetContext(ctx)

	// Parse CLI command and flags
	opts := cli.ParseCommand()

	// Set version for UI rendering
	ui.SetVersion(getVersion())

	// Handle no-color flag (global)
	if opts.Global.NoColor {
		ui.SetNoColor(true)
	}

	// Handle version flag
	if opts.Global.Version {
		fmt.Print(ui.RenderLogo())
		return 0
	}

	// Handle help flag at root or for subcommand
	if opts.Global.Help {
		help.HandleHelp(opts.Command, opts.Args)
		return 0
	}

	// Dispatch to subcommand
	switch opts.Command {
	case cli.CommandPublish:
		return runPublishCommand(ctx, opts)
	case cli.CommandIdentity:
		return runIdentityCommand(ctx, opts)
	case cli.CommandAPK:
		return runAPKCommand(ctx, opts)
	default:
		// No subcommand - show help
		help.HandleHelp(cli.CommandNone, nil)
		return 0
	}
}

// runPublishCommand handles the publish subcommand.
func runPublishCommand(ctx context.Context, opts *cli.Options) int {
	// Handle no-color for subcommand
	if opts.Global.NoColor {
		ui.SetNoColor(true)
	}

	// Handle --check flag (validates config without publishing)
	if opts.Publish.Check {
		if err := checkAPK(ctx, opts); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

	// Load configuration
	cfg, err := loadConfig(&opts.Publish, opts.Args)
	if err != nil {
		// Wizard completed successfully - user should run the displayed command
		if errors.Is(err, config.ErrWizardComplete) {
			return 0
		}
		// User interrupted with Ctrl+C
		if errors.Is(err, ui.ErrInterrupted) {
			return 130
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", ui.SanitizeErrorMessage(err))
		return 1
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid configuration: %s\n", ui.SanitizeErrorMessage(err))
		return 1
	}

	// Validate CLI options
	if err := opts.Publish.ValidateChannel(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", ui.SanitizeErrorMessage(err))
		return 1
	}

	// Apply CLI flag overrides
	if opts.Publish.Match != "" {
		cfg.Match = opts.Publish.Match
	}

	// Run the publish workflow
	if err := runPublish(ctx, opts, cfg); err != nil {
		if errors.Is(err, workflow.ErrNothingToDo) {
			return 0
		}
		if errors.Is(err, context.Canceled) {
			return 130 // Standard exit code for Ctrl+C
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", ui.SanitizeErrorMessage(err))
		return 1
	}

	return 0
}

// runIdentityCommand handles the identity subcommand.
func runIdentityCommand(ctx context.Context, opts *cli.Options) int {
	// Handle no-color for subcommand
	if opts.Global.NoColor {
		ui.SetNoColor(true)
	}

	// Determine which identity operation
	if opts.Identity.LinkKey != "" {
		if err := runLinkKey(ctx, opts); err != nil {
			if errors.Is(err, ui.ErrInterrupted) || errors.Is(err, context.Canceled) {
				return 130
			}
			if errors.Is(err, identity.ErrJKSFormat) {
				fmt.Fprint(os.Stderr, identity.JKSConversionHelp(opts.Identity.LinkKey))
				return 1
			}
			fmt.Fprintf(os.Stderr, "Error: %s\n", ui.SanitizeErrorMessage(err))
			return 1
		}
		return 0
	}

	if opts.Identity.Verify != "" {
		if err := runVerifyIdentity(ctx, opts); err != nil {
			if errors.Is(err, ui.ErrInterrupted) || errors.Is(err, context.Canceled) {
				return 130
			}
			if errors.Is(err, identity.ErrJKSFormat) {
				fmt.Fprint(os.Stderr, identity.JKSConversionHelp(opts.Identity.Verify))
				return 1
			}
			fmt.Fprintf(os.Stderr, "Error: %s\n", ui.SanitizeErrorMessage(err))
			return 1
		}
		return 0
	}

	// No operation specified - show help
	help.HandleHelp(cli.CommandIdentity, nil)
	return 0
}

// runAPKCommand handles the apk subcommand.
func runAPKCommand(ctx context.Context, opts *cli.Options) int {
	// Handle no-color for subcommand
	if opts.Global.NoColor {
		ui.SetNoColor(true)
	}

	if opts.APK.Extract {
		if len(opts.Args) == 0 || !strings.HasSuffix(strings.ToLower(opts.Args[0]), ".apk") {
			fmt.Fprintln(os.Stderr, "Error: --extract requires a local APK file as argument")
			fmt.Fprintln(os.Stderr, "Usage: zsp apk --extract <file.apk>")
			return 1
		}
		if err := extractAPKMetadata(opts.Args[0]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", ui.SanitizeErrorMessage(err))
			return 1
		}
		return 0
	}

	// No operation specified - show help
	help.HandleHelp(cli.CommandAPK, nil)
	return 0
}

// runPublish executes the publish workflow.
func runPublish(ctx context.Context, opts *cli.Options, cfg *config.Config) error {
	pub, err := workflow.NewPublisher(opts, cfg)
	if err != nil {
		return err
	}
	defer pub.Close()

	return pub.Execute(ctx)
}

// loadConfig loads configuration from various sources.
func loadConfig(opts *cli.PublishOptions, args []string) (*config.Config, error) {
	// --wizard flag: run wizard with optional existing config as defaults
	if opts.Wizard {
		if opts.Quiet {
			return nil, fmt.Errorf("--wizard cannot be used with --quiet")
		}
		var defaults *config.Config
		configPath := "zapstore.yaml"
		if opts.ConfigFile != "" {
			configPath = opts.ConfigFile
		} else if len(args) > 0 && isYAMLFile(args[0]) {
			configPath = args[0]
		}
		if cfg, err := config.Load(configPath); err == nil {
			defaults = cfg
		}
		return config.RunWizardWithOptions(defaults, config.WizardOptions{
			FetchAPKInfo: fetchAPKInfoForWizard,
		})
	}

	// -c flag: load config from explicit path, positional args are asset files
	if opts.ConfigFile != "" {
		cfg, err := config.Load(opts.ConfigFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load config %s: %w", opts.ConfigFile, err)
		}
		// If positional args are present, they are local asset files
		if len(args) > 0 {
			cfg.LocalAssetFiles = args
		}
		return cfg, nil
	}

	// Quick mode with -r flag only (no APK/binary)
	if len(args) == 0 && opts.RepoURL != "" {
		return loadRepoConfig(opts)
	}

	// YAML config file as positional argument (single arg, must be YAML)
	if len(args) == 1 && isYAMLFile(args[0]) {
		return config.Load(args[0])
	}

	// Local file(s) as positional arguments
	// Single file: `zsp publish app.apk` or `zsp publish ./mybinary`
	// Multiple files: `zsp publish build/*` or `zsp publish a.apk b.apk c.apk`
	if len(args) > 0 {
		return loadLocalAssetFilesConfig(opts, args)
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
		return nil, fmt.Errorf("no configuration provided. Use 'zsp publish -c <config.yaml>' or 'zsp publish -r <repo-url>'")
	}

	return config.RunWizardWithOptions(nil, config.WizardOptions{
		FetchAPKInfo: fetchAPKInfoForWizard,
	})
}

// isYAMLFile returns true if the path has a .yaml or .yml extension.
func isYAMLFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")
}

// fetchAPKInfoForWizard downloads the APK and extracts basic info.
// This is passed as a callback to the wizard since config package can't import source/picker/apk.
func fetchAPKInfoForWizard(cfg *config.Config, matchPattern string) *config.APKBasicInfo {
	ctx := ui.GetContext()

	spinner := ui.NewSpinner("Fetching APK to detect app info...")
	spinner.Start()

	src, err := source.NewWithOptions(cfg, source.Options{})
	if err != nil {
		spinner.StopWithWarning("Could not create source")
		return nil
	}

	release, err := src.FetchLatestRelease(ctx)
	if err != nil {
		spinner.StopWithWarning("Could not fetch release")
		return nil
	}

	// Filter to APKs
	apkAssets := picker.FilterAPKs(release.Assets)
	if len(apkAssets) == 0 {
		spinner.StopWithWarning("No APK found in release")
		return nil
	}

	// Apply match pattern if specified
	if matchPattern != "" {
		apkAssets, err = picker.FilterByMatch(apkAssets, matchPattern)
		if err != nil || len(apkAssets) == 0 {
			spinner.StopWithWarning("No APK matches pattern")
			return nil
		}
	}

	// Pick the best APK
	var selectedAsset *source.Asset
	if len(apkAssets) == 1 {
		selectedAsset = apkAssets[0]
	} else {
		ranked := picker.DefaultModel.RankAssets(apkAssets)
		selectedAsset = ranked[0].Asset
	}

	// Download the APK
	apkPath, err := src.Download(ctx, selectedAsset, "", nil)
	if err != nil {
		spinner.StopWithWarning("Could not download APK")
		return nil
	}

	// Parse the APK
	apkInfo, err := apk.Parse(apkPath)
	if err != nil {
		spinner.StopWithWarning("Could not parse APK")
		return nil
	}

	spinner.StopWithSuccess(fmt.Sprintf("Found app: %s", apkInfo.PackageID))
	return &config.APKBasicInfo{
		PackageID: apkInfo.PackageID,
		AppName:   apkInfo.Label,
	}
}

// loadLocalAssetFilesConfig creates config from one or more local asset files.
// For a single file, uses the legacy ReleaseSource.LocalPath for backward compat.
// For multiple files, populates LocalAssetFiles.
func loadLocalAssetFilesConfig(opts *cli.PublishOptions, args []string) (*config.Config, error) {
	// Validate all files exist
	for _, arg := range args {
		if _, err := os.Stat(arg); err != nil {
			return nil, fmt.Errorf("file not found: %s", arg)
		}
	}

	cfg := &config.Config{}

	if len(args) == 1 {
		// Single file: use legacy local path (backward compat)
		cfg.ReleaseSource = &config.ReleaseSource{LocalPath: args[0]}
	} else {
		// Multiple files: use LocalAssetFiles
		cfg.LocalAssetFiles = args
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
func loadRepoConfig(opts *cli.PublishOptions) (*config.Config, error) {
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
func checkAPK(ctx context.Context, opts *cli.Options) error {
	// For check, we need to load config from args
	var cfg *config.Config
	var err error

	if len(opts.Args) > 0 {
		cfg, err = config.Load(opts.Args[0])
	} else if _, statErr := os.Stat("zapstore.yaml"); statErr == nil {
		cfg, err = config.Load("zapstore.yaml")
	} else {
		return fmt.Errorf("no configuration provided. Use 'zsp publish --check <config.yaml>'")
	}

	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	src, err := source.NewWithOptions(cfg, source.Options{
		BaseDir:            cfg.BaseDir,
		SkipCache:          true,
		IncludePreReleases: opts.Publish.IncludePreReleases,
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
		if opts.Global.Verbose {
			fmt.Fprintf(os.Stderr, "Release: %s\n", release.Version)
			if release.TagName != "" && release.TagName != release.Version {
				fmt.Fprintf(os.Stderr, "Tag: %s\n", release.TagName)
			}
			if release.URL != "" {
				fmt.Fprintf(os.Stderr, "URL: %s\n", release.URL)
			}
			if len(release.Assets) == 0 {
				fmt.Fprintf(os.Stderr, "Assets: (none)\n")
			} else {
				fmt.Fprintf(os.Stderr, "Assets (%d):\n", len(release.Assets))
				for _, asset := range release.Assets {
					fmt.Fprintf(os.Stderr, "  - %s\n", asset.Name)
				}
			}
		}
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

// runLinkKey handles the --link-key flag for cryptographic identity proofs (NIP-C1).
func runLinkKey(ctx context.Context, opts *cli.Options) error {
	filePath := opts.Identity.LinkKey

	// Check file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("file not found: %s", filePath)
	}

	// Parse expiry duration
	expiry, err := cli.ParseExpiryDuration(opts.Identity.LinkKeyExpiry)
	if err != nil {
		return fmt.Errorf("invalid --link-key-expiry: %w", err)
	}

	// 1. Load x509 key and certificate based on file type
	privateKey, cert, err := loadX509FromFile(filePath)
	if err != nil {
		return err
	}

	// Compute SPKIFP from cert
	certSPKIFP, err := identity.ComputeSPKIFP(cert)
	if err != nil {
		return fmt.Errorf("failed to compute SPKIFP: %w", err)
	}

	// Show certificate summary (skip in offline mode for clean output)
	if !opts.Identity.Offline {
		ui.PrintSectionHeader("Certificate Summary")
		ui.PrintKeyValue("SPKIFP", certSPKIFP)
		ui.PrintKeyValue("Validity", fmt.Sprintf("%d year(s)", int(expiry.Hours()/24/365)))
	}

	// 2. Get signWith config
	signWith := config.GetSignWith()
	if signWith == "" {
		if opts.Identity.Offline {
			return fmt.Errorf("SIGN_WITH environment variable required in offline mode")
		}
		ui.PrintSectionHeader("Signing Setup")
		signWith, err = config.PromptSignWith()
		if err != nil {
			return fmt.Errorf("signing setup failed: %w", err)
		}
	}

	// 3. Create publisher with identity-specific relays (only needed for online mode)
	var publisher *nostrpkg.Publisher
	if !opts.Identity.Offline {
		publisher = nostrpkg.NewPublisher(opts.Identity.Relays)
	}

	// 4. Try to get pubkey without opening browser (for nsec/npub/hex)
	// This allows checking existing proofs BEFORE browser opens
	pubkeyHex, canCheckBeforeSigner := extractPubkeyFromSignWith(signWith)

	// Check for existing identity proofs (skip in offline mode)
	if !opts.Identity.Offline && canCheckBeforeSigner {
		checkSpinner := ui.NewSpinner("Checking existing identity proofs...")
		checkSpinner.Start()
		existingProof, err := publisher.FetchIdentityProof(ctx, pubkeyHex, certSPKIFP)
		if err != nil {
			checkSpinner.StopWithWarning("Could not check existing proofs")
		} else if existingProof != nil {
			checkSpinner.StopWithSuccess("Found existing proof (will be replaced)")
		} else {
			checkSpinner.StopWithSuccess("No existing proof for this SPKIFP")
		}
	}

	// 5. Create signer (browser opens here for browser signers)
	signer, err := nostrpkg.NewSignerWithOptions(ctx, signWith, nostrpkg.SignerOptions{})
	if err != nil {
		return fmt.Errorf("failed to create signer: %w", err)
	}
	defer signer.Close()

	// Get pubkey from signer (needed for browser/bunker signers)
	if !canCheckBeforeSigner {
		pubkeyHex = signer.PublicKey()

		// Check for existing proofs (skip in offline mode)
		if !opts.Identity.Offline {
			checkSpinner := ui.NewSpinner("Checking existing identity proofs...")
			checkSpinner.Start()
			existingProof, err := publisher.FetchIdentityProof(ctx, pubkeyHex, certSPKIFP)
			if err != nil {
				checkSpinner.StopWithWarning("Could not check existing proofs")
			} else if existingProof != nil {
				checkSpinner.StopWithSuccess("Found existing proof (will be replaced)")
			} else {
				checkSpinner.StopWithSuccess("No existing proof for this SPKIFP")
			}
		}
	}

	// 6. Generate identity proof
	proof, err := identity.GenerateIdentityProof(privateKey, pubkeyHex, &identity.IdentityProofOptions{
		Expiry: expiry,
	})
	if err != nil {
		return fmt.Errorf("failed to generate identity proof: %w", err)
	}

	// 7. Build kind 30509 identity proof event (CreatedAt must match the signed message)
	identityTags := proof.ToEventTags()
	identityEvent := nostrpkg.BuildIdentityProofEvent(identityTags, pubkeyHex, proof.CreatedAt)

	// 8. Sign the identity event with Nostr key
	if !opts.Identity.Offline {
		signSpinner := ui.NewSpinner("Signing identity event...")
		signSpinner.Start()
		if err := signer.Sign(ctx, identityEvent); err != nil {
			signSpinner.StopWithError("Failed to sign")
			return fmt.Errorf("failed to sign identity event: %w", err)
		}
		signSpinner.StopWithSuccess("Signed identity event")
	} else {
		// Offline mode: sign without spinner for clean output
		if err := signer.Sign(ctx, identityEvent); err != nil {
			return fmt.Errorf("failed to sign identity event: %w", err)
		}
	}

	// 9. Offline mode: output event JSON to stdout and exit
	if opts.Identity.Offline {
		data, err := json.Marshal(identityEvent)
		if err != nil {
			return fmt.Errorf("failed to marshal event: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	// Convert pubkey to npub for display
	npub, err := nip19.EncodePublicKey(pubkeyHex)
	if err != nil {
		return fmt.Errorf("failed to encode public key: %w", err)
	}

	// 10. Interactive menu for preview/publish
	ui.PrintSectionHeader("Ready to Publish")
	fmt.Printf("  SPKIFP: %s\n", proof.SPKIFP)
	fmt.Printf("  Nostr pubkey: %s\n", npub)
	fmt.Printf("  Created: %s\n", proof.CreatedAtTime().Format("2006-01-02 15:04:05 UTC"))
	fmt.Printf("  Expires: %s\n", proof.ExpiryTime().Format("2006-01-02 15:04:05 UTC"))
	fmt.Printf("  Target: %s\n", strings.Join(opts.Identity.Relays, ", "))
	fmt.Println()

	for {
		options := []string{
			"Preview event (JSON)",
			"Publish now",
			"Exit without publishing",
		}

		idx, err := ui.SelectOption("Choose an option:", options, 1)
		if err != nil {
			return fmt.Errorf("selection failed: %w", err)
		}

		switch idx {
		case 0:
			// Preview JSON
			fmt.Println()
			fmt.Printf("%s\n", ui.Bold("Kind 30509 (Cryptographic Identity Proof):"))
			printIdentityEventJSON(identityEvent)
			fmt.Println()
		case 1:
			// Publish
			return publishIdentityProof(ctx, publisher, identityEvent, opts)
		case 2:
			// Exit
			fmt.Println(ui.Warning("Aborted - identity proof was NOT published"))
			return nil
		}
	}
}

// publishIdentityProof publishes the identity proof event to relays.
func publishIdentityProof(ctx context.Context, publisher *nostrpkg.Publisher, event *nostr.Event, opts *cli.Options) error {
	publishSpinner := ui.NewSpinner(fmt.Sprintf("Publishing to %d relays...", len(opts.Identity.Relays)))
	publishSpinner.Start()

	results := publisher.Publish(ctx, event)

	var successCount int
	var failures []string
	for _, r := range results {
		if r.Success {
			successCount++
		} else {
			failures = append(failures, fmt.Sprintf("  ✗ %s: %v", r.RelayURL, r.Error))
		}
	}

	if successCount == len(results) {
		publishSpinner.StopWithSuccess("Published successfully")
	} else if successCount > 0 {
		publishSpinner.StopWithWarning("Published with some failures")
	} else {
		publishSpinner.StopWithError("Failed to publish")
	}

	// Show individual relay results
	for _, r := range results {
		if r.Success {
			fmt.Printf("  ✓ %s\n", r.RelayURL)
		}
	}
	for _, f := range failures {
		fmt.Println(f)
	}

	if successCount == 0 {
		return fmt.Errorf("failed to publish to any relay")
	}

	ui.PrintCompletionSummary(true, fmt.Sprintf("Identity proof published to %d relay(s)", successCount))
	return nil
}

// printIdentityEventJSON prints an identity event as colorized JSON.
func printIdentityEventJSON(event *nostr.Event) {
	data, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Println(ui.ColorizeJSON(string(data)))
}

// runVerifyIdentity handles the --verify flag to verify cryptographic identity proofs (NIP-C1).
func runVerifyIdentity(ctx context.Context, opts *cli.Options) error {
	filePath := opts.Identity.Verify

	// Check file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("file not found: %s", filePath)
	}

	var cert *x509.Certificate
	var err error

	// Check if file is an APK
	lower := strings.ToLower(filePath)
	isAPK := strings.HasSuffix(lower, ".apk")

	if isAPK {
		// Extract certificate from APK
		ui.PrintSectionHeader("APK Certificate")
		cert, err = apk.ExtractCertificate(filePath)
		if err != nil {
			return fmt.Errorf("failed to extract certificate from APK: %w", err)
		}
		fmt.Printf("  File: %s\n", filepath.Base(filePath))
	} else {
		// Load x509 certificate from file
		_, cert, err = loadX509FromFile(filePath)
		if err != nil {
			return err
		}
		ui.PrintSectionHeader("Certificate Loaded")
	}

	// Display certificate info
	if cert.Subject.CommonName != "" {
		fmt.Printf("  Subject: %s\n", cert.Subject.CommonName)
	}
	if len(cert.Subject.Organization) > 0 {
		fmt.Printf("  Organization: %s\n", cert.Subject.Organization[0])
	}
	fmt.Printf("  Valid: %s to %s\n",
		cert.NotBefore.Format("2006-01-02"),
		cert.NotAfter.Format("2006-01-02"))

	// Compute SPKIFP from certificate
	certSPKIFP, err := identity.ComputeSPKIFP(cert)
	if err != nil {
		return fmt.Errorf("failed to compute SPKIFP: %w", err)
	}
	fmt.Printf("  SPKIFP: %s\n", certSPKIFP)

	// 2. Get pubkey to verify - for APKs, prompt for npub directly
	var pubkeyHex string
	if isAPK {
		// For APKs, ask for the npub to verify against
		fmt.Println()
		npubInput, err := ui.Prompt("Enter npub to verify: ")
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		npubInput = strings.TrimSpace(npubInput)
		if !strings.HasPrefix(npubInput, "npub1") {
			return fmt.Errorf("invalid npub format")
		}
		_, pubkey, err := nip19.Decode(npubInput)
		if err != nil {
			return fmt.Errorf("failed to decode npub: %w", err)
		}
		var ok bool
		pubkeyHex, ok = pubkey.(string)
		if !ok {
			return fmt.Errorf("invalid npub")
		}
	} else {
		// For certificate files, use SIGN_WITH
		signWith := config.GetSignWith()
		if signWith == "" {
			ui.PrintSectionHeader("Signing Setup")
			signWith, err = config.PromptSignWith()
			if err != nil {
				return fmt.Errorf("signing setup failed: %w", err)
			}
		}

		// Create signer to get public key
		signer, err := nostrpkg.NewSignerWithOptions(ctx, signWith, nostrpkg.SignerOptions{})
		if err != nil {
			return fmt.Errorf("failed to create signer: %w", err)
		}
		defer signer.Close()

		pubkeyHex = signer.PublicKey()
	}

	// Convert pubkey to npub for display
	npub, err := nip19.EncodePublicKey(pubkeyHex)
	if err != nil {
		return fmt.Errorf("failed to encode public key: %w", err)
	}

	fmt.Println()
	ui.PrintSectionHeader("Fetching Identity Proof")
	fmt.Printf("  Nostr pubkey: %s\n", npub)
	fmt.Printf("  Relays: %v\n", opts.Identity.Relays)

	// 5. Fetch identity proof for this SPKIFP from relays
	publisher := nostrpkg.NewPublisher(opts.Identity.Relays)
	identityEvent, err := publisher.FetchIdentityProof(ctx, pubkeyHex, certSPKIFP)
	if err != nil {
		return fmt.Errorf("failed to fetch identity proof: %w", err)
	}
	if identityEvent == nil {
		return fmt.Errorf("no identity proof found for SPKIFP %s", certSPKIFP)
	}

	fmt.Printf("  Found identity proof (created: %s)\n", identityEvent.CreatedAt.Time().Format("2006-01-02 15:04:05 UTC"))

	// 6. Parse the identity event
	proof, err := identity.ParseIdentityProofFromEvent(identityEvent)
	if err != nil {
		return fmt.Errorf("failed to parse identity event: %w", err)
	}

	fmt.Println()
	ui.PrintSectionHeader("Kind 30509 Event")
	eventJSON, _ := json.MarshalIndent(identityEvent, "", "  ")
	fmt.Println(string(eventJSON))

	// 7. Verify the proof against the certificate
	fmt.Println()
	ui.PrintSectionHeader("Verification Results")
	result := identity.VerifyIdentityProofWithCert(proof, identityEvent, pubkeyHex, cert)

	fmt.Printf("  SPKIFP: %s\n", result.SPKIFP)
	fmt.Printf("  Expiry: %s\n", result.ExpiryTime.Format("2006-01-02 15:04:05 UTC"))

	// Show individual check results
	if result.SPKIFPMatch {
		fmt.Printf("  SPKIFP match: %s\n", ui.Success("YES"))
	} else {
		fmt.Printf("  SPKIFP match: %s\n", ui.Error("NO"))
	}

	if result.Revoked {
		fmt.Printf("  Status: %s", ui.Error("REVOKED"))
		if result.RevokeReason != "" {
			fmt.Printf(" (%s)", result.RevokeReason)
		}
		fmt.Println()
	} else if result.Expired {
		fmt.Printf("  Status: %s\n", ui.Warning("EXPIRED"))
	} else {
		fmt.Printf("  Status: %s\n", ui.Success("ACTIVE"))
	}

	if result.Error != nil {
		fmt.Printf("  Signature: %s\n", ui.Error("INVALID"))
		fmt.Printf("  Error: %v\n", result.Error)
	} else if result.Valid {
		fmt.Printf("  Signature: %s\n", ui.Success("VALID"))
	}

	fmt.Println()
	if result.Valid && result.SPKIFPMatch && !result.Expired && !result.Revoked {
		fmt.Println(ui.Success("✓ Cryptographic identity proof is fully verified"))
	} else if result.Valid && result.SPKIFPMatch && result.Expired {
		fmt.Println(ui.Warning("⚠ Cryptographic identity proof is valid but EXPIRED"))
	} else if result.Valid && result.SPKIFPMatch && result.Revoked {
		fmt.Println(ui.Error("✗ Cryptographic identity proof has been REVOKED"))
		return fmt.Errorf("identity proof revoked")
	} else {
		fmt.Println(ui.Error("✗ Cryptographic identity proof verification failed"))
		return fmt.Errorf("verification failed")
	}

	return nil
}

// extractPubkeyFromSignWith extracts the pubkey from signWith without creating a signer.
// Returns (pubkey, true) for nsec/npub/hex, or ("", false) for browser/bunker.
func extractPubkeyFromSignWith(signWith string) (string, bool) {
	signWith = strings.TrimSpace(signWith)

	// nsec - decode private key and derive pubkey
	if strings.HasPrefix(signWith, "nsec1") {
		_, privkey, err := nip19.Decode(signWith)
		if err != nil {
			return "", false
		}
		privkeyHex, ok := privkey.(string)
		if !ok {
			return "", false
		}
		pubkey, err := nostr.GetPublicKey(privkeyHex)
		if err != nil {
			return "", false
		}
		return pubkey, true
	}

	// npub - decode directly
	if strings.HasPrefix(signWith, "npub1") {
		_, pubkey, err := nip19.Decode(signWith)
		if err != nil {
			return "", false
		}
		pubkeyHex, ok := pubkey.(string)
		if !ok {
			return "", false
		}
		return pubkeyHex, true
	}

	// Hex private key (64 chars)
	if len(signWith) == 64 && isHex(signWith) {
		pubkey, err := nostr.GetPublicKey(signWith)
		if err != nil {
			return "", false
		}
		return pubkey, true
	}

	// browser or bunker - cannot extract pubkey without connecting
	return "", false
}

// isHex checks if a string is valid hexadecimal.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// getKeystorePassword returns the keystore password from KEYSTORE_PASSWORD env var/.env or prompts.
func getKeystorePassword() (string, error) {
	// Check environment variable and .env file first
	if password := config.GetKeystorePassword(); password != "" {
		return password, nil
	}

	// Prompt for password
	return ui.PromptPassword("Keystore password")
}

// loadX509FromFile loads x509 private key and certificate from a file.
// Detects file type by extension and prompts for additional info as needed.
// For PKCS12 files, set KEYSTORE_PASSWORD env var to avoid interactive prompt.
func loadX509FromFile(filePath string) (crypto.PrivateKey, *x509.Certificate, error) {
	lower := strings.ToLower(filePath)

	// Check for JKS files first (by extension or content)
	if strings.HasSuffix(lower, ".jks") || strings.HasSuffix(lower, ".keystore") {
		return nil, nil, identity.ErrJKSFormat
	}

	// PKCS12 keystore (.p12, .pfx)
	if strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".pfx") {
		password, err := getKeystorePassword()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read password: %w", err)
		}
		return identity.LoadPKCS12File(filePath, password)
	}

	// PEM certificate (.pem, .crt, .cer)
	if strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".crt") || strings.HasSuffix(lower, ".cer") {
		// Prompt for private key file
		keyPath, err := ui.PromptDefault("Private key file", strings.TrimSuffix(filePath, ".pem")+"-key.pem")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read input: %w", err)
		}
		keyPath = strings.TrimSpace(keyPath)
		if keyPath == "" {
			return nil, nil, fmt.Errorf("private key file is required")
		}

		return identity.LoadPEM(keyPath, filePath)
	}

	// Unknown extension - try to detect from content
	return nil, nil, fmt.Errorf("unsupported file type: %s (use .p12, .pfx, .pem, or .crt)", filePath)
}
