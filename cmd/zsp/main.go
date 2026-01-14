package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/blossom"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/picker"
	"github.com/zapstore/zsp/internal/source"
	"github.com/zapstore/zsp/internal/ui"
)

var version = "dev"

// CLI flags
var (
	repoFlag             = flag.String("r", "", "Repository URL (quick mode)")
	releaseSourceFlag    = flag.String("s", "", "Release source URL (defaults to -r)")
	fetchMetadataFlag    stringSliceFlag // Accumulates multiple -m flags
	yesFlag              = flag.Bool("y", false, "Skip confirmations (auto-yes)")
	dryRunFlag           = flag.Bool("dry-run", false, "Do everything except upload/publish")
	quietFlag            = flag.Bool("quiet", false, "Minimal output, no prompts (implies -y)")
	verboseFlag          = flag.Bool("verbose", false, "Debug output")
	noColorFlag          = flag.Bool("no-color", false, "Disable colored output")
	extractFlag          = flag.Bool("extract", false, "Extract APK metadata as JSON (local APK only)")
	checkAPKFlag         = flag.Bool("check-apk", false, "Verify config fetches and parses an arm64-v8a APK (exit 0 on success)")
	previewFlag          = flag.Bool("preview", false, "Show HTML preview in browser before publishing")
	portFlag             = flag.Int("port", 0, "Custom port for browser preview/signing (default: 17007 for signing, 17008 for preview)")
	overwriteReleaseFlag = flag.Bool("overwrite-release", false, "Bypass cache and re-publish even if release unchanged")
	overwriteAppFlag     = flag.Bool("overwrite-app", false, "Re-fetch metadata even if app already exists on relays")
	wizardFlag           = flag.Bool("wizard", false, "Run interactive wizard (uses existing config as defaults)")
	versionFlag          = flag.Bool("v", false, "Print version and exit")
	helpFlag             = flag.Bool("h", false, "Show help")
)

// stringSliceFlag implements flag.Value to accumulate multiple flag values.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func init() {
	flag.Var(&fetchMetadataFlag, "m", "Fetch metadata from source (can be repeated: -m github -m fdroid)")
	flag.Var(&fetchMetadataFlag, "fetch-metadata", "Fetch metadata from source (can be repeated)")
	flag.BoolVar(versionFlag, "version", false, "Print version and exit")
	flag.BoolVar(helpFlag, "help", false, "Show help")
	flag.Usage = usage
}

// reorderArgs moves flags before positional arguments so flag.Parse() works
// regardless of argument order (e.g., "zsp config.yaml --dry-run" works).
func reorderArgs() {
	args := os.Args[1:]
	var flags, positional []string

	// Flags that take a value argument
	valuedFlags := map[string]bool{
		"-r": true, "-s": true, "-m": true, "--fetch-metadata": true, "--port": true,
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

func usage() {
	fmt.Fprintf(os.Stderr, `zsp - Publish Android apps to Nostr relays used by Zapstore

USAGE
  zsp [config.yaml]              Config file (default: ./zapstore.yaml)
  zsp <app.apk> [-r <repo>]      Local APK with optional source repo
  zsp -r <repo>                  Fetch latest release from repo
  zsp <app.apk> --extract        Extract APK metadata as JSON
  zsp                            Interactive wizard (no args, no config)
  zsp --wizard                   Interactive wizard (uses existing config as defaults)

FLAGS
  -r <url>        Repository URL (GitHub/GitLab/F-Droid)
  -s <url>        Release source URL (defaults to -r if not specified)
  -m <source>     Fetch metadata from source (repeatable: -m github -m fdroid)
  -y              Auto-confirm all prompts
  -h, --help      Show this help
  -v, --version   Print version

  --wizard        Run interactive wizard (uses existing config as defaults)
  --fetch-metadata <source>   Same as -m
  --extract       Extract APK metadata as JSON (local APK only)
  --check-apk     Verify config fetches and parses an arm64-v8a APK (exit 0=success)
  --preview       Show HTML preview in browser before publishing
  --port <port>   Custom port for browser preview/signing (default: 17007/17008)
  --overwrite-release  Bypass cache and re-publish even if release unchanged
  --overwrite-app      Re-fetch metadata even if app already exists on relays
  --dry-run       Parse & build events, but don't upload/publish
  --quiet         Minimal output, no prompts (implies -y)
  --verbose       Debug output (show scores, API responses)
  --no-color      Disable colored output

ENVIRONMENT
  SIGN_WITH         Required. Signing method:
                      nsec1...      Direct signing with private key
                      npub1...      Output unsigned events (for external signing)
                      bunker://...  Remote signing via NIP-46
                      browser       Sign with browser extension (NIP-07)

  GITHUB_TOKEN      Optional. Avoid GitHub API rate limits
  FDROID_DATA_PATH  Optional. Local fdroiddata clone for metadata
  RELAY_URLS        Custom relay URLs (default: wss://relay.zapstore.dev)
  BLOSSOM_URL       Custom CDN server (default: https://cdn.zapstore.dev)

CONFIG FILE (zapstore.yaml)
  repository: https://github.com/user/app    # Source code repo (URL or NIP-34 naddr)
  release_source: <url>                      # APK source (if different from repo)
  local: ./build/app.apk                     # Local APK path (highest priority)
  match: ".*arm64.*\\.apk$"                  # Asset filter regex
  name: My App                               # Override APK label
  summary: Short tagline                     # One-line summary
  description: ...                           # App description
  tags: [tools, productivity]                # Category tags
  license: MIT                               # SPDX license identifier
  website: https://myapp.com                 # App homepage
  icon: ./icon.png                           # Custom icon (local path or URL)
  images: [./screenshot1.png, ...]           # Screenshots (local paths or URLs)
  release_notes: ./CHANGELOG.md              # Release notes (file path or URL)
  release_channel: main                      # Channel: main, beta, nightly, dev
  commit: abc123                             # Git commit hash (for reproducible builds)
  supported_nips: ["01", "07"]               # Supported Nostr NIPs
  min_allowed_version: "1.0.0"               # Minimum allowed version
  variants:                                  # APK variant patterns
    fdroid: ".*-fdroid-.*\\.apk$"
    google: ".*-google-.*\\.apk$"

EXAMPLES
  SIGN_WITH=nsec1... zsp                          # Wizard mode
  SIGN_WITH=nsec1... zsp zapstore.yaml            # Publish from config
  SIGN_WITH=nsec1... zsp -r github.com/user/app   # Quick GitHub publish
  SIGN_WITH=nsec1... zsp -r github.com/user/app -m github  # With GitHub metadata
  SIGN_WITH=nsec1... zsp -r f-droid.org/packages/com.app  # F-Droid publish
  SIGN_WITH=nsec1... zsp app.apk                  # Publish local APK
  SIGN_WITH=nsec1... zsp --preview zapstore.yaml  # Preview release in browser
  SIGN_WITH=npub1... zsp --dry-run zapstore.yaml  # Preview unsigned events

More info: https://github.com/zapstore/zsp
`)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	reorderArgs()
	flag.Parse()

	// Handle help flag
	if *helpFlag {
		usage()
		return nil
	}

	// Handle version flag
	if *versionFlag {
		fmt.Print(ui.Title(config.Logo))
		fmt.Printf("zsp version %s\n", version)
		return nil
	}

	// Quiet implies yes
	if *quietFlag {
		*yesFlag = true
	}

	// Handle no-color flag
	if *noColorFlag {
		ui.SetNoColor(true)
	}

	// Handle extract flag (must be a local APK)
	if *extractFlag {
		args := flag.Args()
		if len(args) == 0 || !strings.HasSuffix(strings.ToLower(args[0]), ".apk") {
			return fmt.Errorf("--extract requires a local APK file as argument")
		}
		return extractAPKMetadata(args[0])
	}

	// Handle check-apk flag (verify config fetches and parses arm64-v8a APK)
	if *checkAPKFlag {
		return checkAPK()
	}

	// Create context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
		// Exit immediately - stdin blocking reads won't respond to context cancellation
		fmt.Fprintln(os.Stderr, "\nAborted")
		os.Exit(130) // 130 = 128 + SIGINT (2)
	}()

	// Determine config source
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Run the publish flow
	return publish(ctx, cfg)
}

// loadConfig loads configuration from various sources.
func loadConfig() (*config.Config, error) {
	args := flag.Args()

	// --wizard flag: run wizard with optional existing config as defaults
	if *wizardFlag {
		if *quietFlag {
			return nil, fmt.Errorf("--wizard cannot be used with --quiet")
		}
		// Try to load existing config for defaults
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
		return loadAPKConfig(args[0])
	}

	// Quick mode with -r flag only (no APK)
	if *repoFlag != "" {
		repoURL := normalizeRepoURL(*repoFlag)
		if err := config.ValidateURL(repoURL); err != nil {
			return nil, fmt.Errorf("invalid -r URL: %w", err)
		}
		cfg := &config.Config{
			Repository: repoURL,
		}
		// If -s is specified, use it as release source; otherwise defaults to repository
		if *releaseSourceFlag != "" {
			sourceURL := normalizeRepoURL(*releaseSourceFlag)
			if err := config.ValidateURL(sourceURL); err != nil {
				return nil, fmt.Errorf("invalid -s URL: %w", err)
			}
			cfg.ReleaseSource = &config.ReleaseSource{URL: sourceURL}
		}
		return cfg, nil
	}

	// Config file as positional argument
	if len(args) > 0 {
		return config.Load(args[0])
	}

	// Check for stdin
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		// Data is being piped
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("failed to read stdin: %w", err)
		}
		return config.Parse(strings.NewReader(string(data)))
	}

	// Look for default config file
	if _, err := os.Stat("zapstore.yaml"); err == nil {
		return config.Load("zapstore.yaml")
	}

	// Launch interactive wizard
	if *quietFlag {
		return nil, fmt.Errorf("no configuration provided. Use 'zsp <config.yaml>' or 'zsp -r <repo-url>'")
	}

	return config.RunWizard(nil)
}

