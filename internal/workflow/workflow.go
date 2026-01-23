// Package workflow orchestrates the APK publishing workflow.
package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/blossom"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/picker"
	"github.com/zapstore/zsp/internal/source"
	"github.com/zapstore/zsp/internal/ui"
)

// Publisher orchestrates the APK publishing workflow.
type Publisher struct {
	opts      *cli.Options
	cfg       *config.Config
	src       source.Source
	publisher *nostr.Publisher
	signer    nostr.Signer

	// Computed during workflow
	release           *source.Release
	selectedAsset     *source.Asset
	apkPath           string
	apkInfo           *apk.APKInfo
	iconURL           string
	imageURLs         []string
	releaseNotes      string
	preDownloaded     *PreDownloadedImages
	events      *nostr.EventSet
	blossomURL  string
	browserPort int
}

// NewPublisher creates a new publish workflow.
func NewPublisher(opts *cli.Options, cfg *config.Config) (*Publisher, error) {
	// Create source with base directory for relative paths
	src, err := source.NewWithOptions(cfg, source.Options{
		BaseDir:   cfg.BaseDir,
		SkipCache: opts.Publish.OverwriteRelease,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create source: %w", err)
	}

	// Get Blossom server URL
	blossomURL := os.Getenv("BLOSSOM_URL")
	if blossomURL == "" {
		blossomURL = blossom.DefaultServer
	}

	// Create relay publisher
	relaysEnv := os.Getenv("RELAY_URLS")
	publisher := nostr.NewPublisherFromEnv(relaysEnv)

	return &Publisher{
		opts:       opts,
		cfg:        cfg,
		src:        src,
		publisher:  publisher,
		blossomURL: blossomURL,
	}, nil
}

// Execute runs the complete publish workflow.
func (p *Publisher) Execute(ctx context.Context) error {
	// Determine total steps based on mode
	totalSteps := 4
	if p.opts.Publish.DryRun {
		totalSteps = 2
	}

	var steps *ui.StepTracker
	if p.opts.Publish.ShouldShowSpinners() {
		steps = ui.NewStepTracker(totalSteps)
	}

	// Step 1: Fetch assets
	if steps != nil {
		steps.StartStep("Fetch Assets")
	}
	if err := p.fetchAssets(ctx); err != nil {
		return err
	}

	// Step 2: Gather metadata
	if steps != nil {
		steps.StartStep("Gather Metadata")
	}
	if err := p.gatherMetadata(ctx); err != nil {
		return err
	}

	// Show preview if requested
	if err := p.handlePreview(ctx); err != nil {
		return err
	}

	// Step 3: Sign & Upload (skip in dry run)
	if steps != nil && !p.opts.Publish.DryRun {
		steps.StartStep("Sign & Upload")
	}
	if err := p.signAndUpload(ctx); err != nil {
		return err
	}

	// Handle dry run output
	if p.isDryRun() {
		return p.outputDryRun()
	}

	// Handle npub signer
	if p.signer != nil && p.signer.Type() == nostr.SignerNpub {
		return p.outputNpubEvents()
	}

	// Hash confirmation
	if !p.opts.Publish.Yes {
		isClosedSource := p.cfg.Repository == ""
		confirmed, err := confirmHash(p.apkInfo.SHA256, isClosedSource)
		if err != nil {
			return fmt.Errorf("hash confirmation failed: %w", err)
		}
		if !confirmed {
			fmt.Println("  Aborted. No events were published.")
			p.clearCache()
			return nil
		}
	}

	// Step 4: Publish to relays
	if steps != nil {
		steps.StartStep("Publish")
	}
	return p.publishToRelays(ctx)
}

// fetchAssets fetches and selects the APK to publish.
func (p *Publisher) fetchAssets(ctx context.Context) error {
	if p.opts.Global.Verbose {
		fmt.Printf("Source type: %s\n", p.src.Type())
	}

	// Fetch release
	release, err := p.fetchRelease(ctx)
	if err != nil {
		return err
	}
	p.release = release

	// Select APK
	asset, err := p.selectAPK(ctx)
	if err != nil {
		return err
	}
	p.selectedAsset = asset

	// Download and parse APK
	return p.downloadAndParseAPK(ctx)
}

// fetchRelease fetches the latest release with spinner feedback.
func (p *Publisher) fetchRelease(ctx context.Context) (*source.Release, error) {
	release, err := WithSpinner(p.opts, "Fetching release info...", func() (*source.Release, error) {
		return p.src.FetchLatestRelease(ctx)
	})

	if err == source.ErrNotModified {
		if p.opts.Publish.ShouldShowSpinners() {
			ui.PrintSuccess("Release unchanged, nothing to do")
			fmt.Println("  Release has not changed since last publish. Use --overwrite-release to publish anyway.")
		}
		return nil, ErrNothingToDo
	}

	if err != nil {
		return nil, fmt.Errorf("failed to fetch release: %w", err)
	}

	if p.opts.Publish.ShouldShowSpinners() {
		if release.Version != "" {
			ui.PrintSuccess(fmt.Sprintf("Found release %s with %d assets", release.Version, len(release.Assets)))
		} else {
			ui.PrintSuccess(fmt.Sprintf("Found %d assets (version from APK)", len(release.Assets)))
		}
	}

	return release, nil
}

// selectAPK filters and selects the best APK from the release.
func (p *Publisher) selectAPK(ctx context.Context) (*source.Asset, error) {
	// Filter to APKs only
	apkAssets := picker.FilterAPKs(p.release.Assets)
	if len(apkAssets) == 0 {
		if p.opts.Global.Verbose {
			fmt.Printf("Release: %s\n", p.release.Version)
			if p.release.TagName != "" && p.release.TagName != p.release.Version {
				fmt.Printf("Tag: %s\n", p.release.TagName)
			}
			if p.release.URL != "" {
				fmt.Printf("URL: %s\n", p.release.URL)
			}
			if len(p.release.Assets) == 0 {
				fmt.Println("Assets: (none)")
			} else {
				fmt.Printf("Assets (%d):\n", len(p.release.Assets))
				for _, asset := range p.release.Assets {
					fmt.Printf("  - %s\n", asset.Name)
				}
			}
		}
		return nil, fmt.Errorf("no APK files found in release")
	}

	// Apply match filter if specified
	if p.cfg.Match != "" {
		var err error
		apkAssets, err = picker.FilterByMatch(apkAssets, p.cfg.Match)
		if err != nil {
			return nil, err
		}
		if len(apkAssets) == 0 {
			return nil, fmt.Errorf("no APK files match pattern: %s", p.cfg.Match)
		}
	}

	// Single APK - use it
	if len(apkAssets) == 1 {
		if p.opts.Publish.ShouldShowSpinners() {
			ui.PrintSuccess(fmt.Sprintf("Selected %s", apkAssets[0].Name))
		}
		return apkAssets[0], nil
	}

	// Multiple APKs - rank and select
	ranked := picker.DefaultModel.RankAssets(apkAssets)

	if p.opts.Global.Verbose {
		fmt.Println("  Ranked APKs:")
		for i, sa := range ranked {
			fmt.Printf("    %d. %s (score: %.2f)\n", i+1, sa.Asset.Name, sa.Score)
		}
	}

	// Interactive selection if not quiet mode
	if p.opts.Publish.IsInteractive() && len(ranked) > 1 {
		return selectAPKInteractive(ranked)
	}

	// Auto-select best match
	if p.opts.Publish.ShouldShowSpinners() {
		ui.PrintSuccess(fmt.Sprintf("Selected %s (best match)", ranked[0].Asset.Name))
	}
	return ranked[0].Asset, nil
}

// downloadAndParseAPK downloads (if needed) and parses the selected APK.
func (p *Publisher) downloadAndParseAPK(ctx context.Context) error {
	var err error

	// Get APK path (download if needed)
	p.apkPath, err = p.getAPKPath(ctx)
	if err != nil {
		return err
	}

	// Parse APK
	p.apkInfo, err = WithSpinner(p.opts, "Parsing APK...", func() (*apk.APKInfo, error) {
		return apk.Parse(p.apkPath)
	})
	if err != nil {
		return fmt.Errorf("failed to parse APK: %w", err)
	}

	// Verify arm64 support
	if !p.apkInfo.IsArm64() {
		return fmt.Errorf("APK does not support arm64-v8a architecture (found: %v)", p.apkInfo.Architectures)
	}

	if p.opts.Publish.ShouldShowSpinners() {
		ui.PrintSuccess("Parsed and verified APK")
	}

	// Backfill version from APK if not known from release
	if p.release.Version == "" {
		p.release.Version = p.apkInfo.VersionName
	}

	// Display APK summary
	if p.opts.Publish.ShouldShowSpinners() {
		ui.PrintSectionHeader("APK Summary")
		ui.PrintKeyValue("Name", p.apkInfo.Label)
		ui.PrintKeyValue("App ID", p.apkInfo.PackageID)
		ui.PrintKeyValue("Version", fmt.Sprintf("%s (%d)", p.apkInfo.VersionName, p.apkInfo.VersionCode))
		ui.PrintKeyValue("Certificate hash", p.apkInfo.CertFingerprint)
		ui.PrintKeyValue("Size", fmt.Sprintf("%.2f MB", float64(p.apkInfo.FileSize)/(1024*1024)))
	}

	// Check if asset already exists on relays
	return p.checkExistingAsset(ctx)
}

// getAPKPath returns the local path to the APK, downloading if necessary.
func (p *Publisher) getAPKPath(ctx context.Context) (string, error) {
	if p.selectedAsset.LocalPath != "" {
		if p.opts.Publish.ShouldShowSpinners() {
			ui.PrintSuccess("Using local APK file")
		}
		return p.selectedAsset.LocalPath, nil
	}

	// Check download cache
	if cachedPath := source.GetCachedDownload(p.selectedAsset.URL, p.selectedAsset.Name); cachedPath != "" {
		p.selectedAsset.LocalPath = cachedPath
		if p.opts.Publish.ShouldShowSpinners() {
			ui.PrintSuccess("Using cached APK")
		}
		return cachedPath, nil
	}

	// Download
	if p.opts.Global.Verbose {
		fmt.Printf("  Download URL: %s\n", p.selectedAsset.URL)
	}

	var tracker *ui.DownloadTracker
	var progressCallback source.DownloadProgress
	if p.opts.Publish.ShouldShowSpinners() {
		tracker = ui.NewDownloadTracker(fmt.Sprintf("Downloading %s", p.selectedAsset.Name), p.selectedAsset.Size)
		progressCallback = tracker.Callback()
	}

	apkPath, err := p.src.Download(ctx, p.selectedAsset, "", progressCallback)
	if tracker != nil {
		tracker.Done()
	}
	if err != nil {
		return "", fmt.Errorf("failed to download APK: %w", err)
	}

	return apkPath, nil
}

// checkExistingAsset checks if the release already exists on relays.
func (p *Publisher) checkExistingAsset(ctx context.Context) error {
	if p.opts.Publish.OverwriteRelease || p.opts.Publish.DryRun {
		return nil
	}

	existingAsset, err := p.publisher.CheckExistingAsset(ctx, p.apkInfo.PackageID, p.apkInfo.VersionName)
	if err != nil {
		if p.opts.Global.Verbose {
			fmt.Printf("  Could not check relays: %v\n", err)
		}
		return nil
	}

	if existingAsset != nil {
		if p.opts.Publish.ShouldShowSpinners() {
			ui.PrintWarning(fmt.Sprintf("Asset %s@%s already exists on %s",
				p.apkInfo.PackageID, p.apkInfo.VersionName, existingAsset.RelayURL))
			fmt.Println("  Use --overwrite-release to publish anyway.")
		}
		return ErrNothingToDo
	}

	return nil
}

// gatherMetadata fetches metadata from external sources.
func (p *Publisher) gatherMetadata(ctx context.Context) error {
	// Fetch metadata from external sources (default for new releases)
	// Use --skip-metadata to opt out (useful for apps with frequent releases)
	if !p.opts.Publish.SkipMetadata {
		if err := p.fetchExternalMetadata(ctx); err != nil {
			return err
		}
	} else if p.opts.Publish.ShouldShowSpinners() {
		ui.PrintInfo("Skipping metadata fetch (--skip-metadata)")
	}

	// Determine release notes
	p.releaseNotes = p.release.Changelog
	if p.cfg.ReleaseNotes != "" {
		var err error
		p.releaseNotes, err = source.FetchReleaseNotes(ctx, p.cfg.ReleaseNotes, p.apkInfo.VersionName, p.cfg.BaseDir)
		if err != nil {
			return fmt.Errorf("failed to fetch release notes: %w", err)
		}
	}

	// Pre-download remote images
	return p.preDownloadImages(ctx)
}

// fetchExternalMetadata fetches metadata from configured sources.
func (p *Publisher) fetchExternalMetadata(ctx context.Context) error {
	metadataSources := p.opts.Publish.Metadata
	if len(metadataSources) == 0 {
		metadataSources = source.DefaultMetadataSources(p.cfg)
	}

	if len(metadataSources) == 0 {
		if p.opts.Publish.ShouldShowSpinners() {
			ui.PrintInfo("No external metadata sources configured")
		}
		return nil
	}

	fetcher := source.NewMetadataFetcherWithPackageID(p.cfg, p.apkInfo.PackageID)
	fetcher.APKName = p.apkInfo.Label

	err := WithSpinnerMsg(p.opts, "Fetching metadata from external sources...", func() error {
		return fetcher.FetchMetadata(ctx, metadataSources)
	}, func(err error) string {
		if err != nil {
			return "Metadata fetch failed (continuing)"
		}
		return fmt.Sprintf("Fetched metadata from %s", strings.Join(metadataSources, ", "))
	})

	if err != nil && p.opts.Global.Verbose {
		fmt.Printf("    %v\n", err)
	}

	if p.opts.Global.Verbose && err == nil {
		fmt.Printf("    name=%q, description=%d chars, tags=%v\n",
			p.cfg.Name, len(p.cfg.Description), p.cfg.Tags)
	}

	return nil // Metadata errors are non-fatal
}

// preDownloadImages downloads remote icons and screenshots.
func (p *Publisher) preDownloadImages(ctx context.Context) error {
	if p.cfg.Icon == "" || !isRemoteURL(p.cfg.Icon) {
		if !hasRemoteImages(p.cfg.Images) {
			return nil
		}
	}

	var err error
	p.preDownloaded, err = PreDownloadImages(ctx, p.cfg, p.opts)
	if err != nil {
		return fmt.Errorf("failed to download images: %w", err)
	}

	return nil
}

// handlePreview shows the browser preview if requested.
func (p *Publisher) handlePreview(ctx context.Context) error {
	// Skip preview if metadata fetch was skipped (may have incomplete data)
	if p.opts.Publish.Quiet || p.opts.Publish.Yes || p.opts.Publish.SkipPreview || p.opts.Publish.SkipMetadata {
		return nil
	}

	// Skip preview prompt if no graphical display is available
	if !ui.HasDisplay() {
		return nil
	}

	defaultPort := nostr.DefaultPreviewPort
	if p.opts.Publish.Port != 0 {
		defaultPort = p.opts.Publish.Port
	}

	fmt.Println()
	confirmed, port, err := ui.ConfirmWithPort("Preview release in browser?", defaultPort)
	if err != nil {
		return fmt.Errorf("prompt failed: %w", err)
	}

	if !confirmed {
		return nil
	}

	p.browserPort = port
	if p.browserPort == 0 {
		p.browserPort = nostr.DefaultPreviewPort
	}

	return p.showPreview(ctx)
}

// showPreview displays the browser preview.
func (p *Publisher) showPreview(ctx context.Context) error {
	previewData := nostr.BuildPreviewDataFromAPK(p.apkInfo, p.cfg, p.releaseNotes, p.blossomURL, p.publisher.RelayURLs())

	// Override icon with pre-downloaded icon if available
	if p.preDownloaded != nil && p.preDownloaded.Icon != nil {
		previewData.IconData = p.preDownloaded.Icon.Data
	}

	// Add pre-downloaded screenshots
	if p.preDownloaded != nil && len(p.preDownloaded.Images) > 0 {
		for _, img := range p.preDownloaded.Images {
			previewData.ImageData = append(previewData.ImageData, nostr.PreviewImageData{
				Data:     img.Data,
				MimeType: img.MimeType,
			})
		}
	}

	previewServer := nostr.NewPreviewServer(previewData, p.releaseNotes, "", p.browserPort)
	url, err := previewServer.Start()
	if err != nil {
		return fmt.Errorf("failed to start preview server: %w", err)
	}

	fmt.Printf("Preview server started at %s\n", url)
	fmt.Println("Press Enter to continue, or Ctrl+C to cancel...")

	// Wait for Enter with context support
	err = cli.WaitForEnterWithContext(ctx)
	if err != nil {
		previewServer.Close()
		return err
	}

	previewServer.ConfirmFromCLI()
	previewServer.Close()

	return nil
}

// signAndUpload handles signer creation and file uploads.
func (p *Publisher) signAndUpload(ctx context.Context) error {
	// Create signer
	if err := p.createSigner(ctx); err != nil {
		return err
	}

	// Determine URLs and build events
	if p.isDryRun() || p.signer.Type() == nostr.SignerNpub {
		return p.buildEventsWithoutUpload(ctx)
	}

	return p.uploadAndBuildEvents(ctx)
}

// createSigner creates the appropriate signer based on configuration.
func (p *Publisher) createSigner(ctx context.Context) error {
	var signWith string

	if p.opts.Publish.DryRun {
		signWith = nostr.TestNsec
	} else {
		signWith = config.GetSignWith()
		if signWith == "" {
			if p.opts.Publish.Quiet {
				return fmt.Errorf("SIGN_WITH environment variable is required")
			}
			ui.PrintSectionHeader("Signing Setup")
			var err error
			signWith, err = config.PromptSignWith()
			if err != nil {
				return fmt.Errorf("signing setup failed: %w", err)
			}
		}
	}

	// Determine port for browser signer
	signerPort := p.browserPort
	if signWith == "browser" && p.browserPort == 0 && p.opts.Publish.IsInteractive() {
		port, err := ui.ConfirmWithPortYesOnly("Browser signing port?", nostr.DefaultNIP07Port)
		if err != nil {
			return fmt.Errorf("prompt failed: %w", err)
		}
		signerPort = port
	}

	var err error
	p.signer, err = nostr.NewSignerWithOptions(ctx, signWith, nostr.SignerOptions{
		Port: signerPort,
	})
	if err != nil {
		return fmt.Errorf("failed to create signer: %w", err)
	}

	if p.opts.Global.Verbose {
		fmt.Printf("Signer type: %v, pubkey: %s...\n", p.signer.Type(), p.signer.PublicKey()[:16])
	}

	return nil
}

// isDryRun returns true if this is a dry run.
func (p *Publisher) isDryRun() bool {
	return p.opts.Publish.DryRun || (p.signer != nil && p.signWithTestKey())
}

// signWithTestKey returns true if using the test key.
func (p *Publisher) signWithTestKey() bool {
	signWith := config.GetSignWith()
	return signWith == nostr.TestNsec
}

// buildEventsWithoutUpload builds events without uploading files (dry run / npub mode).
func (p *Publisher) buildEventsWithoutUpload(ctx context.Context) error {
	var err error
	p.iconURL, p.imageURLs, err = ResolveURLsWithoutUpload(ctx, p.cfg, p.apkInfo, p.blossomURL, p.preDownloaded, p.opts)
	if err != nil {
		return err
	}

	p.events = nostr.BuildEventSet(nostr.BuildEventSetParams{
		APKInfo:          p.apkInfo,
		Config:           p.cfg,
		Pubkey:           p.signer.PublicKey(),
		OriginalURL:      p.getOriginalURL(),
		BlossomServer:    p.blossomURL,
		IconURL:          p.iconURL,
		ImageURLs:        p.imageURLs,
		Changelog:        p.releaseNotes,
		Variant:          p.matchVariant(),
		Commit:           p.opts.Publish.Commit,
		Channel:          p.opts.Publish.Channel,
		ReleaseURL:       p.getReleaseURL(),
		LegacyFormat:     p.opts.Publish.Legacy,
		ReleaseTimestamp: p.getReleaseTimestamp(),
	})

	relayHint := p.getRelayHint()
	return nostr.SignEventSet(ctx, p.signer, p.events, relayHint)
}

// uploadAndBuildEvents uploads files and builds events.
func (p *Publisher) uploadAndBuildEvents(ctx context.Context) error {
	client := blossom.NewClient(p.blossomURL)
	relayHint := p.getRelayHint()

	// Check if we should use batch signing
	batchSigner, isBatchSigner := p.signer.(nostr.BatchSigner)

	if isBatchSigner {
		var err error
		p.events, err = UploadAndSignWithBatch(ctx, UploadParams{
			Cfg:           p.cfg,
			APKInfo:       p.apkInfo,
			APKPath:       p.apkPath,
			Release:       p.release,
			Client:        client,
			OriginalURL:   p.getOriginalURL(),
			BlossomServer: p.blossomURL,
			BatchSigner:   batchSigner,
			Pubkey:        p.signer.PublicKey(),
			RelayHint:     relayHint,
			PreDownloaded: p.preDownloaded,
			Variant:       p.matchVariant(),
			Commit:        p.opts.Publish.Commit,
			Channel:       p.opts.Publish.Channel,
			Opts:          p.opts,
			Legacy:        p.opts.Publish.Legacy,
		})
		return err
	}

	// Regular signing mode
	var err error
	p.iconURL, p.imageURLs, err = UploadWithIndividualSigning(ctx, UploadParams{
		Cfg:           p.cfg,
		APKInfo:       p.apkInfo,
		APKPath:       p.apkPath,
		Client:        client,
		Signer:        p.signer,
		PreDownloaded: p.preDownloaded,
		Opts:          p.opts,
	})
	if err != nil {
		return err
	}

	p.events = nostr.BuildEventSet(nostr.BuildEventSetParams{
		APKInfo:          p.apkInfo,
		Config:           p.cfg,
		Pubkey:           p.signer.PublicKey(),
		OriginalURL:      p.getOriginalURL(),
		BlossomServer:    p.blossomURL,
		IconURL:          p.iconURL,
		ImageURLs:        p.imageURLs,
		Changelog:        p.releaseNotes,
		Variant:          p.matchVariant(),
		Commit:           p.opts.Publish.Commit,
		Channel:          p.opts.Publish.Channel,
		ReleaseURL:       p.getReleaseURL(),
		LegacyFormat:     p.opts.Publish.Legacy,
		ReleaseTimestamp: p.getReleaseTimestamp(),
	})

	return nostr.SignEventSet(ctx, p.signer, p.events, relayHint)
}

// getRelayHint returns the first relay URL for event references.
func (p *Publisher) getRelayHint() string {
	relayHint := nostr.DefaultRelay
	if relaysEnv := os.Getenv("RELAY_URLS"); relaysEnv != "" {
		parts := strings.Split(relaysEnv, ",")
		if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
			relayHint = strings.TrimSpace(parts[0])
		}
	}
	return relayHint
}

