package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/ui"
	"gopkg.in/yaml.v3"
)

// ErrWizardComplete is returned when the wizard completes successfully.
// The caller should display the suggested command and exit without auto-running.
var ErrWizardComplete = errors.New("wizard complete")

// APKBasicInfo contains basic information extracted from an APK for the wizard.
type APKBasicInfo struct {
	PackageID string // Package identifier (e.g., "com.example.app")
	AppName   string // App name/label from the APK
}

// APKInfoFetcher is a callback that fetches basic APK info.
// It receives the config with repo/release source info and returns APK info.
type APKInfoFetcher func(cfg *Config, matchPattern string) *APKBasicInfo

// WizardOptions configures the wizard behavior.
type WizardOptions struct {
	// PackageID is the app's package ID (e.g., "com.example.app").
	// If provided, enables checking metadata source availability.
	// If empty and FetchAPKInfo is set, FetchAPKInfo will be called.
	PackageID string

	// AppName is the app's display name from the APK.
	// If empty and FetchAPKInfo is set, FetchAPKInfo will be called.
	AppName string

	// FetchAPKInfo is called to fetch APK info if PackageID is empty.
	// This allows the caller to provide the APK download/parsing logic.
	FetchAPKInfo APKInfoFetcher
}

// MetadataSourceOption represents a metadata source that can be selected in the wizard.
type MetadataSourceOption struct {
	Name      string // Display name
	Value     string // CLI flag value (e.g., "fdroid", "playstore")
	Available bool   // Whether the source is available for this app
}

// RunWizard runs the interactive configuration wizard.
// RunWizard runs the interactive configuration wizard.
// If defaults is non-nil, those values are used as defaults for prompts.
func RunWizard(defaults *Config) (*Config, error) {
	return RunWizardWithOptions(defaults, WizardOptions{})
}

