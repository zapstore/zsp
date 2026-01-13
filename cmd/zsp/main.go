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
	"strconv"
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

FLAGS
  -r <url>        Repository URL (GitHub/GitLab/F-Droid)
  -s <url>        Release source URL (defaults to -r if not specified)
  -m <source>     Fetch metadata from source (repeatable: -m github -m fdroid)
  -y              Auto-confirm all prompts
  -h, --help      Show this help
  -v, --version   Print version

  --fetch-metadata <source>   Same as -m
  --extract       Extract APK metadata as JSON (local APK only)
  --check-apk     Verify config fetches and parses an arm64-v8a APK (exit 0=success)
  --preview       Show HTML preview in browser before publishing
  --port <port>   Custom port for browser preview/signing (default: 17007/17008)
  --overwrite-release  Bypass cache and re-publish even if release unchanged
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
  repository: https://github.com/user/app    # Source code repo (for display)
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
  changelog: ./CHANGELOG.md                  # Local changelog file (optional)

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

	return config.RunWizard()
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

	// If -r was provided (with or without -s), return the config
	if *repoFlag != "" {
		return cfg, nil
	}

	// No repository provided - warn and confirm
	if !*quietFlag {
		ui.PrintWarning("No repository provided. The app is likely open source - consider using -r to link to the source.")
	}

	// If --yes or --quiet, proceed without prompting
	if *yesFlag {
		return cfg, nil
	}

	// Prompt for confirmation
	proceed, err := ui.Confirm("Proceed anyway?", true)
	if err != nil {
		return nil, err
	}
	if !proceed {
		return nil, fmt.Errorf("aborted by user")
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

	// Load configuration
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Create source
	src, err := source.NewWithOptions(cfg, source.Options{
		BaseDir:   cfg.BaseDir,
		SkipCache: true, // Always fetch fresh for checking
	})
	if err != nil {
		return fmt.Errorf("failed to create source: %w", err)
	}

	if *verboseFlag {
		fmt.Printf("Source type: %s\n", src.Type())
	}

	// Fetch latest release
	if !*quietFlag {
		fmt.Println("Fetching release info...")
	}
	release, err := src.FetchLatestRelease(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch release: %w", err)
	}

	if *verboseFlag {
		fmt.Printf("Found release: %s with %d assets\n", release.Version, len(release.Assets))
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
	} else {
		ranked := picker.DefaultModel.RankAssets(apkAssets)
		if *verboseFlag {
			fmt.Println("Ranked APKs:")
			for i, sa := range ranked {
				fmt.Printf("  %d. %s (score: %.2f)\n", i+1, sa.Asset.Name, sa.Score)
			}
		}
		selectedAsset = ranked[0].Asset
	}

	if !*quietFlag {
		fmt.Printf("Selected: %s\n", selectedAsset.Name)
	}

	// Download APK if needed
	var apkPath string
	if selectedAsset.LocalPath != "" {
		apkPath = selectedAsset.LocalPath
	} else {
		if !*quietFlag {
			fmt.Printf("Downloading %s...\n", selectedAsset.Name)
		}
		apkPath, err = src.Download(ctx, selectedAsset, "", nil)
		if err != nil {
			return fmt.Errorf("failed to download APK: %w", err)
		}
		defer os.Remove(apkPath)
	}

	// Parse APK
	apkInfo, err := apk.Parse(apkPath)
	if err != nil {
		return fmt.Errorf("failed to parse APK: %w", err)
	}

	// Verify arm64 support
	if !apkInfo.IsArm64() {
		return fmt.Errorf("APK does not support arm64-v8a architecture (found: %v)", apkInfo.Architectures)
	}

	// Success - print summary
	if !*quietFlag {
		fmt.Println()
		fmt.Println("✓ APK check passed")
		fmt.Printf("  Package:  %s\n", apkInfo.PackageID)
		fmt.Printf("  Version:  %s (%d)\n", apkInfo.VersionName, apkInfo.VersionCode)
		fmt.Printf("  Arch:     %v\n", apkInfo.Architectures)
		fmt.Printf("  SHA256:   %s\n", apkInfo.SHA256)
	}

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

	// Fetch latest release
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
			fmt.Println("Release has not changed since last publish. Use --overwrite-release to publish anyway.")
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
		spinner.StopWithSuccess("Fetched release info")
	}

	if *verboseFlag {
		fmt.Printf("Found release: %s with %d assets\n", release.Version, len(release.Assets))
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
	} else {
		// Rank and select
		ranked := picker.DefaultModel.RankAssets(apkAssets)

		if *verboseFlag {
			fmt.Println("Ranked APKs:")
			for i, sa := range ranked {
				fmt.Printf("  %d. %s (score: %.2f)\n", i+1, sa.Asset.Name, sa.Score)
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
				fmt.Printf("Selected: %s\n", selectedAsset.Name)
			}
		}
	}

	// Download APK if needed
	var apkPath string
	if selectedAsset.LocalPath != "" {
		apkPath = selectedAsset.LocalPath
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
		defer os.Remove(apkPath) // Clean up temp file
	}

	// Parse APK
	apkInfo, err := apk.Parse(apkPath)
	if err != nil {
		return fmt.Errorf("failed to parse APK: %w", err)
	}

	// Verify arm64 support
	if !apkInfo.IsArm64() {
		return fmt.Errorf("APK does not support arm64-v8a architecture (found: %v)", apkInfo.Architectures)
	}

	// Display summary
	if !*quietFlag {
		ui.PrintHeader("APK Summary")
		ui.PrintKeyValue("Package", apkInfo.PackageID)
		ui.PrintKeyValue("Version", fmt.Sprintf("%s (%d)", apkInfo.VersionName, apkInfo.VersionCode))
		ui.PrintKeyValue("Label", apkInfo.Label)
		ui.PrintKeyValue("Certificate", apkInfo.CertFingerprint)
		ui.PrintKeyValue("Size", fmt.Sprintf("%.2f MB", float64(apkInfo.FileSize)/(1024*1024)))
		ui.PrintKeyValue("SHA256", apkInfo.SHA256)
		fmt.Println()
	}

	// Check if asset already exists on relays (unless --overwrite-release is set)
	relaysEnv := os.Getenv("RELAY_URLS")
	publisher := nostr.NewPublisherFromEnv(relaysEnv)
	if !*overwriteReleaseFlag && !*dryRunFlag {
		var checkSpinner *ui.Spinner
		if !*quietFlag {
			checkSpinner = ui.NewSpinner("Checking relays for existing asset...")
			checkSpinner.Start()
		}
		existingAsset, err := publisher.CheckExistingAsset(ctx, apkInfo.PackageID, apkInfo.VersionName)
		if err != nil {
			// Log warning but continue - relay might be unavailable
			if checkSpinner != nil {
				checkSpinner.StopWithWarning("Could not check relays (continuing)")
			}
			if *verboseFlag {
				fmt.Printf("  %v\n", err)
			}
		} else if existingAsset != nil {
			if checkSpinner != nil {
				checkSpinner.StopWithSuccess("Found existing asset on relay")
			}
			if !*quietFlag {
				fmt.Printf("Asset %s@%s already exists on %s\n",
					apkInfo.PackageID, apkInfo.VersionName, existingAsset.RelayURL)
				fmt.Println("Use --overwrite-release to publish anyway.")
			}
			return nil
		} else {
			if checkSpinner != nil {
				checkSpinner.StopWithSuccess("No existing asset found")
			}
		}
	}

	// Fetch metadata from external sources
	// Use -m flags if provided, otherwise auto-detect based on source type
	metadataSources := fetchMetadataFlag
	if len(metadataSources) == 0 {
		metadataSources = source.DefaultMetadataSources(cfg)
	}
	if len(metadataSources) > 0 {
		var metaSpinner *ui.Spinner
		if !*quietFlag {
			metaSpinner = ui.NewSpinner("Fetching metadata...")
			metaSpinner.Start()
		}
		fetcher := source.NewMetadataFetcherWithPackageID(cfg, apkInfo.PackageID)
		if err := fetcher.FetchMetadata(ctx, metadataSources); err != nil {
			// Log warning but continue - metadata is optional
			if metaSpinner != nil {
				metaSpinner.StopWithWarning("Metadata fetch failed (continuing)")
			}
			if *verboseFlag {
				fmt.Printf("  %v\n", err)
			}
		} else {
			if metaSpinner != nil {
				metaSpinner.StopWithSuccess("Fetched metadata")
			}
			if *verboseFlag {
				fmt.Printf("  name=%q, description=%d chars, tags=%v\n",
					cfg.Name, len(cfg.Description), cfg.Tags)
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

	// Determine changelog: use local file if specified, otherwise use remote release notes
	changelog := release.Changelog
	if cfg.Changelog != "" {
		changelogPath := resolvePath(cfg.Changelog, cfg.BaseDir)
		changelogData, err := os.ReadFile(changelogPath)
		if err != nil {
			return fmt.Errorf("failed to read changelog file %s: %w", changelogPath, err)
		}
		changelog = string(changelogData)
	}

	// Track port used for browser operations (preview and/or signing)
	browserPort := *portFlag
	previewWasShown := false

	// Show HTML preview BEFORE signing (if --preview flag or interactive prompt)
	if !*quietFlag && !*yesFlag {
		showPreview := *previewFlag

		if !showPreview {
			// Combined prompt: Y/n to preview, or enter a port number
			defaultPort := nostr.DefaultPreviewPort
			if browserPort != 0 {
				defaultPort = browserPort
			}

			response, err := ui.Prompt(fmt.Sprintf("Preview the release in a web browser at port %d? [Y/n/port]: ", defaultPort))
			if err != nil {
				return fmt.Errorf("prompt failed: %w", err)
			}

			response = strings.ToLower(strings.TrimSpace(response))
			if response == "n" || response == "no" {
				showPreview = false
			} else if response == "" || response == "y" || response == "yes" {
				showPreview = true
				if browserPort == 0 {
					browserPort = defaultPort
				}
			} else {
				// Try to parse as port number
				port, err := strconv.Atoi(response)
				if err != nil || port < 1 || port > 65535 {
					return fmt.Errorf("invalid port: %s (enter Y, n, or a port number 1-65535)", response)
				}
				showPreview = true
				browserPort = port
			}
		}

		if showPreview {
			if browserPort == 0 {
				browserPort = nostr.DefaultPreviewPort
			}

			previewData := nostr.BuildPreviewDataFromAPK(apkInfo, cfg, changelog, blossomURL, relayURLs)
			previewServer := nostr.NewPreviewServer(previewData, changelog, "", browserPort)
			url, err := previewServer.Start()
			if err != nil {
				return fmt.Errorf("failed to start preview server: %w", err)
			}

			fmt.Printf("Preview server started at %s\n", url)
			fmt.Println("Press Enter to confirm, or cancel in browser...")

			// Wait for confirmation from CLI (Enter) or browser
			cliConfirm := make(chan struct{})
			go func() {
				reader := bufio.NewReader(os.Stdin)
				reader.ReadString('\n')
				close(cliConfirm)
			}()

			var confirmed bool
			select {
			case <-cliConfirm:
				// Confirmed from CLI - signal browser to close
				previewServer.ConfirmFromCLI()
				confirmed = true
			case confirmed = <-previewServer.WaitForBrowserConfirmation():
				// Confirmed or cancelled from browser
			case <-ctx.Done():
				previewServer.Close()
				return ctx.Err()
			}
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

	// Check for SIGN_WITH (from environment or .env file)
	signWith := config.GetSignWith()
	if signWith == "" {
		if *dryRunFlag {
			// Use test key (nsec for private key = 1) for dry run
			signWith = nostr.TestNsec
		} else if !*quietFlag {
			// Interactive prompt for signing method
			ui.PrintHeader("Signing Setup")
			var err error
			signWith, err = config.PromptSignWith()
			if err != nil {
				return fmt.Errorf("signing setup failed: %w", err)
			}
		} else {
			return fmt.Errorf("SIGN_WITH environment variable is required")
		}
	}

	// For browser signer: if preview was shown, reuse that port
	// Otherwise, prompt for port if not provided via flag
	signerPort := browserPort
	if signWith == "browser" && !previewWasShown && signerPort == 0 && !*quietFlag && !*yesFlag {
		defaultPort := nostr.DefaultNIP07Port
		response, err := ui.Prompt(fmt.Sprintf("Browser signing at port %d? [Y/port]: ", defaultPort))
		if err != nil {
			return fmt.Errorf("prompt failed: %w", err)
		}

		response = strings.TrimSpace(response)
		if response == "" || strings.ToLower(response) == "y" || strings.ToLower(response) == "yes" {
			signerPort = defaultPort
		} else {
			port, err := strconv.Atoi(response)
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("invalid port: %s (enter Y or a port number 1-65535)", response)
			}
			signerPort = port
		}
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
	if !*dryRunFlag && signer.Type() != nostr.SignerNpub {
		blossomClient := blossom.NewClient(blossomURL)

		if isBatchSigner {
			// Batch signing mode: pre-collect all data, create ALL events (auth + main), sign once
			events, err = uploadAndSignWithBatch(ctx, cfg, apkInfo, apkPath, release, blossomClient, blossomURL, batchSigner, signer.PublicKey(), relayHint)
			if err != nil {
				return err
			}
		} else {
			// Regular signing mode: sign each upload auth event individually
			iconURL, imageURLs, err = uploadWithIndividualSigning(ctx, cfg, apkInfo, apkPath, blossomClient, signer)
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
				Changelog:  changelog,
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
			Changelog:  changelog,
		})
		// Sign events (will use batch signing if available)
		if err := nostr.SignEventSet(ctx, signer, events, relayHint); err != nil {
			return fmt.Errorf("failed to sign events: %w", err)
		}
	}

	// Dry run - output events and exit
	if *dryRunFlag {
		outputEvents(events)
		return nil
	}

	// For npub signer, output unsigned events
	if signer.Type() == nostr.SignerNpub {
		outputEvents(events)
		return nil
	}

	// Publish to relays (publisher was created above for relay check)

	// Confirm before publishing (unless -y flag - preview confirmation already done earlier)
	if !*yesFlag {
		confirmed, err := confirmPublish(events, publisher.RelayURLs())
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			fmt.Println("Aborted. No events were published.")
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
					failures = append(failures, fmt.Sprintf("  %s -> %s: OK", eventType, r.RelayURL))
				}
			} else {
				failures = append(failures, fmt.Sprintf("  %s -> %s: FAILED (%v)", eventType, r.RelayURL, r.Error))
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

	// If publishing failed (even partially), clear the cache so we can retry
	if !allSuccess {
		clearSourceCache(src)
		if *verboseFlag {
			fmt.Println("Cleared release cache for retry")
		}
	}

	return nil
}

// outputEvents prints events as JSON Lines (one JSON object per line).
func outputEvents(events *nostr.EventSet) {
	enc := json.NewEncoder(os.Stdout)
	enc.Encode(events.AppMetadata)
	enc.Encode(events.Release)
	enc.Encode(events.SoftwareAsset)
}

// previewEvents displays signed events in a human-readable format.
func previewEvents(events *nostr.EventSet) {
	ui.PrintHeader("Signed Events Preview")

	// App metadata (kind 32267)
	fmt.Println()
	fmt.Println(ui.Bold("Kind 32267 (App Metadata)"))
	fmt.Printf("  ID: %s\n", events.AppMetadata.ID)
	fmt.Printf("  pubkey: %s\n", events.AppMetadata.PubKey)
	fmt.Printf("  Created: %s\n", events.AppMetadata.CreatedAt.Time().Format("2006-01-02 15:04:05"))
	fmt.Println("  Tags:")
	for _, tag := range events.AppMetadata.Tags {
		fmt.Printf("    %v\n", tag)
	}
	if events.AppMetadata.Content != "" {
		fmt.Printf("  Content: %s\n", truncateString(events.AppMetadata.Content, 100))
	}
	fmt.Printf("  Sig: %s\n", events.AppMetadata.Sig)

	// Release (kind 30063)
	fmt.Println()
	fmt.Println(ui.Bold("Kind 30063 (Release)"))
	fmt.Printf("  ID: %s\n", events.Release.ID)
	fmt.Printf("  pubkey: %s\n", events.Release.PubKey)
	fmt.Printf("  Created: %s\n", events.Release.CreatedAt.Time().Format("2006-01-02 15:04:05"))
	fmt.Println("  Tags:")
	for _, tag := range events.Release.Tags {
		fmt.Printf("    %v\n", tag)
	}
	if events.Release.Content != "" {
		fmt.Printf("  Content: %s\n", truncateString(events.Release.Content, 100))
	}
	fmt.Printf("  Sig: %s\n", events.Release.Sig)

	// Software asset (kind 3063)
	fmt.Println()
	fmt.Println(ui.Bold("Kind 3063 (Software Asset)"))
	fmt.Printf("  ID: %s\n", events.SoftwareAsset.ID)
	fmt.Printf("  pubkey: %s\n", events.SoftwareAsset.PubKey)
	fmt.Printf("  Created: %s\n", events.SoftwareAsset.CreatedAt.Time().Format("2006-01-02 15:04:05"))
	fmt.Println("  Tags:")
	for _, tag := range events.SoftwareAsset.Tags {
		fmt.Printf("    %v\n", tag)
	}
	fmt.Printf("  Sig: %s\n", events.SoftwareAsset.Sig)
	fmt.Println()
}

// previewEventsJSON outputs events as formatted JSON.
func previewEventsJSON(events *nostr.EventSet) {
	ui.PrintHeader("Signed Events (JSON)")
	fmt.Println()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	fmt.Println(ui.Bold("Kind 32267 (App Metadata):"))
	enc.Encode(events.AppMetadata)
	fmt.Println()

	fmt.Println(ui.Bold("Kind 30063 (Release):"))
	enc.Encode(events.Release)
	fmt.Println()

	fmt.Println(ui.Bold("Kind 3063 (Software Asset):"))
	enc.Encode(events.SoftwareAsset)
	fmt.Println()
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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

	ui.PrintHeader("Ready to Publish")
	fmt.Printf("  App: %s v%s\n", packageID, version)
	fmt.Printf("  Kind 32267 (App metadata)\n")
	fmt.Printf("  Kind 30063 (Release)\n")
	fmt.Printf("  Kind 3063 (Software asset) x1\n")
	fmt.Println()

	for {
		options := []string{
			"Preview events (formatted)",
			"Preview events (JSON)",
			fmt.Sprintf("Publish to %s now", strings.Join(relayURLs, ", ")),
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
func uploadAndSignWithBatch(ctx context.Context, cfg *config.Config, apkInfo *apk.APKInfo, apkPath string, release *source.Release, client *blossom.Client, blossomURL string, batchSigner nostr.BatchSigner, pubkey string, relayHint string) (*nostr.EventSet, error) {
	var uploads []uploadItem
	var iconURL string
	var imageURLs []string
	expiration := time.Now().Add(blossom.AuthExpiration)

	// Determine changelog: use local file if specified, otherwise use remote release notes
	changelog := release.Changelog
	if cfg.Changelog != "" {
		changelogPath := resolvePath(cfg.Changelog, cfg.BaseDir)
		changelogData, err := os.ReadFile(changelogPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read changelog file %s: %w", changelogPath, err)
		}
		changelog = string(changelogData)
	}

	// Collect icon upload
	if cfg.Icon != "" {
		if isRemoteURL(cfg.Icon) {
			// Check if already on Blossom server - if so, keep it
			if isBlossomURL(cfg.Icon, client.ServerURL()) {
				iconURL = cfg.Icon
			} else {
				// Download from remote URL and prepare for upload to Blossom
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

	// Collect image uploads
	for i, img := range cfg.Images {
		if isRemoteURL(img) {
			// Check if already on Blossom server - if so, keep it
			if isBlossomURL(img, client.ServerURL()) {
				imageURLs = append(imageURLs, img)
			} else {
				// Download from remote URL and prepare for upload to Blossom
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
		Changelog:  changelog,
	})

	// For batch signing, we need to pre-compute the asset event ID and add it to release
	// before signing. The ID is computed from the event content.
	events.SoftwareAsset.PubKey = pubkey
	assetID := events.SoftwareAsset.GetID()
	events.AddAssetReference(assetID, relayHint)

	// Collect ALL events to sign: auth events + main events
	allEvents := make([]*gonostr.Event, 0, len(uploads)+3)
	for _, u := range uploads {
		allEvents = append(allEvents, u.authEvent)
	}
	allEvents = append(allEvents, events.AppMetadata, events.Release, events.SoftwareAsset)

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
			_, err := client.UploadWithAuth(ctx, u.apkPath, u.hash, u.authEvent, uploadCallback)
			if err != nil {
				return nil, fmt.Errorf("failed to upload APK: %w", err)
			}
			if uploadTracker != nil {
				uploadTracker.Done()
			}
		} else {
			var uploadSpinner *ui.Spinner
			if !*quietFlag {
				uploadSpinner = ui.NewSpinner(fmt.Sprintf("Uploading %s...", u.uploadType))
				uploadSpinner.Start()
			}
			_, err := client.UploadBytesWithAuth(ctx, u.data, u.hash, u.mimeType, u.authEvent)
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

	return events, nil
}

// uploadWithIndividualSigning handles uploads with regular signers (nsec, bunker).
// Each auth event is signed individually before its corresponding upload.
func uploadWithIndividualSigning(ctx context.Context, cfg *config.Config, apkInfo *apk.APKInfo, apkPath string, client *blossom.Client, signer nostr.Signer) (iconURL string, imageURLs []string, err error) {
	// Process icon: from config or APK
	if cfg.Icon != "" {
		if isRemoteURL(cfg.Icon) {
			// Check if already on Blossom server - if so, keep it
			if isBlossomURL(cfg.Icon, client.ServerURL()) {
				iconURL = cfg.Icon
			} else {
				// Download from remote URL and upload to Blossom
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
					iconSpinner.StopWithSuccess("Uploaded icon")
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
				iconSpinner.StopWithSuccess("Uploaded icon")
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
			iconSpinner.StopWithSuccess("Uploaded icon")
		}
		iconURL = result.URL
	}

	// Process images from config
	for i, img := range cfg.Images {
		if isRemoteURL(img) {
			// Check if already on Blossom server - if so, keep it
			if isBlossomURL(img, client.ServerURL()) {
				imageURLs = append(imageURLs, img)
			} else {
				// Download from remote URL and upload to Blossom
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
					imgSpinner.StopWithSuccess(fmt.Sprintf("Uploaded screenshot (%d/%d)", i+1, len(cfg.Images)))
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
				imgSpinner.StopWithSuccess(fmt.Sprintf("Uploaded image %s", img))
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
	_, err = client.Upload(ctx, apkPath, apkInfo.SHA256, signer, uploadCallback)
	if err != nil {
		return "", nil, fmt.Errorf("failed to upload APK: %w", err)
	}
	if uploadTracker != nil {
		uploadTracker.Done()
	}

	return iconURL, imageURLs, nil
}

// selectAPKInteractive prompts the user to select an APK from a ranked list.
func selectAPKInteractive(ranked []picker.ScoredAsset) (*source.Asset, error) {
	fmt.Println()
	ui.PrintHeader("Multiple APKs Found")

	// Build option list with size and recommendation
	options := make([]string, len(ranked))
	for i, sa := range ranked {
		sizeStr := ""
		if sa.Asset.Size > 0 {
			sizeMB := float64(sa.Asset.Size) / (1024 * 1024)
			sizeStr = fmt.Sprintf(" (%.1f MB)", sizeMB)
		}

		recommended := ""
		if i == 0 {
			recommended = " [recommended]"
		}

		options[i] = fmt.Sprintf("%s%s%s", sa.Asset.Name, sizeStr, recommended)
	}

	// Default to first (recommended) option
	idx, err := ui.SelectOption("Select APK to publish:", options, 0)
	if err != nil {
		return nil, err
	}

	return ranked[idx].Asset, nil
}
