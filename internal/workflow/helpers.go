package workflow

import (
	"encoding/json"
	"fmt"
	"os"
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
			"Preview events (JSON)",
			"Publish now",
			"Exit without publishing",
		}

		idx, err := ui.SelectOption("Choose an option:", options, 1)
		if err != nil {
			return false, err
		}

		switch idx {
		case 0:
			previewEventsJSON(events)
		case 1:
			return true, nil
		case 2:
			return false, nil
		}
	}
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

// OutputEventsToStdout outputs events as newline-delimited JSON to stdout.
// This format is suitable for piping to tools like `nak event`.
func OutputEventsToStdout(events *nostr.EventSet) {
	// Output each event as a single line of JSON
	outputEventLine(events.AppMetadata)
	outputEventLine(events.Release)
	for _, asset := range events.SoftwareAssets {
		outputEventLine(asset)
	}
}

// outputEventLine outputs a single event as JSON on one line to stdout.
func outputEventLine(event any) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Println(string(data))
}

// OutputUploadManifest outputs the upload manifest to stderr.
func OutputUploadManifest(entries []UploadManifestEntry, blossomServer string) {
	if len(entries) == 0 {
		return
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "Make sure to upload these files to %s before publishing events:\n", blossomServer)
	fmt.Fprintln(os.Stderr)

	for _, e := range entries {
		fmt.Fprintf(os.Stderr, "%s:\n", e.Description)
		fmt.Fprintf(os.Stderr, "  Path:   %s\n", e.FilePath)
		fmt.Fprintf(os.Stderr, "  SHA256: %s\n", e.SHA256)
		fmt.Fprintf(os.Stderr, "  URL:    %s\n", e.BlossomURL)
		fmt.Fprintln(os.Stderr)
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