// RunWizardWithOptions runs the wizard with additional options.
func RunWizardWithOptions(defaults *Config, opts WizardOptions) (*Config, error) {
	fmt.Print(ui.RenderLogo())
	if defaults != nil {
		fmt.Println(ui.Title("Edit Configuration"))
	} else {
		fmt.Println(ui.Title("Welcome to the Wizard of Publishing 🧙"))
		fmt.Println(ui.Dim("Let's get your app published in no time."))
	}
	fmt.Println()

	// Initialize config from defaults or empty
	cfg := &Config{}
	if defaults != nil {
		*cfg = *defaults // Copy defaults
	}

	// Use package ID from options
	packageID := opts.PackageID

	// Step 1: Get repository URL (optional for closed source apps)
	var sourceType SourceType
	var needsReleaseSource bool // True if we need to prompt for -s

	defaultRepo := cfg.Repository
	fmt.Println(ui.Dim("Press Enter to skip if this is a closed-source app."))

repoLoop:
	for {
		source, err := ui.PromptDefault("Repository URL (optional)", defaultRepo)
		if err != nil {
			return nil, err
		}

		// Repository is optional
		if source == "" {
			needsReleaseSource = true
			fmt.Printf("%s No repository - will need a release source\n", ui.Info("ℹ"))
			break repoLoop
		}

		// Reset config for retry
		cfg.Repository = ""
		cfg.Local = ""

		// Detect source type
		sourceType = DetectSourceType(source)
		if sourceType == SourceUnknown {
			// Check if it's a local path
			if _, err := os.Stat(source); err == nil {
				cfg.Local = source
				sourceType = SourceLocal
			} else if strings.Contains(source, "*") {
				// Glob pattern
				cfg.Local = source
				sourceType = SourceLocal
			} else {
				// Assume it's a URL, add https:// if needed
				if !strings.Contains(source, "://") {
					source = "https://" + source
				}
				cfg.Repository = source
				sourceType = DetectSourceType(source)
			}
		} else {
			// Ensure URL has scheme
			if !strings.Contains(source, "://") {
				source = "https://" + source
			}
			cfg.Repository = source
		}

		fmt.Printf("\n%s Detected: %s\n", ui.Info("ℹ"), sourceType)

		// Web sources (unknown type) are not supported in the wizard
		if sourceType == SourceUnknown {
			fmt.Printf("\n%s Web sources require YAML configuration.\n", ui.Warning("⚠"))
			fmt.Println(ui.Dim("The wizard supports GitHub, GitLab, Gitea, F-Droid, or local paths."))
			fmt.Println(ui.Dim("For web sources, create a zapstore.yaml with release_source config."))
			fmt.Println()
			return nil, fmt.Errorf("unsupported source type for wizard")
		}

		// Validate repository if GitHub or GitLab
		hasWarning := false
		noViableAPKs := false
		if sourceType == SourceGitHub || sourceType == SourceGitLab {
			fmt.Printf("%s Checking for releases...\n", ui.Dim("⋯"))

			var validation *releaseValidation
			if sourceType == SourceGitHub {
				validation = validateGitHubRepo(GetGitHubRepo(cfg.Repository))
			} else {
				validation = validateGitLabRepo(GetGitLabRepo(cfg.Repository))
			}

			if validation.Error != nil {
				fmt.Printf("%s Could not validate: %v\n", ui.Warning("⚠"), validation.Error)
				hasWarning = true
				noViableAPKs = true
			} else if !validation.HasReleases {
				fmt.Printf("%s No releases found\n", ui.Warning("⚠"))
				hasWarning = true
				noViableAPKs = true
			} else if validation.APKCount == 0 {
				fmt.Printf("%s Release found but no APK assets\n", ui.Warning("⚠"))
				hasWarning = true
				noViableAPKs = true
			} else {
				// Filter to viable APKs (exclude debug, x86, etc.)
				viableNames := filterViableAPKNames(validation.APKNames)

				if len(viableNames) == 0 {
					fmt.Printf("%s Found %d APK(s) but none are viable (all debug/x86/etc)\n", ui.Warning("⚠"), validation.APKCount)
					hasWarning = true
					noViableAPKs = true
				} else {
					// Auto-select best APK (picker will handle selection during fetch)
					bestName := selectBestAPKName(viableNames)
					fmt.Printf("%s Found APK: %s\n", ui.Success("✓"), bestName)
				}
			}
		}

		// If warning, ask what to do
		if hasWarning {
			if noViableAPKs {
				// No APKs found - offer to specify release source or retry
				fmt.Println()
				options := []string{
					"Specify a different release source",
					"Re-enter repository URL",
					"Continue anyway (repo only for display)",
				}
				idx, err := ui.SelectOption("What would you like to do?", options, 0)
				if err != nil {
					return nil, err
				}
				switch idx {
				case 0:
					needsReleaseSource = true
					break repoLoop
				case 1:
					fmt.Println()
					defaultRepo = source
					continue repoLoop
				case 2:
					break repoLoop
				}
			} else {
				proceed, _ := ui.Confirm("Proceed anyway?", false)
				if !proceed {
					fmt.Println()
					defaultRepo = source
					continue repoLoop
				}
			}
		}

		break repoLoop
	}

	fmt.Println()

	// Step 2: Get release source if needed (repo skipped or no viable APKs)
	var releaseSourceURL string
	defaultReleaseSource := ""
	if cfg.ReleaseSource != nil {
		defaultReleaseSource = cfg.ReleaseSource.URL
	}

	if needsReleaseSource {
		fmt.Println(ui.Dim("Specify where to fetch APK releases from."))
		fmt.Println(ui.Dim("Examples: github.com/user/repo, f-droid.org/packages/com.app, codeberg.org/user/repo"))

		for {
			source, err := ui.PromptDefault("Release source URL", defaultReleaseSource)
			if err != nil {
				return nil, err
			}

			if source == "" {
				// Release source is required if no repo
				if cfg.Repository == "" && cfg.Local == "" {
					fmt.Printf("%s Release source is required when no repository is specified\n", ui.Warning("⚠"))
					continue
				}
				break
			}

			// Ensure URL has scheme
			if !strings.Contains(source, "://") {
				source = "https://" + source
			}

			rsType := DetectSourceType(source)
			fmt.Printf("%s Detected: %s\n", ui.Info("ℹ"), rsType)

			// Web sources (unknown type) are not supported in the wizard
			if rsType == SourceUnknown {
				fmt.Printf("\n%s Web sources require YAML configuration.\n", ui.Warning("⚠"))
				fmt.Println(ui.Dim("The wizard supports GitHub, GitLab, Gitea, or F-Droid URLs."))
				fmt.Println(ui.Dim("For web sources, create a zapstore.yaml with release_source config."))
				fmt.Println()
				return nil, fmt.Errorf("unsupported source type for wizard")
			}

			releaseSourceURL = source
			break
		}

		fmt.Println()
	}

	// Step 3: Fetch APK info (for metadata source availability checking)
	// Build temporary config for fetching
	appName := opts.AppName
	if packageID == "" && opts.FetchAPKInfo != nil {
		tempCfg := &Config{
			Repository: cfg.Repository,
			Local:      cfg.Local,
		}
		if releaseSourceURL != "" {
			tempCfg.ReleaseSource = &ReleaseSource{URL: releaseSourceURL}
		}
		// Only fetch if we have a source
		if tempCfg.Repository != "" || tempCfg.Local != "" || tempCfg.ReleaseSource != nil {
			if info := opts.FetchAPKInfo(tempCfg, ""); info != nil {
				packageID = info.PackageID
				appName = info.AppName
			}
		}
	}

	// Step 5: Ask about metadata sources (multi-select)
	var selectedMetadataSources []string
	ctx := ui.GetContext()

	// Determine effective source type for pre-selection
	effectiveSourceType := sourceType
	if effectiveSourceType == SourceUnknown || effectiveSourceType == SourceLocal {
		if releaseSourceURL != "" {
			effectiveSourceType = DetectSourceType(releaseSourceURL)
		}
	}

	// Build available metadata sources based on package ID availability
	availableSources := BuildAvailableMetadataSources(ctx, packageID, effectiveSourceType)

	if len(availableSources) > 0 {
		fmt.Println()

		sourceNames := make([]string, len(availableSources))
		for i, s := range availableSources {
			sourceNames[i] = s.Name
		}

		// Determine which sources to pre-select
		var preselected []int
		for i, s := range availableSources {
			// Pre-select GitHub if source is GitHub
			if s.Value == "github" && effectiveSourceType == SourceGitHub {
				preselected = append(preselected, i)
			}
		}

		fmt.Println(ui.Bold("Metadata available in the following sources"))
		fmt.Println(ui.Dim("Optionally pull app metadata (description, screenshots) from these."))
		selectedIndices, err := ui.SelectMultipleWithDefaults("", sourceNames, preselected)
		if err != nil {
			// User aborted, continue without metadata sources
			selectedIndices = nil
		}

		for _, idx := range selectedIndices {
			selectedMetadataSources = append(selectedMetadataSources, availableSources[idx].Value)
		}

		fmt.Println()
	}

	// Step 4: Build command (shown at the end)
	command := buildCommand(cfg, releaseSourceURL, "", selectedMetadataSources)

	// Step 5: Ask about metadata overrides
	var metadataPrompt string
	if len(selectedMetadataSources) > 0 {
		sourceList := formatSourceList(selectedMetadataSources)
		metadataPrompt = fmt.Sprintf("You're fetching metadata from %s. Would you like to override or further add metadata, perhaps local?", sourceList)
	} else {
		metadataPrompt = "Would you like to add metadata?"
	}
	wantMetadataOverrides, err := ui.Confirm(metadataPrompt, false)
	if err != nil {
		return nil, err
	}

	if wantMetadataOverrides {
		fmt.Println()
		fmt.Println(ui.Dim("These override values fetched from metadata sources. Press Enter to skip."))
		fmt.Println()

		// Basic info
		// Use APK app name as default, but only save to config if user enters something different
		defaultName := cfg.Name
		if defaultName == "" {
			defaultName = appName
		}
		name, _ := ui.PromptDefault("App name", defaultName)
		if name != "" && name != appName {
			// Only save if user entered something different from APK name
			cfg.Name = name
		} else {
			cfg.Name = ""
		}

		summary, _ := ui.PromptDefault("Summary (short tagline)", cfg.Summary)
		if summary != "" {
			cfg.Summary = summary
		} else {
			cfg.Summary = ""
		}

		description, _ := ui.PromptDefault("Description", cfg.Description)
		if description != "" {
			cfg.Description = description
		} else {
			cfg.Description = ""
		}

		defaultTags := strings.Join(cfg.Tags, " ")
		tagsStr, _ := ui.PromptDefault("Tags (space-separated)", defaultTags)
		if tagsStr != "" {
			cfg.Tags = strings.Fields(tagsStr)
		} else {
			cfg.Tags = nil
		}

		fmt.Println()

		// URLs and links
		website, _ := ui.PromptDefault("Website URL", cfg.Website)
		if website != "" {
			cfg.Website = website
		} else {
			cfg.Website = ""
		}

		license, _ := ui.PromptDefault("License (e.g., MIT, GPL-3.0, Apache-2.0)", cfg.License)
		if license != "" {
			cfg.License = license
		} else {
			cfg.License = ""
		}

		fmt.Println()

		// Media
		defaultImages := strings.Join(cfg.Images, " ")
		imagesStr, _ := ui.PromptDefault("Screenshot URLs or local paths (space-separated)", defaultImages)
		if imagesStr != "" {
			cfg.Images = strings.Fields(imagesStr)
		} else {
			cfg.Images = nil
		}

		fmt.Println()

		// Nostr-specific (optional)
		defaultNIPs := strings.Join(cfg.SupportedNIPs, " ")
		nipsStr, _ := ui.PromptDefault("Supported NIPs (space-separated, e.g., 01 07 19)", defaultNIPs)
		if nipsStr != "" {
			cfg.SupportedNIPs = strings.Fields(nipsStr)
		} else {
			cfg.SupportedNIPs = nil
		}

		fmt.Println()
	}

	// Step 7: Signing setup (always before showing final command)
	if !hasSignWith() {
		fmt.Println()
		fmt.Println(ui.Bold("Signing setup"))
		_, err := PromptSignWith()
		if err != nil {
			return nil, err
		}
		fmt.Println()
	}

	// Step 6: Save config if settings require it
	// Config is needed for: metadata overrides or release source URL
	// Metadata sources alone don't need config - they're on the command line
	needsConfig := wantMetadataOverrides || releaseSourceURL != ""

	// Check if interrupted before saving
	if ui.IsInterrupted() {
		return nil, ui.ErrInterrupted
	}

	if needsConfig {
		// Store release source in config (both the struct and raw YAML node for marshaling)
		if releaseSourceURL != "" {
			cfg.ReleaseSource = &ReleaseSource{URL: releaseSourceURL}
			// Set ReleaseSourceRaw for YAML serialization (ReleaseSource has yaml:"-")
			cfg.ReleaseSourceRaw = yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: releaseSourceURL,
			}
		}

		// Store metadata sources in config if we're saving one anyway
		if len(selectedMetadataSources) > 0 {
			cfg.MetadataSources = selectedMetadataSources
		}

		// Generate and save config
		yamlBytes, err := yaml.Marshal(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to generate YAML: %w", err)
		}

		if err := os.WriteFile("zapstore.yaml", yamlBytes, 0644); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
		ui.PrintSuccess("Saved to zapstore.yaml")
		fmt.Println()

		// Show simplified command since config was saved
		fmt.Println(ui.Bold("🎉 Your command is ready! Run this to publish:"))
		fmt.Println()
		fmt.Printf("  %s\n", ui.Success("zsp publish"))
		fmt.Println()
	} else {
		// No config needed, just show the command
		fmt.Println(ui.Bold("🎉 Your command is ready! Run this to publish:"))
		fmt.Println()
		fmt.Printf("  %s\n", ui.Success(command))
		fmt.Println()
	}

	// Return sentinel error so caller knows not to auto-run
	return nil, ErrWizardComplete
}

