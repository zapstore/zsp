package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zapstore/zsp/internal/artifact"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/picker"
	"github.com/zapstore/zsp/internal/source"
	"github.com/zapstore/zsp/internal/ui"
)

// slugifyRe matches any character that is not a lowercase letter or digit.
var slugifyRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a name into a lowercase identifier suitable for use as
// a Nostr d-tag. It lowercases, replaces runs of non-alphanumeric characters
// with a single hyphen, and trims leading/trailing hyphens.
//
// Examples:
//
//	"My-Tool"       -> "my-tool"
//	"my_tool_v2"    -> "my-tool-v2"
//	"App (beta)"    -> "app-beta"
//	"nostr-relay"   -> "nostr-relay"
func slugify(name string) string {
	s := strings.ToLower(name)
	s = slugifyRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// WithSpinner executes a function with spinner feedback.
// Returns the result and any error from the function.
func WithSpinner[T any](opts *cli.Options, message string, fn func() (T, error)) (T, error) {
	var zero T
	if !opts.Publish.ShouldShowSpinners() {
		return fn()
	}

	spinner := ui.NewStatusSpinner("Working", message)
	spinner.Start()

	result, err := fn()
	if err != nil {
		spinner.Fail("Error", err.Error())
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

	spinner := ui.NewStatusSpinner("Working", message)
	spinner.Start()

	err := fn()
	msg := successMsg(err)

	if err != nil {
		spinner.Warn("Warning", msg)
	} else {
		spinner.Done("Done", msg)
	}

	return err
}

// selectAssetInteractive prompts the user to select an asset from a ranked list.
func selectAssetInteractive(ranked []picker.ScoredAsset) (*source.Asset, error) {
	ui.Status("Summary", "Select Asset")
	fmt.Fprintf(os.Stderr, "  %s\n", ui.Dim("Select the best asset for your target platform."))

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

// selectAssetsInteractive prompts the user to select one or more assets.
// Top-ranked assets are pre-selected.
func selectAssetsInteractive(ranked []picker.ScoredAsset) ([]*source.Asset, error) {
	ui.Status("Summary", "Select Assets")
	fmt.Fprintf(os.Stderr, "  %s\n", ui.Dim("Select assets to publish. Space to toggle, Enter to confirm."))

	options := make([]string, len(ranked))
	for i, sa := range ranked {
		sizeStr := ""
		if sa.Asset.Size > 0 {
			sizeMB := float64(sa.Asset.Size) / (1024 * 1024)
			sizeStr = fmt.Sprintf(" (%.1f MB)", sizeMB)
		}
		options[i] = fmt.Sprintf("%s%s", sa.Asset.Name, sizeStr)
	}

	// Pre-select the top-ranked asset
	preselected := []int{0}

	indices, err := ui.SelectMultipleWithDefaults("", options, preselected)
	if err != nil {
		return nil, err
	}

	if len(indices) == 0 {
		return nil, fmt.Errorf("no assets selected")
	}

	result := make([]*source.Asset, len(indices))
	for i, idx := range indices {
		result[i] = ranked[idx].Asset
	}
	return result, nil
}

// confirmHash asks the user to confirm the file hash they just signed.
func confirmHash(sha256Hash string, isClosedSource bool, isLegacy bool) (bool, error) {
	kindStr := "3063"
	if isLegacy {
		kindStr = "1063"
	}
	
	fmt.Fprintln(os.Stderr)
	ui.WarningStatus("Warning", fmt.Sprintf("You just signed an event attesting to this file hash (kind %s):", kindStr))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  %s\n", ui.Bold(sha256Hash))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  %s\n", ui.Bold("Make sure it matches the file you intend to distribute."))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  To verify, run:")
	fmt.Fprintf(os.Stderr, "    %s\n", ui.Dim("shasum -a 256 <path-to-file>   # macOS"))
	fmt.Fprintf(os.Stderr, "    %s\n", ui.Dim("sha256sum <path-to-file>       # Linux"))
	fmt.Fprintln(os.Stderr)

	if isClosedSource {
		ui.WarningStatus("Warning", "This application has no repository (closed source).")
		fmt.Fprintln(os.Stderr)
	}

	return ui.Confirm("Confirm hash is correct?", false)
}

// confirmHashes asks the user to confirm file hashes for one or more assets.
func confirmHashes(assetInfos []*artifact.AssetInfo, assetPaths []string, isClosedSource bool, isLegacy bool) (bool, error) {
	// Single asset: use original format
	if len(assetInfos) == 1 {
		return confirmHash(assetInfos[0].SHA256, isClosedSource, isLegacy)
	}

	kindStr := "3063"
	if isLegacy {
		kindStr = "1063"
	}

	fmt.Fprintln(os.Stderr)
	ui.WarningStatus("Warning", fmt.Sprintf("You just signed events attesting to these file hashes (kind %s):", kindStr))
	fmt.Fprintln(os.Stderr)
	for i, ai := range assetInfos {
		filename := filepath.Base(assetPaths[i])
		fmt.Fprintf(os.Stderr, "  %s  %s\n", ui.Bold(ai.SHA256), filename)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  %s\n", ui.Bold("Make sure they match the files you intend to distribute."))
	fmt.Fprintln(os.Stderr)

	if isClosedSource {
		ui.WarningStatus("Warning", "This application has no repository (closed source).")
		fmt.Fprintln(os.Stderr)
	}

	return ui.Confirm("Confirm hashes are correct?", false)
}

// confirmPublish shows a pre-publish summary and asks for confirmation.
func confirmPublish(events *nostr.EventSet, relayURLs []string) (bool, error) {
	packageID := ""
	version := ""
	
	// Determine if using legacy format by checking asset kind
	isLegacy := len(events.SoftwareAssets) > 0 && events.SoftwareAssets[0].Kind == 1063
	
	for _, tag := range events.Release.Tags {
		if len(tag) >= 2 {
			if tag[0] == "i" {
				packageID = tag[1]
			}
			if tag[0] == "version" {
				version = tag[1]
			}
			// In legacy format, version is in the "d" tag as "packageID@version"
			if isLegacy && tag[0] == "d" && strings.Contains(tag[1], "@") {
				parts := strings.Split(tag[1], "@")
				if len(parts) == 2 {
					packageID = parts[0]
					version = parts[1]
				}
			}
		}
	}

	assetKind := "3063"
	if isLegacy {
		assetKind = "1063"
	}

	ui.Status("Summary", "Ready to Publish")
	ver := version
	if ver != "" && !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}
	ui.Status("App", fmt.Sprintf("%s %s", packageID, ver))
	assetStr := fmt.Sprintf("Kind %s (Asset)", assetKind)
	if len(events.SoftwareAssets) > 1 {
		assetStr = fmt.Sprintf("Kind %s (Assets x%d)", assetKind, len(events.SoftwareAssets))
	}
	ui.Status("Events", fmt.Sprintf("Kind 32267 (App) + Kind 30063 (Release) + %s", assetStr))
	ui.Status("Target", strings.Join(relayURLs, ", "))
	fmt.Fprintln(os.Stderr)

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
	ui.Status("Summary", "Signed Events (JSON)")
	fmt.Fprintln(os.Stderr)

	fmt.Fprintf(os.Stderr, "  %s\n", ui.Bold("Kind 32267 (Software Application):"))
	printColorizedJSON(events.AppMetadata)
	fmt.Fprintln(os.Stderr)

	fmt.Fprintf(os.Stderr, "  %s\n", ui.Bold("Kind 30063 (Software Release):"))
	printColorizedJSON(events.Release)
	fmt.Fprintln(os.Stderr)

	// Determine asset kind based on actual event kind
	assetKindStr := "3063"
	if len(events.SoftwareAssets) > 0 && events.SoftwareAssets[0].Kind == 1063 {
		assetKindStr = "1063"
	}

	for i, asset := range events.SoftwareAssets {
		assetLabel := fmt.Sprintf("Kind %s (Software Asset):", assetKindStr)
		if len(events.SoftwareAssets) > 1 {
			assetLabel = fmt.Sprintf("Kind %s (Software Asset %d):", assetKindStr, i+1)
		}
		fmt.Fprintf(os.Stderr, "  %s\n", ui.Bold(assetLabel))
		printColorizedJSON(asset)
		fmt.Fprintln(os.Stderr)
	}
}

// OutputEvents prints events as formatted, colorized JSON to stderr.
func OutputEvents(events *nostr.EventSet) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "%s\n", ui.Bold("Kind 32267 (Software Application):"))
	printColorizedJSON(events.AppMetadata)
	fmt.Fprintln(os.Stderr)

	fmt.Fprintf(os.Stderr, "%s\n", ui.Bold("Kind 30063 (Software Release):"))
	printColorizedJSON(events.Release)
	fmt.Fprintln(os.Stderr)

	// Determine asset kind based on actual event kind
	assetKindStr := "3063"
	if len(events.SoftwareAssets) > 0 && events.SoftwareAssets[0].Kind == 1063 {
		assetKindStr = "1063"
	}

	for i, asset := range events.SoftwareAssets {
		assetLabel := fmt.Sprintf("Kind %s (Software Asset):", assetKindStr)
		if len(events.SoftwareAssets) > 1 {
			assetLabel = fmt.Sprintf("Kind %s (Software Asset %d):", assetKindStr, i+1)
		}
		fmt.Fprintf(os.Stderr, "%s\n", ui.Bold(assetLabel))
		printColorizedJSON(asset)
		fmt.Fprintln(os.Stderr)
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

// printColorizedJSON prints a value as colorized JSON to stderr.
func printColorizedJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	fmt.Fprintln(os.Stderr, ui.ColorizeJSON(string(data)))
}