// getReleaseURL returns the release page URL (for legacy format).
func (p *Publisher) getReleaseURL() string {
	if p.release != nil {
		return p.release.URL
	}
	return ""
}

// getReleaseTimestamp returns the release creation/publish timestamp.
// Returns zero time if unknown (current time will be used for events).
func (p *Publisher) getReleaseTimestamp() time.Time {
	if p.release != nil {
		return p.release.CreatedAt
	}
	return time.Time{}
}

// getOriginalURL returns the original download URL for the asset.
// Returns empty string if the asset's URL should be excluded from the event
// (e.g., versionless web sources where only Blossom URL should be used).
func (p *Publisher) getOriginalURL() string {
	if p.selectedAsset == nil {
		return ""
	}
	if p.selectedAsset.ExcludeURL {
		return ""
	}
	return p.selectedAsset.URL
}

// matchVariant returns the variant name if the APK matches a variant pattern.
func (p *Publisher) matchVariant() string {
	if len(p.cfg.Variants) == 0 {
		return ""
	}

	filename := filepath.Base(p.apkInfo.FilePath)
	for name, pattern := range p.cfg.Variants {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(filename) {
			return name
		}
	}
	return ""
}

// outputDryRun outputs events in dry run mode.
func (p *Publisher) outputDryRun() error {
	if p.opts.Publish.ShouldShowSpinners() {
		fmt.Println()
		fmt.Println(ui.Dim("─────────────────────────────────────────────────────────────────────"))
		fmt.Println(ui.Warning("You are in dry run mode. These events are signed with a dummy key."))
		fmt.Println(ui.Dim("Use SIGN_WITH to generate real events."))
		fmt.Println(ui.Dim("─────────────────────────────────────────────────────────────────────"))
	}

	if p.opts.Publish.IsInteractive() {
		confirmed, err := ui.Confirm("View events?", true)
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			return nil
		}
	}

	OutputEvents(p.events)
	return nil
}