// buildCommand constructs the CLI command from wizard inputs.
func buildCommand(cfg *Config, releaseSource, match string, metadataSources []string) string {
	var parts []string
	parts = append(parts, "zsp publish")

	// Add repository flag
	if cfg.Repository != "" {
		repo := cfg.Repository
		// Strip https:// for cleaner display
		repo = strings.TrimPrefix(repo, "https://")
		parts = append(parts, fmt.Sprintf("-r %s", repo))
	} else if cfg.Local != "" {
		parts = append(parts, cfg.Local)
	}

	// Add release source if different from repository
	if releaseSource != "" {
		rs := releaseSource
		// Strip https:// for cleaner display
		rs = strings.TrimPrefix(rs, "https://")
		parts = append(parts, fmt.Sprintf("-s %s", rs))
	}

	// Add match pattern if specified
	if match != "" {
		parts = append(parts, fmt.Sprintf("--match %q", match))
	}

	// Add metadata sources
	for _, src := range metadataSources {
		parts = append(parts, fmt.Sprintf("-m %s", src))
	}

	return strings.Join(parts, " ")
}

// formatSourceList formats metadata source values into a friendly display string.
// e.g., ["github", "fdroid"] -> "GitHub and F-Droid"
func formatSourceList(sources []string) string {
	names := make([]string, len(sources))
	for i, src := range sources {
		switch src {
		case "github":
			names[i] = "GitHub"
		case "fdroid":
			names[i] = "F-Droid"
		case "playstore":
			names[i] = "Play Store"
		default:
			names[i] = src
		}
	}

	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

// BuildAvailableMetadataSources checks which metadata sources are available for the given package ID.
// It checks F-Droid and Play Store availability if a package ID is provided.
func BuildAvailableMetadataSources(ctx context.Context, packageID string, sourceType SourceType) []MetadataSourceOption {
	var sources []MetadataSourceOption

	// GitHub is always available if source is GitHub
	if sourceType == SourceGitHub {
		sources = append(sources, MetadataSourceOption{Name: "GitHub", Value: "github", Available: true})
	}

	// If we don't have a package ID, only return GitHub (if applicable)
	if packageID == "" {
		return sources
	}

	// Check F-Droid and Play Store availability in parallel
	spinner := ui.NewSpinner("Checking metadata source availability...")
	spinner.Start()

	fdroidCh := make(chan bool, 1)
	playstoreCh := make(chan bool, 1)

	go func() { fdroidCh <- CheckFDroidAvailability(ctx, packageID) }()
	go func() { playstoreCh <- CheckPlayStoreAvailability(ctx, packageID) }()

	fdroidAvailable := <-fdroidCh
	playStoreAvailable := <-playstoreCh

	var found []string
	if fdroidAvailable {
		sources = append(sources, MetadataSourceOption{Name: "F-Droid", Value: "fdroid", Available: true})
		found = append(found, "F-Droid")
	}
	if playStoreAvailable {
		sources = append(sources, MetadataSourceOption{Name: "Play Store", Value: "playstore", Available: true})
		found = append(found, "Play Store")
	}

	if len(found) > 0 {
		spinner.StopWithSuccess(fmt.Sprintf("Found on: %s", strings.Join(found, ", ")))
	} else {
		spinner.StopWithSuccess("Not found on F-Droid or Play Store")
	}

	return sources
}

// CheckFDroidAvailability checks if an app exists on F-Droid.
func CheckFDroidAvailability(ctx context.Context, packageID string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://f-droid.org/en/packages/%s/", packageID)
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return false
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; zsp/1.0)")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// CheckPlayStoreAvailability checks if an app exists on the Play Store.
func CheckPlayStoreAvailability(ctx context.Context, packageID string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://play.google.com/store/apps/details?id=%s", packageID)
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return false
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; zsp/1.0)")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// filterViableAPKNames filters out APK names that are clearly not what we want.
// Excludes: debug builds, x86/x86_64 architecture-specific builds, armeabi (32-bit ARM).
func filterViableAPKNames(names []string) []string {
	var viable []string
	for _, name := range names {
		lower := strings.ToLower(name)

		// Skip debug builds
		if strings.Contains(lower, "debug") {
			continue
		}

		// Skip x86 architecture variants (but not if it also says arm64/universal)
		if (strings.Contains(lower, "x86") || strings.Contains(lower, "x86_64")) &&
			!strings.Contains(lower, "arm64") && !strings.Contains(lower, "universal") {
			continue
		}

		// Skip armeabi-only builds (old 32-bit ARM, but not if it's also arm64)
		if strings.Contains(lower, "armeabi") && !strings.Contains(lower, "arm64") {
			continue
		}

		viable = append(viable, name)
	}
	return viable
}

// selectBestAPKName selects the best APK from a list of names using simple preference.
// For the wizard, we just need any APK to extract the app ID - prefer arm64 > fdroid > universal.
// Note: The actual APK selection during publish uses picker.PickBest with full scoring.
func selectBestAPKName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	if len(names) == 1 {
		return names[0]
	}

	// Simple preference order for wizard display
	for _, name := range names {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "arm64") {
			return name
		}
	}
	for _, name := range names {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "fdroid") || strings.Contains(lower, "foss") {
			return name
		}
	}
	for _, name := range names {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "universal") {
			return name
		}
	}
	return names[0]
}

