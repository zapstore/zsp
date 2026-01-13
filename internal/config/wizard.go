package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/ui"
	"gopkg.in/yaml.v3"
)

// Logo is the ASCII art logo for Zapstore.
const Logo = `
 _____                _
/ _  / __ _ _ __  ___| |_ ___  _ __ ___
\// / / _` + "`" + ` | '_ \/ __| __/ _ \| '__/ _ \
 / //\ (_| | |_) \__ \ || (_) | | |  __/
/____/\__,_| .__/|___/\__\___/|_|  \___|
           |_|
`

// RunWizard runs the interactive configuration wizard.
// If defaults is non-nil, those values are used as defaults for prompts.
func RunWizard(defaults *Config) (*Config, error) {
	fmt.Print(ui.Title(Logo))
	if defaults != nil {
		fmt.Println(ui.Title("Edit Configuration"))
	} else {
		fmt.Println(ui.Title("Publish Wizard"))
	}
	fmt.Println()

	// Introduction (only show for new configs)
	if defaults == nil {
		fmt.Println(ui.Dim("zsp helps you publish Android apps to Nostr relays used by Zapstore."))
		fmt.Println()
		fmt.Println(ui.Dim("Quick start (if you release on GitHub):"))
		fmt.Println(ui.Dim("  zsp -r github.com/user/repo"))
		fmt.Println(ui.Dim("or with a local APK:"))
		fmt.Println(ui.Dim("  zsp ./app.apk -r github.com/user/repo"))
		fmt.Println()
		fmt.Println(ui.Dim("If that is sufficient, hit Ctrl+C and run it"))
		fmt.Println()
		fmt.Println(ui.Dim("For richer metadata (description, screenshots, etc), this wizard helps you create"))
		fmt.Println(ui.Dim("a zapstore.yaml config specifying sources like Play Store or F-droid to pull from."))
		fmt.Println()
	}

	// Initialize config from defaults or empty
	cfg := &Config{}
	if defaults != nil {
		*cfg = *defaults // Copy defaults
	}

	// Compute default source value for prompt
	defaultSource := ""
	if cfg.Repository != "" {
		defaultSource = cfg.Repository
	} else if cfg.Local != "" {
		defaultSource = cfg.Local
	}

	// Step 1: Get source (with validation loop)
	var sourceType SourceType
	for {
		fmt.Println(ui.Bold("1. What's the source?"))
		source, err := ui.PromptDefault("   Repository URL or local path", defaultSource)
		if err != nil {
			return nil, err
		}

		if source == "" {
			return nil, fmt.Errorf("source is required")
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
			cfg.Repository = source
		}

		fmt.Printf("\n   %s Detected: %s\n", ui.Info("ℹ"), sourceType)

		// Validate repository if GitHub or GitLab
		hasWarning := false
		if sourceType == SourceGitHub || sourceType == SourceGitLab {
			fmt.Printf("   %s Checking for releases...\n", ui.Dim("⋯"))

			var validation *releaseValidation
			if sourceType == SourceGitHub {
				validation = validateGitHubRepo(GetGitHubRepo(cfg.Repository))
			} else {
				validation = validateGitLabRepo(GetGitLabRepo(cfg.Repository))
			}

			if validation.Error != nil {
				fmt.Printf("   %s Could not validate: %v\n", ui.Warning("⚠"), validation.Error)
				hasWarning = true
			} else if !validation.HasReleases {
				fmt.Printf("   %s No releases found\n", ui.Warning("⚠"))
				hasWarning = true
			} else if validation.APKCount == 0 {
				fmt.Printf("   %s Release found but no APK assets\n", ui.Warning("⚠"))
				hasWarning = true
			} else {
				fmt.Printf("   %s Found %d APK(s) in latest release:\n", ui.Success("✓"), validation.APKCount)
				for _, name := range validation.APKNames {
					fmt.Printf("      • %s\n", name)
				}
			}
		}

		// If warning, ask to proceed
		if hasWarning {
			proceed, _ := ui.Confirm("   Proceed anyway?", false)
			if !proceed {
				fmt.Println()
				defaultSource = source // Keep the entered value for retry
				continue               // Loop back to ask for source again
			}
		}

		break // Validation passed or user chose to proceed
	}

	fmt.Println()

	// Step 2: App metadata
	fmt.Println(ui.Bold("2. App metadata (optional, press Enter to skip)"))

	name, _ := ui.PromptDefault("   App name", cfg.Name)
	if name != "" {
		cfg.Name = name
	} else {
		cfg.Name = ""
	}

	description, _ := ui.PromptDefault("   Description", cfg.Description)
	if description != "" {
		cfg.Description = description
	} else {
		cfg.Description = ""
	}

	summary, _ := ui.PromptDefault("   Summary (short tagline)", cfg.Summary)
	if summary != "" {
		cfg.Summary = summary
	} else {
		cfg.Summary = ""
	}

	defaultTags := strings.Join(cfg.Tags, " ")
	tagsStr, _ := ui.PromptDefault("   Tags (space-separated)", defaultTags)
	if tagsStr != "" {
		cfg.Tags = strings.Fields(tagsStr)
	} else {
		cfg.Tags = nil
	}

	fmt.Println()

	// Step 3: Show generated config
	fmt.Println(ui.Bold("3. Generated configuration:"))
	fmt.Println(ui.Dim("   ─────────────────────────"))

	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to generate YAML: %w", err)
	}

	// Indent the YAML output
	lines := strings.Split(string(yamlBytes), "\n")
	for _, line := range lines {
		if line != "" {
			fmt.Println("   " + line)
		}
	}
	fmt.Println(ui.Dim("   ─────────────────────────"))
	fmt.Println()

	// Step 4: Save config
	fmt.Println(ui.Bold("4. Save configuration"))
	save, err := ui.Confirm("   Save to ./zapstore.yaml?", true)
	if err != nil {
		return nil, err
	}

	if save {
		if err := os.WriteFile("zapstore.yaml", yamlBytes, 0644); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
		ui.PrintSuccess("Saved to zapstore.yaml")
	}

	fmt.Println()

	// Check if SIGN_WITH is configured
	if !hasSignWith() {
		fmt.Println(ui.Bold("5. Signing setup"))
		_, err := PromptSignWith()
		if err != nil {
			return nil, err
		}
	}

	fmt.Println()

	return cfg, nil
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
		return signWith, nil
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