// loadAPKConfig creates config from a local APK path with optional -r and -s flags.
func loadAPKConfig(apkPath string) (*config.Config, error) {
	cfg := &config.Config{
		Local: apkPath,
	}

	// If -r flag provided, validate and use it as repository
	if *repoFlag != "" {
		repoURL := normalizeRepoURL(*repoFlag)
		if err := config.ValidateURL(repoURL); err != nil {
			return nil, fmt.Errorf("invalid -r URL: %w", err)
		}
		cfg.Repository = repoURL
	}

	// If -s flag provided, validate and use it as release source
	if *releaseSourceFlag != "" {
		sourceURL := normalizeRepoURL(*releaseSourceFlag)
		if err := config.ValidateURL(sourceURL); err != nil {
			return nil, fmt.Errorf("invalid -s URL: %w", err)
		}
		cfg.ReleaseSource = &config.ReleaseSource{URL: sourceURL}
	}

	return cfg, nil
}

// extractAPKMetadata parses an APK and outputs its metadata as JSON.
// If the APK contains an icon, it writes it to disk alongside the APK.
func extractAPKMetadata(apkPath string) error {
	apkInfo, err := apk.Parse(apkPath)
	if err != nil {
		return fmt.Errorf("failed to parse APK: %w", err)
	}

	// Build a clean JSON output structure
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

	// Write icon to disk if present
	if apkInfo.Icon != nil {
		// Generate icon path: same directory as APK, named <apk_basename>_icon.png
		apkBase := strings.TrimSuffix(apkPath, filepath.Ext(apkPath))
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
// Returns nil (exit 0) on success, error (exit 1) on failure.
func checkAPK() error {
	// Create context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
		fmt.Fprintln(os.Stderr, "\nAborted")
		os.Exit(130)
	}()

	// Helper to exit with error on stderr
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Load configuration
	cfg, err := loadConfig()
	if err != nil {
		fail(err)
	}

	if err := cfg.Validate(); err != nil {
		fail(err)
	}

	// Create source
	src, err := source.NewWithOptions(cfg, source.Options{
		BaseDir:   cfg.BaseDir,
		SkipCache: true, // Always fetch fresh for checking
	})
	if err != nil {
		fail(err)
	}

	// Fetch latest release
	release, err := src.FetchLatestRelease(ctx)
	if err != nil {
		fail(err)
	}

	// Filter to APKs only
	apkAssets := picker.FilterAPKs(release.Assets)
	if len(apkAssets) == 0 {
		fail(fmt.Errorf("no APK files found in release"))
	}

	// Apply match filter if specified
	if cfg.Match != "" {
		apkAssets, err = picker.FilterByMatch(apkAssets, cfg.Match)
		if err != nil {
			fail(err)
		}
		if len(apkAssets) == 0 {
			fail(fmt.Errorf("no APK files match pattern: %s", cfg.Match))
		}
	}

	// Select best APK
	var selectedAsset *source.Asset
	if len(apkAssets) == 1 {
		selectedAsset = apkAssets[0]
	} else {
		ranked := picker.DefaultModel.RankAssets(apkAssets)
		selectedAsset = ranked[0].Asset
	}

	// Download APK if needed
	var apkPath string
	if selectedAsset.LocalPath != "" {
		apkPath = selectedAsset.LocalPath
	} else {
		apkPath, err = src.Download(ctx, selectedAsset, "", nil)
		if err != nil {
			fail(err)
		}
		// APK is cached by Download() for reuse - don't delete
	}

	// Parse APK
	apkInfo, err := apk.Parse(apkPath)
	if err != nil {
		fail(err)
	}

	// Verify arm64 support
	if !apkInfo.IsArm64() {
		fail(fmt.Errorf("APK does not support arm64-v8a architecture (found: %v)", apkInfo.Architectures))
	}

	// Success - print app ID only
	fmt.Println(apkInfo.PackageID)
	return nil
}

// normalizeRepoURL ensures the repository URL has a scheme.
func normalizeRepoURL(url string) string {
	if !strings.Contains(url, "://") {
		return "https://" + url
	}
	return url
}

// publish runs the main publish flow.
func publish(ctx context.Context, cfg *config.Config) error {
	// Determine total steps based on mode
	// Steps: 1=Fetch Assets, 2=Metadata, 3=Upload, 4=Publish
	totalSteps := 4
	if *dryRunFlag {
		totalSteps = 2 // Fetch Assets, Metadata (no upload/publish)
	}

	var steps *ui.StepTracker
	if !*quietFlag {
		steps = ui.NewStepTracker(totalSteps)
	}

	// Create source with base directory for relative paths
	src, err := source.NewWithOptions(cfg, source.Options{
		BaseDir:   cfg.BaseDir,
		SkipCache: *overwriteReleaseFlag,
	})
	if err != nil {
		return fmt.Errorf("failed to create source: %w", err)
	}

	if *verboseFlag {
		fmt.Printf("Source type: %s\n", src.Type())
	}

	// ═══════════════════════════════════════════════════════════════════
	// STEP 1: FETCH ASSETS
	// ═══════════════════════════════════════════════════════════════════
	if steps != nil {
		steps.StartStep("Fetch Assets")
	}

	var spinner *ui.Spinner
	if !*quietFlag {
		spinner = ui.NewSpinner("Fetching release info...")
		spinner.Start()
	}
	release, err := src.FetchLatestRelease(ctx)
	if errors.Is(err, source.ErrNotModified) {
		if spinner != nil {
			spinner.StopWithSuccess("Release unchanged, nothing to do")
		}
		if !*quietFlag {
			fmt.Println("  Release has not changed since last publish. Use --overwrite-release to publish anyway.")
		}
		return nil
	}
	if err != nil {
		if spinner != nil {
			spinner.StopWithError("Failed to fetch release")
		}
		return fmt.Errorf("failed to fetch release: %w", err)
	}
	if spinner != nil {
		if release.Version != "" {
			spinner.StopWithSuccess(fmt.Sprintf("Found release %s with %d assets", release.Version, len(release.Assets)))
		} else {
			spinner.StopWithSuccess(fmt.Sprintf("Found %d assets (version from APK)", len(release.Assets)))
		}
	}

	if *verboseFlag {
		if release.Version != "" {
			fmt.Printf("  Found release: %s with %d assets\n", release.Version, len(release.Assets))
		} else {
			fmt.Printf("  Found %d assets (version will be extracted from APK)\n", len(release.Assets))
		}
	}

	// Filter to APKs only
	apkAssets := picker.FilterAPKs(release.Assets)
	if len(apkAssets) == 0 {
		return fmt.Errorf("no APK files found in release")
	}

	// Apply match filter if specified
	if cfg.Match != "" {
		apkAssets, err = picker.FilterByMatch(apkAssets, cfg.Match)
		if err != nil {
			return err
		}
		if len(apkAssets) == 0 {
			return fmt.Errorf("no APK files match pattern: %s", cfg.Match)
		}
	}

	// Select best APK
	var selectedAsset *source.Asset
	if len(apkAssets) == 1 {
		selectedAsset = apkAssets[0]
		if !*quietFlag {
			ui.PrintSuccess(fmt.Sprintf("Selected %s", selectedAsset.Name))
		}
	} else {
		// Rank and select
		ranked := picker.DefaultModel.RankAssets(apkAssets)

		if *verboseFlag {
			fmt.Println("  Ranked APKs:")
			for i, sa := range ranked {
				fmt.Printf("    %d. %s (score: %.2f)\n", i+1, sa.Asset.Name, sa.Score)
			}
		}

		// Interactive selection if not quiet mode and multiple valid options
		if !*quietFlag && !*yesFlag && len(ranked) > 1 {
			selectedAsset, err = selectAPKInteractive(ranked)
			if err != nil {
				return fmt.Errorf("APK selection failed: %w", err)
			}
		} else {
			selectedAsset = ranked[0].Asset
			if !*quietFlag {
				ui.PrintSuccess(fmt.Sprintf("Selected %s (best match)", selectedAsset.Name))
			}
		}
	}

	// Download APK if needed
	var apkPath string
	if selectedAsset.LocalPath != "" {
		apkPath = selectedAsset.LocalPath
		if !*quietFlag {
			ui.PrintSuccess("Using local APK file")
		}
	} else {
		// Check if APK is already cached
		cachedPath := source.GetCachedDownload(selectedAsset.URL, selectedAsset.Name)
		if cachedPath != "" {
			apkPath = cachedPath
			selectedAsset.LocalPath = cachedPath
			if !*quietFlag {
				ui.PrintSuccess("Using cached APK")
			}
		} else {
			// Download to temp directory with progress indicator
			var tracker *ui.DownloadTracker
			var progressCallback source.DownloadProgress
			if !*quietFlag {
				tracker = ui.NewDownloadTracker(fmt.Sprintf("Downloading %s", selectedAsset.Name), selectedAsset.Size)
				progressCallback = tracker.Callback()
			}

			var err error
			apkPath, err = src.Download(ctx, selectedAsset, "", progressCallback)
			if tracker != nil {
				tracker.Done()
			}
			if err != nil {
				return fmt.Errorf("failed to download APK: %w", err)
			}
			// APK is cached by Download() for reuse - don't delete
		}
	}

	// Parse APK
	var parseSpinner *ui.Spinner
	if !*quietFlag {
		parseSpinner = ui.NewSpinner("Parsing APK...")
		parseSpinner.Start()
	}
	apkInfo, err := apk.Parse(apkPath)
	if err != nil {
		if parseSpinner != nil {
			parseSpinner.StopWithError("Failed to parse APK")
		}
		return fmt.Errorf("failed to parse APK: %w", err)
	}

	// Verify arm64 support
	if !apkInfo.IsArm64() {
		if parseSpinner != nil {
			parseSpinner.StopWithError("APK architecture not supported")
		}
		return fmt.Errorf("APK does not support arm64-v8a architecture (found: %v)", apkInfo.Architectures)
	}
	if parseSpinner != nil {
		parseSpinner.StopWithSuccess("Parsed and verified APK")
	}

	// Backfill version from APK if not known from release (web sources)
	if release.Version == "" {
		release.Version = apkInfo.VersionName
	}

	// Display APK summary
	if !*quietFlag {
		ui.PrintSectionHeader("APK Summary")
		ui.PrintKeyValue("Name", apkInfo.Label)
		ui.PrintKeyValue("App ID", apkInfo.PackageID)
		ui.PrintKeyValue("Version", fmt.Sprintf("%s (%d)", apkInfo.VersionName, apkInfo.VersionCode))
		ui.PrintKeyValue("Certificate hash", apkInfo.CertFingerprint)
		ui.PrintKeyValue("Size", fmt.Sprintf("%.2f MB", float64(apkInfo.FileSize)/(1024*1024)))
	}

	// Check if asset already exists on relays (unless --overwrite-release is set)
	relaysEnv := os.Getenv("RELAY_URLS")
	publisher := nostr.NewPublisherFromEnv(relaysEnv)
	if !*overwriteReleaseFlag && !*dryRunFlag {
		existingAsset, err := publisher.CheckExistingAsset(ctx, apkInfo.PackageID, apkInfo.VersionName)
		if err != nil {
			// Log warning but continue - relay might be unavailable
			if *verboseFlag {
				fmt.Printf("  Could not check relays: %v\n", err)
			}
		} else if existingAsset != nil {
			if !*quietFlag {
				ui.PrintWarning(fmt.Sprintf("Asset %s@%s already exists on %s",
					apkInfo.PackageID, apkInfo.VersionName, existingAsset.RelayURL))
				fmt.Println("  Use --overwrite-release to publish anyway.")
			}
			return nil
		}
	}

	// ═══════════════════════════════════════════════════════════════════
	// STEP 2: GATHER METADATA
	// ═══════════════════════════════════════════════════════════════════
	if steps != nil {
		steps.StartStep("Gather Metadata")
	}

	// Check if app already exists on relays (skip metadata fetch if so, unless --overwrite-app)
	skipMetadataFetch := false
	if !*overwriteAppFlag && !*dryRunFlag {
		existingApp, err := publisher.CheckExistingApp(ctx, apkInfo.PackageID)
		if err != nil {
			// Log warning but continue - relay might be unavailable
			if *verboseFlag {
				fmt.Printf("  Could not check for existing app: %v\n", err)
			}
		} else if existingApp != nil {
			skipMetadataFetch = true
			if !*quietFlag {
				ui.PrintInfo(fmt.Sprintf("App %s already exists on %s, skipping metadata fetch",
					apkInfo.PackageID, existingApp.RelayURL))
				fmt.Println("  Use --overwrite-app to re-fetch metadata from sources.")
			}
		}
	}

	// Fetch metadata from external sources (unless app already exists)
	// Use -m flags if provided, otherwise auto-detect based on source type
	if !skipMetadataFetch {
		metadataSources := fetchMetadataFlag
		if len(metadataSources) == 0 {
			metadataSources = source.DefaultMetadataSources(cfg)
		}
		if len(metadataSources) > 0 {
			var metaSpinner *ui.Spinner
			if !*quietFlag {
				metaSpinner = ui.NewSpinner("Fetching metadata from external sources...")
				metaSpinner.Start()
			}
			fetcher := source.NewMetadataFetcherWithPackageID(cfg, apkInfo.PackageID)
			fetcher.APKName = apkInfo.Label // APK name has priority over metadata sources
			if err := fetcher.FetchMetadata(ctx, metadataSources); err != nil {
				// Log warning but continue - metadata is optional
				if metaSpinner != nil {
					metaSpinner.StopWithWarning("Metadata fetch failed (continuing)")
				}
				if *verboseFlag {
					fmt.Printf("    %v\n", err)
				}
			} else {
				if metaSpinner != nil {
					metaSpinner.StopWithSuccess(fmt.Sprintf("Fetched metadata from %s", strings.Join(metadataSources, ", ")))
				}
				if *verboseFlag {
					fmt.Printf("    name=%q, description=%d chars, tags=%v\n",
						cfg.Name, len(cfg.Description), cfg.Tags)
				}
			}
		} else {
			if !*quietFlag {
				ui.PrintInfo("No external metadata sources configured")
			}
		}

		// Fetch screenshots from Play Store if none configured yet
		if len(cfg.Images) == 0 && apkInfo.PackageID != "" {
			var screenshotSpinner *ui.Spinner
			if !*quietFlag {
				screenshotSpinner = ui.NewSpinner("Fetching screenshots from Play Store...")
				screenshotSpinner.Start()
			}
			psMeta, err := source.FetchPlayStoreMetadata(ctx, apkInfo.PackageID)
			if err != nil {
				if screenshotSpinner != nil {
					screenshotSpinner.StopWithWarning("Screenshots not available")
				}
				if *verboseFlag {
					fmt.Printf("    %v\n", err)
				}
			} else if len(psMeta.ImageURLs) > 0 {
				cfg.Images = psMeta.ImageURLs
				if screenshotSpinner != nil {
					screenshotSpinner.StopWithSuccess(fmt.Sprintf("Fetched %d screenshots from Play Store", len(psMeta.ImageURLs)))
				}
			} else {
				if screenshotSpinner != nil {
					screenshotSpinner.StopWithWarning("No screenshots found on Play Store")
				}
			}
		}
	}

	// Get Blossom server URL (needed for preview)
	blossomURL := os.Getenv("BLOSSOM_URL")
	if blossomURL == "" {
		blossomURL = blossom.DefaultServer
	}

	// Get relay URLs (needed for preview) - reuse publisher from earlier
	relayURLs := publisher.RelayURLs()

	// Determine release notes: use config if specified, otherwise use remote release notes
	// ReleaseNotes can be a local file path or URL
	releaseNotes := release.Changelog
	if cfg.ReleaseNotes != "" {
		var err error
		releaseNotes, err = source.FetchReleaseNotes(ctx, cfg.ReleaseNotes, apkInfo.VersionName, cfg.BaseDir)
		if err != nil {
			return fmt.Errorf("failed to fetch release notes: %w", err)
		}
	}

	// Determine commit hash for reproducible builds (only from config)
	commit := cfg.Commit

	// Determine variant from config variants map
	variant := matchVariant(cfg.Variants, filepath.Base(apkInfo.FilePath))

	// Pre-download remote icon and screenshots before preview
	// This ensures preview shows the actual images and avoids re-downloading during upload
	var preDownloaded *preDownloadedImages
	if cfg.Icon != "" && isRemoteURL(cfg.Icon) || hasRemoteImages(cfg.Images) {
		var err error
		preDownloaded, err = preDownloadImages(ctx, cfg, *quietFlag)
		if err != nil {
			return fmt.Errorf("failed to download images: %w", err)
		}
	}

	// Track port used for browser operations (preview and/or signing)
	browserPort := *portFlag
	previewWasShown := false

	// Show HTML preview BEFORE signing (if --preview flag or interactive prompt)
	if !*quietFlag && !*yesFlag {
		showPreview := *previewFlag

		if !showPreview {
			defaultPort := nostr.DefaultPreviewPort
			if browserPort != 0 {
				defaultPort = browserPort
			}

			confirmed, port, err := ui.ConfirmWithPort("Preview release in browser?", defaultPort)
			if err != nil {
				return fmt.Errorf("prompt failed: %w", err)
			}
			showPreview = confirmed
			if confirmed {
				browserPort = port
			}
		}

		if showPreview {
			if browserPort == 0 {
				browserPort = nostr.DefaultPreviewPort
			}

			previewData := nostr.BuildPreviewDataFromAPK(apkInfo, cfg, releaseNotes, blossomURL, relayURLs)
			// Override icon with pre-downloaded icon if available (e.g., from Play Store)
			if preDownloaded != nil && preDownloaded.Icon != nil {
				previewData.IconData = preDownloaded.Icon.Data
			}
			// Add pre-downloaded screenshots for local serving in preview
			if preDownloaded != nil && len(preDownloaded.Images) > 0 {
				for _, img := range preDownloaded.Images {
					previewData.ImageData = append(previewData.ImageData, nostr.PreviewImageData{
						Data:     img.Data,
						MimeType: img.MimeType,
					})
				}
			}
			previewServer := nostr.NewPreviewServer(previewData, releaseNotes, "", browserPort)
			url, err := previewServer.Start()
			if err != nil {
				return fmt.Errorf("failed to start preview server: %w", err)
			}

			fmt.Printf("Preview server started at %s\n", url)
			fmt.Println("Press Enter to continue, or Ctrl+C to cancel...")

			// Wait for Enter from terminal
			reader := bufio.NewReader(os.Stdin)
			_, err = reader.ReadString('\n')
			if err != nil {
				previewServer.Close()
				return fmt.Errorf("failed to read input: %w", err)
			}

			// Signal browser to close and confirm
			previewServer.ConfirmFromCLI()
			confirmed := true
			previewServer.Close()

			if !confirmed {
				fmt.Println("Aborted. No events were published.")
				// Clear cache so next run can retry
				clearSourceCache(src)
				return nil
			}
			previewWasShown = true
		}
	}

	// ═══════════════════════════════════════════════════════════════════
	// STEP 3: SIGN & UPLOAD
	// ═══════════════════════════════════════════════════════════════════
	if steps != nil && !*dryRunFlag {
		steps.StartStep("Sign & Upload")
	}

	// In dry run mode, always use test key - skip SIGN_WITH entirely
	var signWith string
	if *dryRunFlag {
		signWith = nostr.TestNsec
	} else {
		// Check for SIGN_WITH (from environment or .env file)
		signWith = config.GetSignWith()
		if signWith == "" {
			if !*quietFlag {
				// Interactive prompt for signing method
				ui.PrintSectionHeader("Signing Setup")
				var err error
				signWith, err = config.PromptSignWith()
				if err != nil {
					return fmt.Errorf("signing setup failed: %w", err)
				}
			} else {
				return fmt.Errorf("SIGN_WITH environment variable is required")
			}
		}
	}

	// Dry run is active if flag is set OR if test nsec was selected from menu
	isDryRun := *dryRunFlag || signWith == nostr.TestNsec

	// For browser signer: if preview was shown, reuse that port
	// Otherwise, prompt for port if not provided via flag
	signerPort := browserPort
	if signWith == "browser" && !previewWasShown && signerPort == 0 && !*quietFlag && !*yesFlag {
		defaultPort := nostr.DefaultNIP07Port
		port, err := ui.ConfirmWithPortYesOnly("Browser signing port?", defaultPort)
		if err != nil {
			return fmt.Errorf("prompt failed: %w", err)
		}
		signerPort = port
	}

	// Create signer (pass port for browser signer)
	signer, err := nostr.NewSignerWithOptions(ctx, signWith, nostr.SignerOptions{
		Port: signerPort,
	})
	if err != nil {
		return fmt.Errorf("failed to create signer: %w", err)
	}
	defer signer.Close()

	if *verboseFlag {
		fmt.Printf("Signer type: %v, pubkey: %s...\n", signer.Type(), signer.PublicKey()[:16])
	}

	// Get relay hint (first relay from RELAY_URLS env or default)
	relayHint := nostr.DefaultRelay
	if relaysEnv != "" {
		parts := strings.Split(relaysEnv, ",")
		if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
			relayHint = strings.TrimSpace(parts[0])
		}
	}

	// Process icon and images from config (can be local paths or remote URLs)
	var iconURL string
	var imageURLs []string
	var events *nostr.EventSet

	// Check if we should use batch signing (NIP-07 browser signer)
	batchSigner, isBatchSigner := signer.(nostr.BatchSigner)

	// Upload to Blossom (unless dry run or npub signer)
	if !isDryRun && signer.Type() != nostr.SignerNpub {
		blossomClient := blossom.NewClient(blossomURL)

		if isBatchSigner {
			// Batch signing mode: pre-collect all data, create ALL events (auth + main), sign once
			events, err = uploadAndSignWithBatch(ctx, cfg, apkInfo, apkPath, release, blossomClient, blossomURL, batchSigner, signer.PublicKey(), relayHint, preDownloaded, variant, commit)
			if err != nil {
				return err
			}
		} else {
			// Regular signing mode: sign each upload auth event individually
			iconURL, imageURLs, err = uploadWithIndividualSigning(ctx, cfg, apkInfo, apkPath, blossomClient, signer, preDownloaded)
			if err != nil {
				return err
			}
			// Build and sign main events
			events = nostr.BuildEventSet(nostr.BuildEventSetParams{
				APKInfo:    apkInfo,
				Config:     cfg,
				Pubkey:     signer.PublicKey(),
				BlossomURL: blossomURL,
				IconURL:    iconURL,
				ImageURLs:  imageURLs,
				Changelog:  releaseNotes,
				Variant:    variant,
				Commit:     commit,
			})
			if err := nostr.SignEventSet(ctx, signer, events, relayHint); err != nil {
				return fmt.Errorf("failed to sign events: %w", err)
			}
		}
	} else {
		// Dry run or npub - just resolve URLs without uploading
		if cfg.Icon != "" && isRemoteURL(cfg.Icon) {
			iconURL = cfg.Icon
		}
		for _, img := range cfg.Images {
			if isRemoteURL(img) {
				imageURLs = append(imageURLs, img)
			}
		}
		// Build events
		events = nostr.BuildEventSet(nostr.BuildEventSetParams{
			APKInfo:    apkInfo,
			Config:     cfg,
			Pubkey:     signer.PublicKey(),
			BlossomURL: blossomURL,
			IconURL:    iconURL,
			ImageURLs:  imageURLs,
			Changelog:  releaseNotes,
			Variant:    variant,
			Commit:     commit,
		})
		// Sign events (will use batch signing if available)
		if err := nostr.SignEventSet(ctx, signer, events, relayHint); err != nil {
			return fmt.Errorf("failed to sign events: %w", err)
		}
	}

	// Dry run - output events and exit
	if isDryRun {
		outputEvents(events)
		return nil
	}

	// For npub signer, output unsigned events
	if signer.Type() == nostr.SignerNpub {
		if !*quietFlag {
			fmt.Println()
			ui.PrintInfo("npub mode - outputting unsigned events for external signing")
		}
		outputEvents(events)
		if !*quietFlag {
			ui.PrintCompletionSummary(true, "Unsigned events generated - sign externally before publishing")
		}
		return nil
	}

	// Hash confirmation - user must confirm they're attesting to this hash
	if !*yesFlag {
		isClosedSource := cfg.Repository == ""
		confirmed, err := confirmHash(apkInfo.SHA256, isClosedSource)
		if err != nil {
			return fmt.Errorf("hash confirmation failed: %w", err)
		}
		if !confirmed {
			fmt.Println("  Aborted. No events were published.")
			clearSourceCache(src)
			return nil
		}
	}

	// ═══════════════════════════════════════════════════════════════════
	// STEP 4: PUBLISH TO RELAYS
	// ═══════════════════════════════════════════════════════════════════
	if steps != nil {
		steps.StartStep("Publish")
	}

	// Publish to relays (publisher was created above for relay check)

	// Confirm before publishing (unless -y flag - preview confirmation already done earlier)
	if !*yesFlag {
		confirmed, err := confirmPublish(events, publisher.RelayURLs())
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			fmt.Println("  Aborted. No events were published.")
			// Clear cache so next run can retry
			clearSourceCache(src)
			return nil
		}
	}

	var publishSpinner *ui.Spinner
	if !*quietFlag {
		publishSpinner = ui.NewSpinner(fmt.Sprintf("Publishing to %d relays...", len(publisher.RelayURLs())))
		publishSpinner.Start()
	}
	results, err := publisher.PublishEventSet(ctx, events)
	if err != nil {
		if publishSpinner != nil {
			publishSpinner.StopWithError("Failed to publish")
		}
		return fmt.Errorf("failed to publish: %w", err)
	}

	// Report results
	allSuccess := true
	var failures []string
	for eventType, eventResults := range results {
		for _, r := range eventResults {
			if r.Success {
				if *verboseFlag {
					failures = append(failures, fmt.Sprintf("    %s -> %s: OK", eventType, r.RelayURL))
				}
			} else {
				failures = append(failures, fmt.Sprintf("    %s -> %s: FAILED (%v)", eventType, r.RelayURL, r.Error))
				allSuccess = false
			}
		}
	}

	if publishSpinner != nil {
		if allSuccess {
			publishSpinner.StopWithSuccess("Published successfully")
		} else {
			publishSpinner.StopWithWarning("Published with some failures")
		}
	}
	// Print failures after spinner stops
	for _, f := range failures {
		fmt.Println(f)
	}

	// Commit or clear cache based on publish success
	if allSuccess {
		// Commit cache to disk so we skip this release next time
		commitSourceCache(src)
	} else {
		// Clear cache so we can retry
		clearSourceCache(src)
		if *verboseFlag {
			fmt.Println("  Cleared release cache for retry")
		}
	}

	// Print completion summary
	if !*quietFlag {
		if allSuccess {
			ui.PrintCompletionSummary(true, fmt.Sprintf("Published %s v%s to %s",
				apkInfo.PackageID, apkInfo.VersionName, strings.Join(publisher.RelayURLs(), ", ")))
		} else {
			ui.PrintCompletionSummary(false, "Published with some failures")
		}
	}

	return nil
}