// GetSignWith returns SIGN_WITH from environment or .env file.
func GetSignWith() string {
	// Check environment variable first
	if value := os.Getenv("SIGN_WITH"); value != "" {
		return value
	}

	// Check .env file
	data, err := os.ReadFile(".env")
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SIGN_WITH=") {
			return strings.TrimPrefix(line, "SIGN_WITH=")
		}
	}

	return ""
}

// hasSignWith checks if SIGN_WITH is set in environment or .env file.
func hasSignWith() bool {
	return GetSignWith() != ""
}

// PromptSignWith prompts for SIGN_WITH if not set.
func PromptSignWith() (string, error) {
	options := []string{
		"Private key (nsec)",
		"Public key (npub) - outputs unsigned events",
		"Bunker connection (bunker://)",
		"Browser extension (NIP-07)",
		"Dry run (NSEC=1)",
	}

	idx, err := ui.SelectOption("", options, -1)
	if err != nil {
		return "", err
	}

	var signWith string
	switch idx {
	case 0:
		signWith, err = ui.PromptSecret("Enter your nsec")
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(signWith, "nsec1") {
			return "", fmt.Errorf("invalid nsec format")
		}
	case 1:
		signWith, err = ui.Prompt("Enter your npub: ")
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(signWith, "npub1") {
			return "", fmt.Errorf("invalid npub format")
		}
	case 2:
		signWith, err = ui.Prompt("Enter bunker URL: ")
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(signWith, "bunker://") {
			return "", fmt.Errorf("invalid bunker URL format")
		}
	case 3:
		signWith = "browser"
	case 4:
		// Test nsec (private key = 1) for dry-run mode
		signWith = "nsec1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqsmhltgl"
	}

	// Offer to save to .env
	saveEnv, err := ui.Confirm("Save to .env for future runs?", true)
	if err != nil {
		return "", err
	}

	if saveEnv {
		envContent := fmt.Sprintf("SIGN_WITH=%s\n", signWith)
		if err := appendToEnvFile(envContent); err != nil {
			ui.PrintWarning(fmt.Sprintf("Could not save to .env: %v", err))
		} else {
			ui.PrintSuccess("Saved to .env")
		}
	}

	return signWith, nil
}