// outputNpubEvents outputs unsigned events for npub signer.
func (p *Publisher) outputNpubEvents() error {
	if p.opts.Publish.ShouldShowSpinners() {
		fmt.Println()
		ui.PrintInfo("npub mode - outputting unsigned events for external signing")
	}
	OutputEvents(p.events)
	if p.opts.Publish.ShouldShowSpinners() {
		ui.PrintCompletionSummary(true, "Unsigned events generated - sign externally before publishing")
	}
	return nil
}

// publishToRelays publishes events to configured relays.
func (p *Publisher) publishToRelays(ctx context.Context) error {
	// Confirm before publishing
	if !p.opts.Publish.Yes {
		confirmed, err := confirmPublish(p.events, p.publisher.RelayURLs())
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			fmt.Println("  Aborted. No events were published.")
			p.clearCache()
			return nil
		}
	}

	// Publish with spinner
	var publishSpinner *ui.Spinner
	if p.opts.Publish.ShouldShowSpinners() {
		publishSpinner = ui.NewSpinner(fmt.Sprintf("Publishing to %d relays...", len(p.publisher.RelayURLs())))
		publishSpinner.Start()
	}

	results, err := p.publisher.PublishEventSet(ctx, p.events)
	if err != nil {
		if publishSpinner != nil {
			publishSpinner.StopWithError("Failed to publish")
		}
		return fmt.Errorf("failed to publish: %w", err)
	}

	// Report results
	allSuccess := true
	hasDuplicates := false
	var messages []string
	for eventType, eventResults := range results {
		for _, r := range eventResults {
			if r.Success {
				if r.IsDuplicate {
					hasDuplicates = true
					messages = append(messages, fmt.Sprintf("    %s -> %s: already exists", eventType, r.RelayURL))
				} else if p.opts.Global.Verbose {
					messages = append(messages, fmt.Sprintf("    %s -> %s: OK", eventType, r.RelayURL))
				}
			} else {
				messages = append(messages, fmt.Sprintf("    %s -> %s: FAILED (%v)", eventType, r.RelayURL, r.Error))
				allSuccess = false
			}
		}
	}

	if publishSpinner != nil {
		if allSuccess {
			if hasDuplicates {
				publishSpinner.StopWithSuccess("Published (some events already existed)")
			} else {
				publishSpinner.StopWithSuccess("Published successfully")
			}
		} else {
			publishSpinner.StopWithWarning("Published with some failures")
		}
	}

	for _, msg := range messages {
		fmt.Println(msg)
	}

	// Commit or clear cache
	if allSuccess {
		p.commitCache()
		p.deleteCachedAPK()
	} else {
		p.clearCache()
		if p.opts.Global.Verbose {
			fmt.Println("  Cleared release cache for retry")
		}
	}

	// Print completion summary
	if p.opts.Publish.ShouldShowSpinners() {
		if allSuccess {
			ui.PrintCompletionSummary(true, fmt.Sprintf("Published %s v%s to %s",
				p.apkInfo.PackageID, p.apkInfo.VersionName, strings.Join(p.publisher.RelayURLs(), ", ")))
		} else {
			ui.PrintCompletionSummary(false, "Published with some failures")
		}
	}

	return nil
}

// clearCache clears the source cache.
func (p *Publisher) clearCache() {
	if cacheClearer, ok := p.src.(source.CacheClearer); ok {
		_ = cacheClearer.ClearCache()
	}
}

// commitCache commits the source cache to disk.
func (p *Publisher) commitCache() {
	if cacheCommitter, ok := p.src.(source.CacheCommitter); ok {
		_ = cacheCommitter.CommitCache()
	}
}

// deleteCachedAPK removes the cached APK file after successful publishing.
func (p *Publisher) deleteCachedAPK() {
	if p.selectedAsset == nil || p.selectedAsset.URL == "" {
		return // Local file or no URL, nothing to delete
	}
	_ = source.DeleteCachedDownload(p.selectedAsset.URL, p.selectedAsset.Name)
}

// Close releases resources.
func (p *Publisher) Close() {
	if p.signer != nil {
		p.signer.Close()
	}
}

// ErrNothingToDo indicates no publishing is needed.
var ErrNothingToDo = fmt.Errorf("nothing to do")