// outputEvents prints events as JSON Lines (one JSON object per line).
func outputEvents(events *nostr.EventSet) {
	enc := json.NewEncoder(os.Stdout)
	enc.Encode(events.AppMetadata)
	enc.Encode(events.Release)
	for _, asset := range events.SoftwareAssets {
		enc.Encode(asset)
	}
}

// previewEvents displays signed events in a human-readable format.
func previewEvents(events *nostr.EventSet) {
	ui.PrintSectionHeader("Signed Events Preview")

	// Software Application (kind 32267)
	fmt.Println()
	fmt.Printf("  %s\n", ui.Bold("Kind 32267 (Software Application)"))
	fmt.Printf("    ID: %s\n", events.AppMetadata.ID)
	fmt.Printf("    pubkey: %s...\n", events.AppMetadata.PubKey[:16])
	fmt.Printf("    Created: %s\n", events.AppMetadata.CreatedAt.Time().Format("2006-01-02 15:04:05"))
	fmt.Println("    Tags:")
	for _, tag := range events.AppMetadata.Tags {
		fmt.Printf("      %v\n", tag)
	}
	if events.AppMetadata.Content != "" {
		fmt.Printf("    Content: %s\n", truncateString(events.AppMetadata.Content, 100))
	}
	fmt.Printf("    Sig: %s...\n", events.AppMetadata.Sig[:32])

	// Software Release (kind 30063)
	fmt.Println()
	fmt.Printf("  %s\n", ui.Bold("Kind 30063 (Software Release)"))
	fmt.Printf("    ID: %s\n", events.Release.ID)
	fmt.Printf("    pubkey: %s...\n", events.Release.PubKey[:16])
	fmt.Printf("    Created: %s\n", events.Release.CreatedAt.Time().Format("2006-01-02 15:04:05"))
	fmt.Println("    Tags:")
	for _, tag := range events.Release.Tags {
		fmt.Printf("      %v\n", tag)
	}
	if events.Release.Content != "" {
		fmt.Printf("    Content: %s\n", truncateString(events.Release.Content, 100))
	}
	fmt.Printf("    Sig: %s...\n", events.Release.Sig[:32])

	// Software Assets (kind 3063)
	for i, asset := range events.SoftwareAssets {
		fmt.Println()
		assetLabel := "Kind 3063 (Software Asset)"
		if len(events.SoftwareAssets) > 1 {
			assetLabel = fmt.Sprintf("Kind 3063 (Software Asset %d)", i+1)
		}
		fmt.Printf("  %s\n", ui.Bold(assetLabel))
		fmt.Printf("    ID: %s\n", asset.ID)
		fmt.Printf("    pubkey: %s...\n", asset.PubKey[:16])
		fmt.Printf("    Created: %s\n", asset.CreatedAt.Time().Format("2006-01-02 15:04:05"))
		fmt.Println("    Tags:")
		for _, tag := range asset.Tags {
			fmt.Printf("      %v\n", tag)
		}
		fmt.Printf("    Sig: %s...\n", asset.Sig[:32])
	}
	fmt.Println()
}