// appendToEnvFile appends content to .env file.
func appendToEnvFile(content string) error {
	f, err := os.OpenFile(".env", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(content)
	return err
}

// releaseValidation holds the result of validating a repository.
type releaseValidation struct {
	HasReleases bool
	APKCount    int
	APKNames    []string
	Error       error
}

// validateGitHubRepo checks if a GitHub repo has releases with APK assets.
func validateGitHubRepo(repoPath string) *releaseValidation {
	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 {
		return &releaseValidation{Error: fmt.Errorf("invalid repo path")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", parts[0], parts[1])
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return &releaseValidation{Error: err}
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &releaseValidation{Error: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &releaseValidation{HasReleases: false}
	}
	if resp.StatusCode != http.StatusOK {
		return &releaseValidation{Error: fmt.Errorf("API error: %d", resp.StatusCode)}
	}

	var release struct {
		Assets []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return &releaseValidation{Error: err}
	}

	result := &releaseValidation{HasReleases: true}
	for _, asset := range release.Assets {
		if strings.HasSuffix(strings.ToLower(asset.Name), ".apk") {
			result.APKCount++
			result.APKNames = append(result.APKNames, asset.Name)
		}
	}
	return result
}

// validateGitLabRepo checks if a GitLab repo has releases with APK assets.
func validateGitLabRepo(repoPath string) *releaseValidation {
	// URL-encode the project path for GitLab API
	encodedPath := strings.ReplaceAll(repoPath, "/", "%2F")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://gitlab.com/api/v4/projects/%s/releases", encodedPath)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return &releaseValidation{Error: err}
	}

	if token := os.Getenv("GITLAB_TOKEN"); token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &releaseValidation{Error: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &releaseValidation{HasReleases: false}
	}
	if resp.StatusCode != http.StatusOK {
		return &releaseValidation{Error: fmt.Errorf("API error: %d", resp.StatusCode)}
	}

	var releases []struct {
		Assets struct {
			Links []struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"links"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return &releaseValidation{Error: err}
	}

	if len(releases) == 0 {
		return &releaseValidation{HasReleases: false}
	}

	result := &releaseValidation{HasReleases: true}
	// Check latest release
	if len(releases) > 0 {
		for _, link := range releases[0].Assets.Links {
			name := link.Name
			if name == "" {
				// Extract filename from URL
				parts := strings.Split(link.URL, "/")
				if len(parts) > 0 {
					name = parts[len(parts)-1]
				}
			}
			if strings.HasSuffix(strings.ToLower(name), ".apk") {
				result.APKCount++
				result.APKNames = append(result.APKNames, name)
			}
		}
	}
	return result
}
