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

// PubkeyResolver resolves the npub from a SIGN_WITH value.
// It may make network calls (e.g., connecting to a bunker) and can return an error.
// Returns the npub (bech32) on success, or an error if resolution fails.
type PubkeyResolver func(ctx context.Context, signWith string) (npub string, err error)

// AppExistsChecker checks whether an app with the given package ID already exists on relays.
// Returns true if the app is found, false otherwise.
type AppExistsChecker func(ctx context.Context, packageID string) bool

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

	// ResolvePubkey resolves the npub from a SIGN_WITH value, including async
	// sources like bunker:// and browser. If nil, only nsec/npub are resolved.
	ResolvePubkey PubkeyResolver

	// CheckAppExists checks whether an app already exists on the relay.
	// If set and the app is found, the pubkey step is skipped (app already published).
	CheckAppExists AppExistsChecker
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
	if defaultRepo != "" {
		fmt.Println(ui.Dim("Enter a space to clear (closed-source app), or press Enter to keep."))
	} else {
		fmt.Println(ui.Dim("Press Enter to skip if this is a closed-source app."))
	}

repoLoop:
	for {
		source, err := ui.PromptDefault("Repository URL (optional)", defaultRepo)
		if err != nil {
			return nil, err
		}

		// A single space means "clear this field" (used when editing to skip/remove the repo)
		if source == " " {
			source = ""
		}

		// Repository is optional
		if source == "" {
			needsReleaseSource = true
			fmt.Printf("%s No repository - will need a release source\n", ui.Info("ℹ"))
			break repoLoop
		}

		// Reset config for retry
		cfg.Repository = ""
		cfg.ReleaseSource = nil

		// Detect source type
		sourceType = DetectSourceType(source)
		if sourceType == SourceUnknown {
			// Check if it's a local path
			if _, err := os.Stat(source); err == nil {
				cfg.ReleaseSource = &ReleaseSource{LocalPath: source}
				sourceType = SourceLocal
			} else if strings.Contains(source, "*") {
				// Glob pattern
				cfg.ReleaseSource = &ReleaseSource{LocalPath: source}
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
			if cfg.Repository == "" && cfg.ReleaseSource == nil {
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
			Repository:    cfg.Repository,
			ReleaseSource: cfg.ReleaseSource,
		}
		if releaseSourceURL != "" {
			tempCfg.ReleaseSource = &ReleaseSource{URL: releaseSourceURL}
		}
		// Only fetch if we have a source
		if tempCfg.Repository != "" || tempCfg.ReleaseSource != nil {
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

	// Step 4: Build command (kept for reference but config is always written)
	_ = buildCommand(cfg, releaseSourceURL, "", selectedMetadataSources)

	// Step 5: Ask about metadata overrides
	var metadataPrompt string
	if len(selectedMetadataSources) > 0 {
		sourceList := formatSourceList(selectedMetadataSources)
		metadataPrompt = fmt.Sprintf("Fetching metadata from %s.\nWould you like to provide a name, description, and more now?", sourceList)
	} else {
		metadataPrompt = "Would you like to provide a name, description, and more now?"
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
		icon, _ := ui.PromptDefault("Icon URL or local path (overrides APK icon)", cfg.Icon)
		cfg.Icon = icon

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

	// Resolve pubkey from SIGN_WITH and store in config for relay auto-whitelisting.
	// For nsec/npub this is synchronous; for bunker/browser we try a live connection.
	// If resolution fails (e.g. bunker unreachable), prompt the user for their npub.
	// Skip if the app already exists on the relay (pubkey already recorded there).
	appAlreadyExists := false
	if packageID != "" && opts.CheckAppExists != nil {
		spinner := ui.NewSpinner("Checking if app already exists on relay...")
		spinner.Start()
		appAlreadyExists = opts.CheckAppExists(ui.GetContext(), packageID)
		spinner.Stop()
		fmt.Println()
	}

	if !appAlreadyExists {
		if signWith := GetSignWith(); signWith != "" {
			npub := resolveOrPromptPubkey(signWith, opts.ResolvePubkey)
			if npub != "" {
				cfg.Pubkey = npub
			}
		}
	}

	// Check if interrupted before saving
	if ui.IsInterrupted() {
		return nil, ui.ErrInterrupted
	}

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

	// Always write zapstore.yaml so the relay can verify pubkey ownership
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

	// Return sentinel error so caller knows not to auto-run
	return nil, ErrWizardComplete
}

// resolveOrPromptPubkey tries to resolve the npub from signWith.
// For nsec/npub it resolves synchronously. For bunker/browser it calls resolver
// (if provided) with a spinner. If resolution fails or resolver is nil, it
// informs the user and prompts them to enter their npub manually.
func resolveOrPromptPubkey(signWith string, resolver PubkeyResolver) string {
	// Fast path: nsec or npub — no network needed
	if npub := ResolvePubkeyFromSignWith(signWith); npub != "" {
		return npub
	}

	// Async path: bunker or browser
	if resolver != nil {
		spinnerMsg := "Connecting to bunker to resolve your public key..."
		if strings.HasPrefix(signWith, "browser") {
			spinnerMsg = "Connecting to browser extension to resolve your public key..."
		}
		spinner := ui.NewSpinner(spinnerMsg)
		spinner.Start()
		ctx, cancel := context.WithTimeout(ui.GetContext(), 15*time.Second)
		defer cancel()
		npub, err := resolver(ctx, signWith)
		if err == nil && npub != "" {
			spinner.StopWithSuccess(fmt.Sprintf("Resolved pubkey: %s", npub))
			fmt.Println()
			return npub
		}
		spinner.Stop()
		if err != nil {
			fmt.Printf("%s Could not resolve pubkey automatically: %v\n", ui.Warning("⚠"), err)
		}
	} else {
		fmt.Printf("%s Could not resolve pubkey automatically (bunker/browser requires a live connection)\n", ui.Warning("⚠"))
	}

	// Fallback: prompt the user
	fmt.Println(ui.Dim("Your npub is needed so the relay can whitelist your key."))
	for {
		npub, err := ui.Prompt("Enter your npub: ")
		if err != nil {
			return ""
		}
		npub = strings.TrimSpace(npub)
		if npub == "" {
			fmt.Printf("%s npub is required to write pubkey to config\n", ui.Warning("⚠"))
			continue
		}
		if !strings.HasPrefix(npub, "npub1") {
			fmt.Printf("%s Must start with npub1\n", ui.Warning("⚠"))
			continue
		}
		return npub
	}
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
	} else if cfg.ReleaseSource != nil && cfg.ReleaseSource.IsLocal() {
		parts = append(parts, cfg.ReleaseSource.LocalPath)
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

// GetEnv returns the value of an environment variable, checking both
// the process environment and .env file (environment takes precedence).
func GetEnv(name string) string {
	// Check environment variable first
	if value := os.Getenv(name); value != "" {
		return value
	}

	// Check .env file
	data, err := os.ReadFile(".env")
	if err != nil {
		return ""
	}

	prefix := name + "="
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}

	return ""
}

// GetSignWith returns SIGN_WITH from environment or .env file.
func GetSignWith() string {
	value := GetEnv("SIGN_WITH")
	if value != "" {
		// Check source for warning
		if os.Getenv("SIGN_WITH") != "" {
			warnIfNsecInEnv(value, "environment variable")
		} else {
			warnIfNsecInEnv(value, ".env file")
		}
	}
	return value
}

// GetKeystorePassword returns KEYSTORE_PASSWORD from environment or .env file.
func GetKeystorePassword() string {
	return GetEnv("KEYSTORE_PASSWORD")
}

// warnIfNsecInEnv prints a security warning if an nsec is stored in an insecure location.
func warnIfNsecInEnv(value, source string) {
	if strings.HasPrefix(value, "nsec1") {
		fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: Private key (nsec) found in %s\n", source)
		fmt.Fprintf(os.Stderr, "   This is insecure. Consider using:\n")
		fmt.Fprintf(os.Stderr, "   - A bunker:// URL for remote signing\n")
		fmt.Fprintf(os.Stderr, "   - Browser extension (NIP-07)\n")
		fmt.Fprintf(os.Stderr, "   - Environment variable set per-session (not persisted)\n")
		if source == ".env file" && !isInGitignore(".env") {
			fmt.Fprintf(os.Stderr, "   ⚠️  .env is NOT in .gitignore - risk of committing secrets!\n")
		}
		fmt.Fprintln(os.Stderr)
	}
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
		// Security: Do not offer to save nsec to .env - it's too risky
		ui.PrintInfo("Set SIGN_WITH environment variable for future runs (do not store in files)")
	case 1:
		signWith, err = ui.Prompt("Enter your npub: ")
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(signWith, "npub1") {
			return "", fmt.Errorf("invalid npub format")
		}
		// Offer to save non-sensitive options to .env
		if err := offerSaveToEnv(signWith); err != nil {
			return "", err
		}
	case 2:
		signWith, err = ui.Prompt("Enter bunker URL: ")
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(signWith, "bunker://") {
			return "", fmt.Errorf("invalid bunker URL format")
		}
		// Offer to save bunker URL to .env (contains no secrets, just connection info)
		if err := offerSaveToEnv(signWith); err != nil {
			return "", err
		}
	case 3:
		signWith = "browser"
		// Offer to save browser option to .env
		if err := offerSaveToEnv(signWith); err != nil {
			return "", err
		}
	}

	return signWith, nil
}

// offerSaveToEnv offers to save non-sensitive SIGN_WITH values to .env.
func offerSaveToEnv(signWith string) error {
	saveEnv, err := ui.Confirm("Save to .env for future runs?", true)
	if err != nil {
		return err
	}

	if saveEnv {
		// Warn about .gitignore
		if !isInGitignore(".env") {
			ui.PrintWarning("Consider adding .env to your .gitignore file")
		}

		envContent := fmt.Sprintf("SIGN_WITH=%s\n", signWith)
		if err := appendToEnvFile(envContent); err != nil {
			ui.PrintWarning(fmt.Sprintf("Could not save to .env: %v", err))
		} else {
			ui.PrintSuccess("Saved to .env")
		}
	}
	return nil
}

// isInGitignore checks if a file pattern is listed in .gitignore.
func isInGitignore(pattern string) bool {
	data, err := os.ReadFile(".gitignore")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == pattern || line == "/"+pattern {
			return true
		}
	}
	return false
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
			// Extract filename from URL if name is empty or doesn't look like a filename
			if name == "" || !strings.Contains(name, ".") {
				// Parse URL to get path without query parameters
				assetURL := link.URL
				if idx := strings.Index(assetURL, "?"); idx >= 0 {
					assetURL = assetURL[:idx]
				}
				parts := strings.Split(assetURL, "/")
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