// previewEventsJSON outputs events as formatted JSON with syntax highlighting.
func previewEventsJSON(events *nostr.EventSet) {
	ui.PrintSectionHeader("Signed Events (JSON)")
	fmt.Println()

	fmt.Printf("  %s\n", ui.Bold("Kind 32267 (Software Application):"))
	printColorizedJSON(events.AppMetadata)
	fmt.Println()

	fmt.Printf("  %s\n", ui.Bold("Kind 30063 (Software Release):"))
	printColorizedJSON(events.Release)
	fmt.Println()

	for i, asset := range events.SoftwareAssets {
		assetLabel := "Kind 3063 (Software Asset):"
		if len(events.SoftwareAssets) > 1 {
			assetLabel = fmt.Sprintf("Kind 3063 (Software Asset %d):", i+1)
		}
		fmt.Printf("  %s\n", ui.Bold(assetLabel))
		printColorizedJSON(asset)
		fmt.Println()
	}
}

// printColorizedJSON prints a value as colorized JSON.
func printColorizedJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Println(ui.ColorizeJSON(string(data)))
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// confirmHash asks the user to confirm the file hash they just signed.
// This is a security step since the hash may have been fetched from an external source.
// If isClosedSource is true, also warns that the app has no repository.
func confirmHash(sha256Hash string, isClosedSource bool) (bool, error) {
	fmt.Println()
	ui.PrintWarning("You just signed an event attesting to this file hash (kind 3063):")
	fmt.Println()
	fmt.Printf("  %s\n", ui.Bold(sha256Hash))
	fmt.Println()
	fmt.Printf("  %s\n", ui.Bold("Make sure it matches the APK you intend to distribute."))
	fmt.Println()
	fmt.Println("  To verify, run:")
	fmt.Printf("    %s\n", ui.Dim("shasum -a 256 <path-to-apk>   # macOS"))
	fmt.Printf("    %s\n", ui.Dim("sha256sum <path-to-apk>       # Linux"))
	fmt.Println()

	if isClosedSource {
		ui.PrintWarning("This application has no repository (closed source).")
		fmt.Println()
	}

	return ui.Confirm("Confirm hash is correct?", false)
}

