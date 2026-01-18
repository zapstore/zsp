package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/picker"
	"github.com/zapstore/zsp/internal/source"
	"github.com/zapstore/zsp/internal/ui"
)

// WithSpinner executes a function with spinner feedback.
// Returns the result and any error from the function.
func WithSpinner[T any](opts *cli.Options, message string, fn func() (T, error)) (T, error) {
	var zero T
	if !opts.Publish.ShouldShowSpinners() {
		return fn()
	}

	spinner := ui.NewSpinner(message)
	spinner.Start()

	result, err := fn()
	if err != nil {
		spinner.StopWithError(err.Error())
		return zero, err
	}

	spinner.Stop()
	return result, nil
}

// WithSpinnerMsg executes a function with spinner feedback and custom success message.
func WithSpinnerMsg(opts *cli.Options, message string, fn func() error, successMsg func(error) string) error {
	if !opts.Publish.ShouldShowSpinners() {
		return fn()
	}

	spinner := ui.NewSpinner(message)
	spinner.Start()

	err := fn()
	msg := successMsg(err)

	if err != nil {
		spinner.StopWithWarning(msg)
	} else {
		spinner.StopWithSuccess(msg)
	}

	return err
}

// selectAPKInteractive prompts the user to select an APK from a ranked list.
func selectAPKInteractive(ranked []picker.ScoredAsset) (*source.Asset, error) {
	ui.PrintSectionHeader("Select APK")
	fmt.Printf("  %s\n", ui.Dim("Zapstore only supports arm64-v8a, always prefer that architecture."))

	options := make([]string, len(ranked))
	for i, sa := range ranked {
		sizeStr := ""
		if sa.Asset.Size > 0 {
			sizeMB := float64(sa.Asset.Size) / (1024 * 1024)
			sizeStr = fmt.Sprintf(" (%.1f MB)", sizeMB)
		}
		options[i] = fmt.Sprintf("%s%s", sa.Asset.Name, sizeStr)
	}

	idx, err := ui.SelectOption("", options, 0)
	if err != nil {
		return nil, err
	}

	return ranked[idx].Asset, nil
}

// confirmHash asks the user to confirm the file hash they just signed.
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
func confirmPublish(events *nostr.EventSet, relayURLs []string) (bool, error) {
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

// previewEvents displays signed events in a human-readable format.
func previewEvents(events *nostr.EventSet) {
	ui.PrintSectionHeader("Signed Events Preview")

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

// OutputEvents prints events as formatted, colorized JSON.
func OutputEvents(events *nostr.EventSet) {
	fmt.Println()
	fmt.Printf("%s\n", ui.Bold("Kind 32267 (Software Application):"))
	printColorizedJSON(events.AppMetadata)
	fmt.Println()

	fmt.Printf("%s\n", ui.Bold("Kind 30063 (Software Release):"))
	printColorizedJSON(events.Release)
	fmt.Println()

	for i, asset := range events.SoftwareAssets {
		assetLabel := "Kind 3063 (Software Asset):"
		if len(events.SoftwareAssets) > 1 {
			assetLabel = fmt.Sprintf("Kind 3063 (Software Asset %d):", i+1)
		}
		fmt.Printf("%s\n", ui.Bold(assetLabel))
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