// confirmPublish shows a pre-publish summary and asks for confirmation.
// Returns true if user confirms, false if they want to exit.
func confirmPublish(events *nostr.EventSet, relayURLs []string) (bool, error) {
	// Get package ID and version from events
	packageID := ""
	version := ""
	for _, tag := range events.Release.Tags {
		if len(tag) >= 2 {
			if tag[0] == "i" {
				packageID = tag[1]
			}
			if tag[0] == "version" {
				version = tag[1]
			}
		}
	}

	ui.PrintSectionHeader("Ready to Publish")
	fmt.Printf("  App: %s v%s\n", packageID, version)
	fmt.Printf("  Events: Kind 32267 (App) + Kind 30063 (Release) + Kind 3063 (Asset)\n")
	fmt.Printf("  Target: %s\n", strings.Join(relayURLs, ", "))
	fmt.Println()

	for {
		options := []string{
			"Preview events (formatted)",
			"Preview events (JSON)",
			"Publish now",
			"Exit without publishing",
		}

		idx, err := ui.SelectOption("Choose an option:", options, 2)
		if err != nil {
			return false, err
		}

		switch idx {
		case 0:
			previewEvents(events)
		case 1:
			previewEventsJSON(events)
		case 2:
			return true, nil
		case 3:
			return false, nil
		}
	}
}

// isRemoteURL returns true if the path is a remote URL (http/https).
func isRemoteURL(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
}

// hasRemoteImages returns true if any of the images are remote URLs.
func hasRemoteImages(images []string) bool {
	for _, img := range images {
		if isRemoteURL(img) {
			return true
		}
	}
	return false
}

// findPreDownloadedImage finds a pre-downloaded image by its original URL.
func findPreDownloadedImage(images []*downloadedImage, url string) *downloadedImage {
	for _, img := range images {
		if img.URL == url {
			return img
		}
	}
	return nil
}

// downloadedImage holds pre-downloaded image data.
type downloadedImage struct {
	URL      string // Original URL
	Data     []byte // Image bytes
	Hash     string // SHA256 hash (hex)
	MimeType string // MIME type
}

// preDownloadedImages holds all pre-downloaded images for a release.
type preDownloadedImages struct {
	Icon   *downloadedImage   // Icon (from cfg.Icon if remote URL)
	Images []*downloadedImage // Screenshots (from cfg.Images if remote URLs)
}

// preDownloadImages downloads cfg.Icon and cfg.Images if they are remote URLs.
// Returns the downloaded data that can be used for preview and later upload.
func preDownloadImages(ctx context.Context, cfg *config.Config, quiet bool) (*preDownloadedImages, error) {
	result := &preDownloadedImages{}

	// Download icon if it's a remote URL
	if cfg.Icon != "" && isRemoteURL(cfg.Icon) {
		var spinner *ui.Spinner
		if !quiet {
			spinner = ui.NewSpinner("Downloading icon...")
			spinner.Start()
		}

		data, hash, mimeType, err := downloadRemoteImage(ctx, cfg.Icon)
		if err != nil {
			if spinner != nil {
				spinner.StopWithError("Failed to download icon")
			}
			return nil, fmt.Errorf("failed to download icon from %s: %w", cfg.Icon, err)
		}

		result.Icon = &downloadedImage{
			URL:      cfg.Icon,
			Data:     data,
			Hash:     hash,
			MimeType: mimeType,
		}

		if spinner != nil {
			spinner.StopWithSuccess("Downloaded icon")
		}
	}

	// Download screenshots if they are remote URLs
	remoteImages := 0
	for _, img := range cfg.Images {
		if isRemoteURL(img) {
			remoteImages++
		}
	}

	if remoteImages > 0 {
		var spinner *ui.Spinner
		if !quiet {
			spinner = ui.NewSpinner(fmt.Sprintf("Downloading 0/%d screenshots...", remoteImages))
			spinner.Start()
		}

		downloaded := 0
		for _, img := range cfg.Images {
			if !isRemoteURL(img) {
				continue
			}

			data, hash, mimeType, err := downloadRemoteImage(ctx, img)
			if err != nil {
				if spinner != nil {
					spinner.StopWithWarning(fmt.Sprintf("Failed to download screenshot: %v", err))
				}
				// Continue with other images - don't fail completely
				continue
			}

			downloaded++
			if spinner != nil {
				spinner.UpdateMessage(fmt.Sprintf("Downloading %d/%d screenshots...", downloaded, remoteImages))
			}

			result.Images = append(result.Images, &downloadedImage{
				URL:      img,
				Data:     data,
				Hash:     hash,
				MimeType: mimeType,
			})
		}

		if spinner != nil {
			spinner.StopWithSuccess(fmt.Sprintf("Downloaded %d screenshots", len(result.Images)))
		}
	}

	return result, nil
}

// isBlossomURL checks if a URL is already hosted on the target Blossom server.
func isBlossomURL(url, blossomServer string) bool {
	return strings.HasPrefix(url, blossomServer)
}

// downloadRemoteImage downloads an image from a URL and returns the data, hash, and mime type.
func downloadRemoteImage(ctx context.Context, url string) (data []byte, hashStr string, mimeType string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers to avoid being blocked
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	data, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to read response: %w", err)
	}

	// Compute SHA256 hash
	hash := sha256.Sum256(data)
	hashStr = hex.EncodeToString(hash[:])

	// Detect MIME type from Content-Type header or data
	mimeType = resp.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = detectMimeTypeFromData(data)
	}

	return data, hashStr, mimeType, nil
}

// detectMimeTypeFromData detects image MIME type from magic bytes.
func detectMimeTypeFromData(data []byte) string {
	if len(data) < 8 {
		return "application/octet-stream"
	}

	// PNG: 89 50 4E 47 0D 0A 1A 0A
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "image/png"
	}

	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}

	// GIF: GIF87a or GIF89a
	if string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a" {
		return "image/gif"
	}

	// WebP: RIFF....WEBP
	if string(data[:4]) == "RIFF" && len(data) >= 12 && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}

	return "application/octet-stream"
}

// resolvePath resolves a path relative to baseDir if it's not absolute.
func resolvePath(path, baseDir string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if baseDir != "" {
		return filepath.Join(baseDir, path)
	}
	return path
}

// clearSourceCache clears the cache on sources that support it.
// This is called when publishing is aborted or fails so the next run can retry.
func clearSourceCache(src source.Source) {
	if cacheClearer, ok := src.(source.CacheClearer); ok {
		_ = cacheClearer.ClearCache() // Ignore errors, best-effort
	}
}

// commitSourceCache commits pending cache data to disk on sources that support it.
// This is called after successful publishing to persist ETags etc.
func commitSourceCache(src source.Source) {
	if cacheCommitter, ok := src.(source.CacheCommitter); ok {
		_ = cacheCommitter.CommitCache() // Ignore errors, best-effort
	}
}

// detectImageMimeType returns the MIME type based on file extension.
func detectImageMimeType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// uploadItem represents a file to upload with its auth event.
type uploadItem struct {
	data       []byte
	hash       string
	mimeType   string
	authEvent  *gonostr.Event
	isAPK      bool
	uploadType string // "icon", "image", "APK" - for display
	apkPath    string
}

// uploadAndSignWithBatch handles uploads and signing when using a batch signer (e.g., NIP-07).
// It pre-creates ALL events (blossom auth + main events), batch signs them once, then performs uploads.
// preDownloaded contains images that were already downloaded before preview (to avoid re-downloading).
func uploadAndSignWithBatch(ctx context.Context, cfg *config.Config, apkInfo *apk.APKInfo, apkPath string, release *source.Release, client *blossom.Client, blossomURL string, batchSigner nostr.BatchSigner, pubkey string, relayHint string, preDownloaded *preDownloadedImages, variant string, commit string) (*nostr.EventSet, error) {
	var uploads []uploadItem
	var iconURL string
	var imageURLs []string
	expiration := time.Now().Add(blossom.AuthExpiration)

	// Determine release notes: use config if specified, otherwise use remote release notes
	releaseNotes := release.Changelog
	if cfg.ReleaseNotes != "" {
		var err error
		releaseNotes, err = source.FetchReleaseNotes(ctx, cfg.ReleaseNotes, apkInfo.VersionName, cfg.BaseDir)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch release notes: %w", err)
		}
	}

	// Collect icon upload - prefer pre-downloaded icon
	if preDownloaded != nil && preDownloaded.Icon != nil {
		// Use pre-downloaded icon (from Play Store, F-Droid, etc.)
		iconURL = fmt.Sprintf("%s/%s", client.ServerURL(), preDownloaded.Icon.Hash)
		uploads = append(uploads, uploadItem{
			data:       preDownloaded.Icon.Data,
			hash:       preDownloaded.Icon.Hash,
			mimeType:   preDownloaded.Icon.MimeType,
			authEvent:  nostr.BuildBlossomAuthEvent(preDownloaded.Icon.Hash, pubkey, expiration),
			uploadType: "icon",
		})
	} else if cfg.Icon != "" {
		if isRemoteURL(cfg.Icon) {
			// Check if already on Blossom server - if so, keep it
			if isBlossomURL(cfg.Icon, client.ServerURL()) {
				iconURL = cfg.Icon
			} else {
				// Download from remote URL and prepare for upload to Blossom (fallback)
				var iconSpinner *ui.Spinner
				if !*quietFlag {
					iconSpinner = ui.NewSpinner("Fetching icon...")
					iconSpinner.Start()
				}
				iconData, iconHash, mimeType, err := downloadRemoteImage(ctx, cfg.Icon)
				if err != nil {
					if iconSpinner != nil {
						iconSpinner.StopWithError("Failed to fetch icon")
					}
					return nil, fmt.Errorf("failed to fetch icon from %s: %w", cfg.Icon, err)
				}
				if iconSpinner != nil {
					iconSpinner.StopWithSuccess("Fetched icon")
				}
				iconURL = fmt.Sprintf("%s/%s", client.ServerURL(), iconHash)
				uploads = append(uploads, uploadItem{
					data:       iconData,
					hash:       iconHash,
					mimeType:   mimeType,
					authEvent:  nostr.BuildBlossomAuthEvent(iconHash, pubkey, expiration),
					uploadType: "icon",
				})
			}
		} else {
			iconPath := resolvePath(cfg.Icon, cfg.BaseDir)
			iconData, err := os.ReadFile(iconPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read icon file %s: %w", iconPath, err)
			}
			hash := sha256.Sum256(iconData)
			iconHash := hex.EncodeToString(hash[:])
			iconURL = fmt.Sprintf("%s/%s", client.ServerURL(), iconHash)
			uploads = append(uploads, uploadItem{
				data:       iconData,
				hash:       iconHash,
				mimeType:   detectImageMimeType(iconPath),
				authEvent:  nostr.BuildBlossomAuthEvent(iconHash, pubkey, expiration),
				uploadType: "icon",
			})
		}
	} else if apkInfo.Icon != nil {
		hash := sha256.Sum256(apkInfo.Icon)
		iconHash := hex.EncodeToString(hash[:])
		iconURL = fmt.Sprintf("%s/%s", client.ServerURL(), iconHash)
		uploads = append(uploads, uploadItem{
			data:       apkInfo.Icon,
			hash:       iconHash,
			mimeType:   "image/png",
			authEvent:  nostr.BuildBlossomAuthEvent(iconHash, pubkey, expiration),
			uploadType: "icon",
		})
	}

	// Collect pre-downloaded image uploads first
	if preDownloaded != nil && len(preDownloaded.Images) > 0 {
		for _, img := range preDownloaded.Images {
			imageURLs = append(imageURLs, fmt.Sprintf("%s/%s", client.ServerURL(), img.Hash))
			uploads = append(uploads, uploadItem{
				data:       img.Data,
				hash:       img.Hash,
				mimeType:   img.MimeType,
				authEvent:  nostr.BuildBlossomAuthEvent(img.Hash, pubkey, expiration),
				uploadType: "screenshot",
			})
		}
	}

	// Collect remaining image uploads (non-remote or not pre-downloaded)
	for i, img := range cfg.Images {
		// Skip remote images that were already handled via pre-download
		if isRemoteURL(img) && preDownloaded != nil && findPreDownloadedImage(preDownloaded.Images, img) != nil {
			continue
		}

		if isRemoteURL(img) {
			// Check if already on Blossom server - if so, keep it
			if isBlossomURL(img, client.ServerURL()) {
				imageURLs = append(imageURLs, img)
			} else {
				// Download from remote URL and prepare for upload to Blossom (fallback)
				var imgSpinner *ui.Spinner
				if !*quietFlag {
					imgSpinner = ui.NewSpinner(fmt.Sprintf("Fetching screenshot (%d/%d)...", i+1, len(cfg.Images)))
					imgSpinner.Start()
				}
				imgData, imgHash, mimeType, err := downloadRemoteImage(ctx, img)
				if err != nil {
					if imgSpinner != nil {
						imgSpinner.StopWithError(fmt.Sprintf("Failed to fetch screenshot %d", i+1))
					}
					// Log warning but continue with other images
					if *verboseFlag {
						fmt.Printf("  Warning: failed to fetch screenshot from %s: %v\n", img, err)
					}
					continue
				}
				if imgSpinner != nil {
					imgSpinner.StopWithSuccess(fmt.Sprintf("Fetched screenshot (%d/%d)", i+1, len(cfg.Images)))
				}
				imageURLs = append(imageURLs, fmt.Sprintf("%s/%s", client.ServerURL(), imgHash))
				uploads = append(uploads, uploadItem{
					data:       imgData,
					hash:       imgHash,
					mimeType:   mimeType,
					authEvent:  nostr.BuildBlossomAuthEvent(imgHash, pubkey, expiration),
					uploadType: "screenshot",
				})
			}
		} else {
			imgPath := resolvePath(img, cfg.BaseDir)
			imgData, err := os.ReadFile(imgPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read image file %s: %w", imgPath, err)
			}
			hash := sha256.Sum256(imgData)
			imgHash := hex.EncodeToString(hash[:])
			imageURLs = append(imageURLs, fmt.Sprintf("%s/%s", client.ServerURL(), imgHash))
			uploads = append(uploads, uploadItem{
				data:       imgData,
				hash:       imgHash,
				mimeType:   detectImageMimeType(imgPath),
				authEvent:  nostr.BuildBlossomAuthEvent(imgHash, pubkey, expiration),
				uploadType: "image",
			})
		}
	}

	// Add APK upload
	uploads = append(uploads, uploadItem{
		isAPK:     true,
		apkPath:   apkPath,
		hash:      apkInfo.SHA256,
		authEvent: nostr.BuildBlossomAuthEvent(apkInfo.SHA256, pubkey, expiration),
	})

	// Build main events (with pre-computed URLs)
	events := nostr.BuildEventSet(nostr.BuildEventSetParams{
		APKInfo:    apkInfo,
		Config:     cfg,
		Pubkey:     pubkey,
		BlossomURL: blossomURL,
		IconURL:    iconURL,
		ImageURLs:  imageURLs,
		Changelog:  releaseNotes,
		Variant:    variant,
		Commit:     commit,
	})

	// For batch signing, we need to pre-compute the asset event IDs and add them to release
	// before signing. The ID is computed from the event content.
	for _, asset := range events.SoftwareAssets {
		asset.PubKey = pubkey
		assetID := asset.GetID()
		events.AddAssetReference(assetID, relayHint)
	}

	// Collect ALL events to sign: auth events + main events
	allEvents := make([]*gonostr.Event, 0, len(uploads)+2+len(events.SoftwareAssets))
	for _, u := range uploads {
		allEvents = append(allEvents, u.authEvent)
	}
	allEvents = append(allEvents, events.AppMetadata, events.Release)
	allEvents = append(allEvents, events.SoftwareAssets...)

	// Pre-check existence for non-APK uploads in parallel (4 concurrent HEAD requests)
	var nonAPKHashes []string
	for _, u := range uploads {
		if !u.isAPK {
			nonAPKHashes = append(nonAPKHashes, u.hash)
		}
	}
	var existsMap map[string]bool
	if len(nonAPKHashes) > 0 {
		var checkSpinner *ui.Spinner
		if !*quietFlag {
			checkSpinner = ui.NewSpinner(fmt.Sprintf("Checking %d files...", len(nonAPKHashes)))
			checkSpinner.Start()
		}
		existsMap = client.ExistsBatch(ctx, nonAPKHashes, 4)
		if checkSpinner != nil {
			existCount := 0
			for _, exists := range existsMap {
				if exists {
					existCount++
				}
			}
			if existCount > 0 {
				checkSpinner.StopWithSuccess(fmt.Sprintf("Checked files (%d already exist)", existCount))
			} else {
				checkSpinner.StopWithSuccess("Checked files")
			}
		}
	}

	// Batch sign everything in one browser interaction
	var signSpinner *ui.Spinner
	if !*quietFlag {
		signSpinner = ui.NewSpinner(fmt.Sprintf("Signing %d events...", len(allEvents)))
		signSpinner.Start()
	}
	if err := batchSigner.SignBatch(ctx, allEvents); err != nil {
		if signSpinner != nil {
			signSpinner.StopWithError("Failed to sign events")
		}
		return nil, fmt.Errorf("failed to batch sign events: %w", err)
	}
	if signSpinner != nil {
		signSpinner.StopWithSuccess("Signed events")
	}

	// Now perform uploads with pre-signed auth events
	for _, u := range uploads {
		if u.isAPK {
			var uploadTracker *ui.DownloadTracker
			var uploadCallback func(uploaded, total int64)
			if !*quietFlag {
				fileInfo, _ := os.Stat(u.apkPath)
				var size int64
				if fileInfo != nil {
					size = fileInfo.Size()
				}
				uploadTracker = ui.NewDownloadTracker(fmt.Sprintf("Uploading APK to %s", client.ServerURL()), size)
				uploadCallback = uploadTracker.Callback()
			}
			result, err := client.UploadWithAuth(ctx, u.apkPath, u.hash, u.authEvent, uploadCallback)
			if err != nil {
				return nil, fmt.Errorf("failed to upload APK: %w", err)
			}
			if uploadTracker != nil {
				if result.Existed {
					uploadTracker.DoneWithMessage(fmt.Sprintf("APK already exists (%s)", result.URL))
				} else {
					uploadTracker.Done()
				}
			}
		} else {
			existed := existsMap[u.hash]
			if existed {
				if !*quietFlag {
					ui.PrintSuccess(fmt.Sprintf("%s already exists (%s/%s)", u.uploadType, client.ServerURL(), u.hash))
				}
			} else {
				var uploadSpinner *ui.Spinner
				if !*quietFlag {
					uploadSpinner = ui.NewSpinner(fmt.Sprintf("Uploading %s...", u.uploadType))
					uploadSpinner.Start()
				}
				_, err := client.UploadBytesWithAuthPreChecked(ctx, u.data, u.hash, u.mimeType, u.authEvent, false)
				if err != nil {
					if uploadSpinner != nil {
						uploadSpinner.StopWithError(fmt.Sprintf("Failed to upload %s", u.uploadType))
					}
					return nil, fmt.Errorf("failed to upload file: %w", err)
				}
				if uploadSpinner != nil {
					uploadSpinner.StopWithSuccess(fmt.Sprintf("Uploaded %s", u.uploadType))
				}
			}
		}
	}

	return events, nil
}

// uploadWithIndividualSigning handles uploads with regular signers (nsec, bunker).
// Each auth event is signed individually before its corresponding upload.
// preDownloaded contains images that were already downloaded before preview (to avoid re-downloading).
func uploadWithIndividualSigning(ctx context.Context, cfg *config.Config, apkInfo *apk.APKInfo, apkPath string, client *blossom.Client, signer nostr.Signer, preDownloaded *preDownloadedImages) (iconURL string, imageURLs []string, err error) {
	// Process icon: use pre-downloaded, config path, or APK
	if preDownloaded != nil && preDownloaded.Icon != nil {
		// Use pre-downloaded icon (from Play Store, F-Droid, etc.)
		var iconSpinner *ui.Spinner
		if !*quietFlag {
			iconSpinner = ui.NewSpinner("Uploading icon...")
			iconSpinner.Start()
		}
		result, err := client.UploadBytes(ctx, preDownloaded.Icon.Data, preDownloaded.Icon.Hash, preDownloaded.Icon.MimeType, signer)
		if err != nil {
			if iconSpinner != nil {
				iconSpinner.StopWithError("Failed to upload icon")
			}
			return "", nil, fmt.Errorf("failed to upload icon: %w", err)
		}
		if iconSpinner != nil {
			if result.Existed {
				iconSpinner.StopWithSuccess(fmt.Sprintf("Icon already exists (%s)", result.URL))
			} else {
				iconSpinner.StopWithSuccess("Uploaded icon")
			}
		}
		iconURL = result.URL
	} else if cfg.Icon != "" {
		if isRemoteURL(cfg.Icon) {
			// Check if already on Blossom server - if so, keep it
			if isBlossomURL(cfg.Icon, client.ServerURL()) {
				iconURL = cfg.Icon
			} else {
				// Download from remote URL and upload to Blossom (fallback if not pre-downloaded)
				var iconSpinner *ui.Spinner
				if !*quietFlag {
					iconSpinner = ui.NewSpinner("Fetching icon...")
					iconSpinner.Start()
				}
				iconData, hashStr, mimeType, err := downloadRemoteImage(ctx, cfg.Icon)
				if err != nil {
					if iconSpinner != nil {
						iconSpinner.StopWithError("Failed to fetch icon")
					}
					return "", nil, fmt.Errorf("failed to fetch icon from %s: %w", cfg.Icon, err)
				}
				if iconSpinner != nil {
					iconSpinner.UpdateMessage("Uploading icon...")
				}
				result, err := client.UploadBytes(ctx, iconData, hashStr, mimeType, signer)
				if err != nil {
					if iconSpinner != nil {
						iconSpinner.StopWithError("Failed to upload icon")
					}
					return "", nil, fmt.Errorf("failed to upload icon: %w", err)
				}
				if iconSpinner != nil {
					if result.Existed {
						iconSpinner.StopWithSuccess(fmt.Sprintf("Icon already exists (%s)", result.URL))
					} else {
						iconSpinner.StopWithSuccess("Uploaded icon")
					}
				}
				iconURL = result.URL
			}
		} else {
			iconPath := resolvePath(cfg.Icon, cfg.BaseDir)
			iconData, err := os.ReadFile(iconPath)
			if err != nil {
				return "", nil, fmt.Errorf("failed to read icon file %s: %w", iconPath, err)
			}
			hash := sha256.Sum256(iconData)
			hashStr := hex.EncodeToString(hash[:])
			mimeType := detectImageMimeType(iconPath)
			var iconSpinner *ui.Spinner
			if !*quietFlag {
				iconSpinner = ui.NewSpinner("Uploading icon...")
				iconSpinner.Start()
			}
			result, err := client.UploadBytes(ctx, iconData, hashStr, mimeType, signer)
			if err != nil {
				if iconSpinner != nil {
					iconSpinner.StopWithError("Failed to upload icon")
				}
				return "", nil, fmt.Errorf("failed to upload icon: %w", err)
			}
			if iconSpinner != nil {
				if result.Existed {
					iconSpinner.StopWithSuccess(fmt.Sprintf("Icon already exists (%s)", result.URL))
				} else {
					iconSpinner.StopWithSuccess("Uploaded icon")
				}
			}
			iconURL = result.URL
		}
	} else if apkInfo.Icon != nil {
		hash := sha256.Sum256(apkInfo.Icon)
		hashStr := hex.EncodeToString(hash[:])
		var iconSpinner *ui.Spinner
		if !*quietFlag {
			iconSpinner = ui.NewSpinner("Uploading icon...")
			iconSpinner.Start()
		}
		result, err := client.UploadBytes(ctx, apkInfo.Icon, hashStr, "image/png", signer)
		if err != nil {
			if iconSpinner != nil {
				iconSpinner.StopWithError("Failed to upload icon")
			}
			return "", nil, fmt.Errorf("failed to upload icon: %w", err)
		}
		if iconSpinner != nil {
			if result.Existed {
				iconSpinner.StopWithSuccess(fmt.Sprintf("Icon already exists (%s)", result.URL))
			} else {
				iconSpinner.StopWithSuccess("Uploaded icon")
			}
		}
		iconURL = result.URL
	}

	// Upload pre-downloaded images first
	if preDownloaded != nil && len(preDownloaded.Images) > 0 {
		for i, img := range preDownloaded.Images {
			var imgSpinner *ui.Spinner
			if !*quietFlag {
				imgSpinner = ui.NewSpinner(fmt.Sprintf("Uploading screenshot (%d/%d)...", i+1, len(preDownloaded.Images)))
				imgSpinner.Start()
			}
			result, err := client.UploadBytes(ctx, img.Data, img.Hash, img.MimeType, signer)
			if err != nil {
				if imgSpinner != nil {
					imgSpinner.StopWithError(fmt.Sprintf("Failed to upload screenshot %d", i+1))
				}
				return "", nil, fmt.Errorf("failed to upload screenshot: %w", err)
			}
			if imgSpinner != nil {
				if result.Existed {
					imgSpinner.StopWithSuccess(fmt.Sprintf("Screenshot (%d/%d) already exists (%s)", i+1, len(preDownloaded.Images), result.URL))
				} else {
					imgSpinner.StopWithSuccess(fmt.Sprintf("Uploaded screenshot (%d/%d)", i+1, len(preDownloaded.Images)))
				}
			}
			imageURLs = append(imageURLs, result.URL)
		}
	}

	// Process remaining images from config (non-remote or not pre-downloaded)
	for i, img := range cfg.Images {
		// Skip remote images that were already handled via pre-download
		if isRemoteURL(img) && preDownloaded != nil && findPreDownloadedImage(preDownloaded.Images, img) != nil {
			continue
		}

		if isRemoteURL(img) {
			// Check if already on Blossom server - if so, keep it
			if isBlossomURL(img, client.ServerURL()) {
				imageURLs = append(imageURLs, img)
			} else {
				// Download from remote URL and upload to Blossom (fallback if not pre-downloaded)
				var imgSpinner *ui.Spinner
				if !*quietFlag {
					imgSpinner = ui.NewSpinner(fmt.Sprintf("Fetching screenshot (%d/%d)...", i+1, len(cfg.Images)))
					imgSpinner.Start()
				}
				imgData, hashStr, mimeType, err := downloadRemoteImage(ctx, img)
				if err != nil {
					if imgSpinner != nil {
						imgSpinner.StopWithError(fmt.Sprintf("Failed to fetch screenshot %d", i+1))
					}
					// Log warning but continue with other images
					if *verboseFlag {
						fmt.Printf("  Warning: failed to fetch screenshot from %s: %v\n", img, err)
					}
					continue
				}
				if imgSpinner != nil {
					imgSpinner.UpdateMessage(fmt.Sprintf("Uploading screenshot (%d/%d)...", i+1, len(cfg.Images)))
				}
				result, err := client.UploadBytes(ctx, imgData, hashStr, mimeType, signer)
				if err != nil {
					if imgSpinner != nil {
						imgSpinner.StopWithError(fmt.Sprintf("Failed to upload screenshot %d", i+1))
					}
					return "", nil, fmt.Errorf("failed to upload screenshot: %w", err)
				}
				if imgSpinner != nil {
					if result.Existed {
						imgSpinner.StopWithSuccess(fmt.Sprintf("Screenshot (%d/%d) already exists (%s)", i+1, len(cfg.Images), result.URL))
					} else {
						imgSpinner.StopWithSuccess(fmt.Sprintf("Uploaded screenshot (%d/%d)", i+1, len(cfg.Images)))
					}
				}
				imageURLs = append(imageURLs, result.URL)
			}
		} else {
			imgPath := resolvePath(img, cfg.BaseDir)
			imgData, err := os.ReadFile(imgPath)
			if err != nil {
				return "", nil, fmt.Errorf("failed to read image file %s: %w", imgPath, err)
			}
			hash := sha256.Sum256(imgData)
			hashStr := hex.EncodeToString(hash[:])
			mimeType := detectImageMimeType(imgPath)
			var imgSpinner *ui.Spinner
			if !*quietFlag {
				imgSpinner = ui.NewSpinner(fmt.Sprintf("Uploading image %s...", img))
				imgSpinner.Start()
			}
			result, err := client.UploadBytes(ctx, imgData, hashStr, mimeType, signer)
			if err != nil {
				if imgSpinner != nil {
					imgSpinner.StopWithError(fmt.Sprintf("Failed to upload image %s", img))
				}
				return "", nil, fmt.Errorf("failed to upload image %s: %w", img, err)
			}
			if imgSpinner != nil {
				if result.Existed {
					imgSpinner.StopWithSuccess(fmt.Sprintf("Image %s already exists (%s)", img, result.URL))
				} else {
					imgSpinner.StopWithSuccess(fmt.Sprintf("Uploaded image %s", img))
				}
			}
			imageURLs = append(imageURLs, result.URL)
		}
	}

	// Upload APK
	var uploadTracker *ui.DownloadTracker
	var uploadCallback func(uploaded, total int64)
	if !*quietFlag {
		// Get file size for progress
		fileInfo, _ := os.Stat(apkPath)
		var size int64
		if fileInfo != nil {
			size = fileInfo.Size()
		}
		uploadTracker = ui.NewDownloadTracker(fmt.Sprintf("Uploading APK to %s", client.ServerURL()), size)
		uploadCallback = uploadTracker.Callback()
	}
	apkResult, err := client.Upload(ctx, apkPath, apkInfo.SHA256, signer, uploadCallback)
	if err != nil {
		return "", nil, fmt.Errorf("failed to upload APK: %w", err)
	}
	if uploadTracker != nil {
		if apkResult.Existed {
			uploadTracker.DoneWithMessage(fmt.Sprintf("APK already exists (%s)", apkResult.URL))
		} else {
			uploadTracker.Done()
		}
	}

	return iconURL, imageURLs, nil
}

// selectAPKInteractive prompts the user to select an APK from a ranked list.
func selectAPKInteractive(ranked []picker.ScoredAsset) (*source.Asset, error) {
	ui.PrintSectionHeader("Select APK")

	// Build option list with size and recommendation
	options := make([]string, len(ranked))
	for i, sa := range ranked {
		sizeStr := ""
		if sa.Asset.Size > 0 {
			sizeMB := float64(sa.Asset.Size) / (1024 * 1024)
			sizeStr = fmt.Sprintf(" (%.1f MB)", sizeMB)
		}

		options[i] = fmt.Sprintf("%s%s", sa.Asset.Name, sizeStr)
	}

	// Default to first (recommended) option
	idx, err := ui.SelectOption("", options, 0)
	if err != nil {
		return nil, err
	}

	return ranked[idx].Asset, nil
}

// matchVariant matches an APK filename against the variants map and returns the variant name.
// Returns empty string if no variant matches.
func matchVariant(variants map[string]string, filename string) string {
	if len(variants) == 0 {
		return ""
	}

	for name, pattern := range variants {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue // Skip invalid patterns (should have been caught in validation)
		}
		if re.MatchString(filename) {
			return name
		}
	}

	return ""
}
