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

	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/zapstore/zsp/internal/artifact"
	"github.com/zapstore/zsp/internal/blossom"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/picker"
	"github.com/zapstore/zsp/internal/source"
	"github.com/zapstore/zsp/internal/ui"
)

// Publisher orchestrates the publishing workflow for APKs and native executables.
type Publisher struct {
	opts      *cli.Options
	cfg       *config.Config
	src       source.Source
	publisher *nostr.Publisher
	signer    nostr.Signer

	// Computed during workflow
	release                  *source.Release
	selectedAssets           []*source.Asset       // Selected assets to publish
	assetPaths               []string              // Local paths to the asset files
	assetInfos               []*artifact.AssetInfo // Parsed metadata per asset
	iconURL                  string
	imageURLs                []string
	releaseNotes             string
	preDownloaded            *PreDownloadedImages
	events                   *nostr.EventSet
	blossomURL               string
	browserPort              int
	existingReleaseTimestamp time.Time // created_at of existing 30063 on relay (for --overwrite-release)
}

// primaryAssetInfo returns the first asset info (used for app-level metadata).
func (p *Publisher) primaryAssetInfo() *artifact.AssetInfo {
	if len(p.assetInfos) > 0 {
		return p.assetInfos[0]
	}
	return nil
}

// NewPublisher creates a new publish workflow.
func NewPublisher(opts *cli.Options, cfg *config.Config) (*Publisher, error) {
	// Get Blossom server URL
	blossomURL := config.GetEnv("BLOSSOM_URL")
	if blossomURL == "" {
		blossomURL = blossom.DefaultServer
	}

	// Create relay publisher
	relaysEnv := config.GetEnv("RELAY_URLS")
	publisher := nostr.NewPublisherFromEnv(relaysEnv)

	p := &Publisher{
		opts:       opts,
		cfg:        cfg,
		publisher:  publisher,
		blossomURL: blossomURL,
	}

	// Only create a remote source if needed (not needed for pure local file publishing)
	if len(cfg.LocalAssetFiles) == 0 || cfg.ReleaseSource != nil || cfg.Repository != "" {
		src, err := source.NewWithOptions(cfg, source.Options{
			BaseDir:            cfg.BaseDir,
			SkipCache:          opts.Publish.OverwriteRelease,
			IncludePreReleases: opts.Publish.IncludePreReleases,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create source: %w", err)
		}
		p.src = src
	}

	return p, nil
}

// Execute runs the complete publish workflow.
func (p *Publisher) Execute(ctx context.Context) error {
	// Determine total steps based on mode
	totalSteps := 4
	if p.opts.Publish.Offline {
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

	// Step 3: Sign & Upload (skip in offline mode)
	if steps != nil && !p.opts.Publish.Offline {
		steps.StartStep("Sign & Upload")
	}
	if err := p.signAndUpload(ctx); err != nil {
		return err
	}

	// Handle offline mode output
	if p.isOffline() {
		return p.outputOffline()
	}

	// Handle npub signer
	if p.signer != nil && p.signer.Type() == nostr.SignerNpub {
		return p.outputNpubEvents()
	}

	// Hash confirmation
	if !p.opts.Publish.Yes {
		isClosedSource := p.cfg.Repository == ""
		confirmed, err := confirmHashes(p.assetInfos, p.assetPaths, isClosedSource, p.opts.Publish.Legacy)
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

// fetchAssets fetches and selects assets to publish.
// Supports multiple local files (via -c flag) and multiple remote assets.
func (p *Publisher) fetchAssets(ctx context.Context) error {
	// Multi-file local mode: explicit file paths from CLI args
	if len(p.cfg.LocalAssetFiles) > 0 {
		return p.fetchLocalAssets(ctx)
	}

	if p.opts.Global.Verbose {
		fmt.Printf("Source type: %s\n", p.src.Type())
	}

	// Fetch release
	release, err := p.fetchRelease(ctx)
	if err != nil {
		return err
	}
	p.release = release

	// Select assets (may return multiple)
	assets, err := p.selectAssets(ctx)
	if err != nil {
		return err
	}
	p.selectedAssets = assets

	// Download and parse each asset
	for i, asset := range p.selectedAssets {
		if err := p.downloadAndParseOne(ctx, asset, i); err != nil {
			return err
		}
	}

	return p.postParseValidation(ctx)
}

// fetchLocalAssets handles explicit local file paths from -c flag.
func (p *Publisher) fetchLocalAssets(ctx context.Context) error {
	// Validate all files exist first
	for _, filePath := range p.cfg.LocalAssetFiles {
		resolved := resolvePath(filePath, p.cfg.BaseDir)
		if _, err := os.Stat(resolved); err != nil {
			return fmt.Errorf("file not found: %s", resolved)
		}
	}

	// Create a synthetic release for local files
	p.release = &source.Release{}

	// If the config has a release source (for version detection etc.), fetch it
	if p.cfg.ReleaseSource != nil && !p.cfg.ReleaseSource.IsLocal() {
		release, err := p.fetchRelease(ctx)
		if err != nil && err != ErrNothingToDo {
			// Non-fatal: we can proceed without release info
			if p.opts.Global.Verbose {
				fmt.Printf("  Could not fetch release info: %v\n", err)
			}
		} else if release != nil {
			p.release = release
		}
	}

	// Create assets from file paths
	for _, filePath := range p.cfg.LocalAssetFiles {
		resolved := resolvePath(filePath, p.cfg.BaseDir)
		asset := &source.Asset{
			Name:      filepath.Base(resolved),
			LocalPath: resolved,
		}
		p.selectedAssets = append(p.selectedAssets, asset)
	}

	if p.opts.Publish.ShouldShowSpinners() {
		if len(p.selectedAssets) == 1 {
			ui.PrintSuccess(fmt.Sprintf("Using local file %s", p.selectedAssets[0].Name))
		} else {
			names := make([]string, len(p.selectedAssets))
			for i, a := range p.selectedAssets {
				names[i] = a.Name
			}
			ui.PrintSuccess(fmt.Sprintf("Using %d local files: %s", len(p.selectedAssets), strings.Join(names, ", ")))
		}
	}

	// Parse each file
	for i, asset := range p.selectedAssets {
		if err := p.downloadAndParseOne(ctx, asset, i); err != nil {
			return err
		}
	}

	return p.postParseValidation(ctx)
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
			ui.PrintSuccess(fmt.Sprintf("Found %d assets (version pending)", len(release.Assets)))
		}
	}

	return release, nil
}

// selectAssets filters and selects assets from the release.
// When --match is used in non-interactive mode, all matching assets are selected.
// In interactive mode, shows a multi-select picker with ranked assets pre-selected.
func (p *Publisher) selectAssets(ctx context.Context) ([]*source.Asset, error) {
	assets := p.release.Assets

	// Try APK-specific path first
	apkAssets := picker.FilterAPKs(assets)

	// If there are APKs, use the APK selection path
	if len(apkAssets) > 0 {
		assets = apkAssets
	} else {
		// Non-APK: filter to supported formats (remove archives, checksums, etc.)
		assets = picker.FilterSupported(assets)
	}

	if len(assets) == 0 {
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
		return nil, fmt.Errorf("no supported assets found in release")
	}

	// Apply match filter if specified
	if p.cfg.Match != "" {
		var err error
		assets, err = picker.FilterByMatch(assets, p.cfg.Match)
		if err != nil {
			return nil, err
		}
		if len(assets) == 0 {
			return nil, fmt.Errorf("no assets match pattern: %s", p.cfg.Match)
		}

		// When --match is used in non-interactive mode, select ALL matching assets
		if !p.opts.Publish.IsInteractive() {
			if p.opts.Publish.ShouldShowSpinners() {
				ui.PrintSuccess(fmt.Sprintf("Selected %d assets matching %q", len(assets), p.cfg.Match))
			}
			return assets, nil
		}
	}

	// Single asset - use it
	if len(assets) == 1 {
		if p.opts.Publish.ShouldShowSpinners() {
			ui.PrintSuccess(fmt.Sprintf("Selected %s", assets[0].Name))
		}
		return []*source.Asset{assets[0]}, nil
	}

	// Multiple assets - rank
	ranked := picker.DefaultModel.RankAssets(assets)

	if p.opts.Global.Verbose {
		fmt.Println("  Ranked assets:")
		for i, sa := range ranked {
			fmt.Printf("    %d. %s (score: %.2f)\n", i+1, sa.Asset.Name, sa.Score)
		}
	}

	// Interactive multi-select if not quiet mode
	if p.opts.Publish.IsInteractive() && len(ranked) > 1 {
		return selectAssetsInteractive(ranked)
	}

	// Non-interactive: auto-select best match (single asset for backward compat)
	if p.opts.Publish.ShouldShowSpinners() {
		ui.PrintSuccess(fmt.Sprintf("Selected %s (best match)", ranked[0].Asset.Name))
	}
	return []*source.Asset{ranked[0].Asset}, nil
}

// downloadAndParseOne downloads (if needed) and parses a single asset.
// Appends the result to p.assetPaths and p.assetInfos.
func (p *Publisher) downloadAndParseOne(ctx context.Context, asset *source.Asset, index int) error {
	// Get asset path (download if needed)
	assetPath, err := p.getAssetPathFor(ctx, asset)
	if err != nil {
		return err
	}

	// Detect format and parse
	label := asset.Name
	if label == "" {
		label = filepath.Base(assetPath)
	}
	parseMsg := fmt.Sprintf("Parsing %s...", label)
	assetInfo, err := WithSpinner(p.opts, parseMsg, func() (*artifact.AssetInfo, error) {
		parser, err := artifact.Detect(assetPath)
		if err != nil {
			return nil, fmt.Errorf("unsupported file format: %w", err)
		}
		return parser.Parse(assetPath)
	})
	if err != nil {
		return fmt.Errorf("failed to parse asset %s: %w", label, err)
	}

	// APK-specific validation
	if assetInfo.IsAPK() {
		apkInfo := artifact.ToAPKInfo(assetInfo)
		if !apkInfo.IsArm64() {
			return fmt.Errorf("APK %s does not support arm64-v8a architecture (found: %v)", label, apkInfo.Architectures)
		}
	}

	if p.opts.Publish.ShouldShowSpinners() {
		ui.PrintSuccess(fmt.Sprintf("Parsed %s", label))
	}

	// Backfill version from asset if not known from release
	if p.release.Version == "" && assetInfo.Version != "" {
		p.release.Version = assetInfo.Version
	}

	// Backfill name for non-APK assets from filename (without extension)
	if !assetInfo.IsAPK() && assetInfo.Name == "" {
		name := filepath.Base(assetPath)
		if ext := filepath.Ext(name); ext != "" {
			name = strings.TrimSuffix(name, ext)
		}
		assetInfo.Name = name
	}

	// Backfill identifier for non-APK assets by slugifying the name
	if !assetInfo.IsAPK() && assetInfo.Identifier == "" {
		assetInfo.Identifier = slugify(assetInfo.Name)
	}

	p.assetPaths = append(p.assetPaths, assetPath)
	p.assetInfos = append(p.assetInfos, assetInfo)
	return nil
}

// postParseValidation runs after all assets are parsed. Applies version overrides
// and validates required fields.
func (p *Publisher) postParseValidation(ctx context.Context) error {
	// CLI --version flag overrides all other version sources
	if p.opts.Publish.Version != "" {
		p.release.Version = p.opts.Publish.Version
	}

	// Version is required — fail if still unknown after all sources
	if p.release.Version == "" {
		return fmt.Errorf("could not determine version; use --version <version> to set it explicitly")
	}

	// CLI --id flag overrides identifier for non-APK assets
	if p.opts.Publish.Identifier != "" {
		for _, ai := range p.assetInfos {
			if !ai.IsAPK() {
				ai.Identifier = p.opts.Publish.Identifier
			}
		}
	}

	// Propagate resolved version and identifier to all asset infos
	for i, ai := range p.assetInfos {
		ai.Version = p.release.Version

		// Identifier is required
		if ai.Identifier == "" {
			return fmt.Errorf("could not determine identifier for asset %s; use --id <identifier> to set it", filepath.Base(p.assetPaths[i]))
		}

		// Ensure all assets share the same identifier (one release = one app)
		if i > 0 && ai.Identifier != p.assetInfos[0].Identifier {
			// Allow it for non-APK assets where identifier is derived from filename
			// but warn in verbose mode
			if p.opts.Global.Verbose {
				fmt.Printf("  Note: asset %s has identifier %q (primary: %q)\n",
					filepath.Base(p.assetPaths[i]), ai.Identifier, p.assetInfos[0].Identifier)
			}
		}
	}

	// Display asset summary
	if p.opts.Publish.ShouldShowSpinners() {
		if len(p.assetInfos) == 1 {
			p.printAssetSummary(p.assetInfos[0], p.assetPaths[0])
		} else {
			ui.PrintSectionHeader(fmt.Sprintf("Assets (%d)", len(p.assetInfos)))
			for i, ai := range p.assetInfos {
				p.printAssetSummaryCompact(ai, p.assetPaths[i], i+1)
			}
		}
	}

	// Check if asset already exists on relays (check primary)
	return p.checkExistingAsset(ctx)
}

// printAssetSummary prints a detailed summary for a single asset.
func (p *Publisher) printAssetSummary(ai *artifact.AssetInfo, assetPath string) {
	if ai.IsAPK() {
		ui.PrintSectionHeader("APK Summary")
		ui.PrintKeyValue("Name", ai.Name)
		ui.PrintKeyValue("App ID", ai.Identifier)
		ui.PrintKeyValue("Version", fmt.Sprintf("%s (%d)", ai.Version, ai.APK.VersionCode))
		ui.PrintKeyValue("Certificate hash", ai.APK.CertFingerprint)
	} else {
		ui.PrintSectionHeader("Asset Summary")
		ui.PrintKeyValue("File", filepath.Base(assetPath))
		ui.PrintKeyValue("Identifier", ai.Identifier)
		ui.PrintKeyValue("Version", ai.Version)
		ui.PrintKeyValue("Type", ai.MIMEType)
		ui.PrintKeyValue("Platform", strings.Join(ai.Platforms, ", "))
	}
	ui.PrintKeyValue("Size", fmt.Sprintf("%.2f MB", float64(ai.FileSize)/(1024*1024)))
}

// printAssetSummaryCompact prints a compact one-line summary for multi-asset mode.
func (p *Publisher) printAssetSummaryCompact(ai *artifact.AssetInfo, assetPath string, num int) {
	platform := strings.Join(ai.Platforms, ", ")
	if platform == "" {
		platform = "unknown"
	}
	sizeMB := float64(ai.FileSize) / (1024 * 1024)
	fmt.Printf("  %d. %s  %s  %.1f MB\n", num, filepath.Base(assetPath), platform, sizeMB)
}

// getAssetPathFor returns the local path to an asset, downloading if necessary.
func (p *Publisher) getAssetPathFor(ctx context.Context, asset *source.Asset) (string, error) {
	if asset.LocalPath != "" {
		// Only print per-file message for the non-LocalAssetFiles path (single-file mode)
		if p.opts.Publish.ShouldShowSpinners() && len(p.cfg.LocalAssetFiles) == 0 {
			ui.PrintSuccess(fmt.Sprintf("Using local file %s", asset.Name))
		}
		return asset.LocalPath, nil
	}

	// Check download cache (evict if --overwrite-release so a replaced remote file is re-downloaded)
	if p.opts.Publish.OverwriteRelease && asset.URL != "" {
		_ = source.DeleteCachedDownload(asset.URL, asset.Name)
	} else if cachedPath := source.GetCachedDownload(asset.URL, asset.Name); cachedPath != "" {
		asset.LocalPath = cachedPath
		if p.opts.Publish.ShouldShowSpinners() {
			ui.PrintSuccess(fmt.Sprintf("Using cached %s", asset.Name))
		}
		return cachedPath, nil
	}

	// Download
	if p.opts.Global.Verbose {
		fmt.Printf("  Download URL: %s\n", asset.URL)
	}

	var tracker *ui.DownloadTracker
	var progressCallback source.DownloadProgress
	if p.opts.Publish.ShouldShowSpinners() {
		tracker = ui.NewDownloadTracker(fmt.Sprintf("Downloading %s", asset.Name), asset.Size)
		progressCallback = tracker.Callback()
	}

	assetPath, err := p.src.Download(ctx, asset, "", progressCallback)
	if tracker != nil {
		tracker.Done()
	}
	if err != nil {
		return "", fmt.Errorf("failed to download asset %s: %w", asset.Name, err)
	}

	return assetPath, nil
}

// checkExistingAsset checks if the release already exists on relays.
func (p *Publisher) checkExistingAsset(ctx context.Context) error {
	if p.opts.Publish.OverwriteRelease || p.opts.Publish.Offline {
		return nil
	}

	primary := p.primaryAssetInfo()
	if primary == nil {
		return nil
	}

	existingAsset, err := p.publisher.CheckExistingAsset(ctx, primary.Identifier, primary.Version)
	if err != nil {
		if p.opts.Global.Verbose {
			fmt.Printf("  Could not check relays: %v\n", err)
		}
		return nil
	}

	if existingAsset != nil {
		if p.opts.Publish.ShouldShowSpinners() {
			ui.PrintWarning(fmt.Sprintf("Asset %s@%s already exists on %s",
				primary.Identifier, primary.Version, existingAsset.RelayURL))
			fmt.Println("  Use --overwrite-release to publish anyway.")
		}
		return ErrNothingToDo
	}

	return nil
}

// gatherMetadata fetches metadata from external sources.
func (p *Publisher) gatherMetadata(ctx context.Context) error {
	primary := p.primaryAssetInfo()

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
		p.releaseNotes, err = source.FetchReleaseNotes(ctx, p.cfg.ReleaseNotes, primary.Version, p.cfg.BaseDir)
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

	primary := p.primaryAssetInfo()
	fetcher := source.NewMetadataFetcherWithPackageID(p.cfg, primary.Identifier)
	fetcher.APKName = primary.Name

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
	// Build preview data from all assets
	previewData := nostr.BuildPreviewDataFromAssets(p.assetInfos, p.cfg, p.releaseNotes, p.blossomURL, p.publisher.RelayURLs())

	// Override icon with pre-downloaded icon if available
	if p.preDownloaded != nil && p.preDownloaded.Icon != nil {
		previewData.IconData = p.preDownloaded.Icon.Data
	}

	// Add screenshots for preview (pre-downloaded remote + local files)
	preDownloadedByURL := make(map[string]*DownloadedImage)
	if p.preDownloaded != nil {
		for _, img := range p.preDownloaded.Images {
			preDownloadedByURL[img.URL] = img
		}
	}

	for _, img := range p.cfg.Images {
		if pd, ok := preDownloadedByURL[img]; ok {
			// Use pre-downloaded remote image
			previewData.ImageData = append(previewData.ImageData, nostr.PreviewImageData{
				Data:     pd.Data,
				MimeType: pd.MimeType,
			})
		} else if !isRemoteURL(img) {
			// Read local file for preview
			imgPath := resolvePath(img, p.cfg.BaseDir)
			data, err := os.ReadFile(imgPath)
			if err != nil {
				if p.opts.Global.Verbose {
					fmt.Printf("  Warning: failed to read local screenshot %s: %v\n", imgPath, err)
				}
				continue
			}
			previewData.ImageData = append(previewData.ImageData, nostr.PreviewImageData{
				Data:     data,
				MimeType: detectImageMimeType(imgPath),
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

	// When overwriting a release, fetch the existing 30063's created_at so the new
	// event gets a strictly higher timestamp and the relay's NIP-33 guard fires.
	if p.opts.Publish.OverwriteRelease && !p.isOffline() {
		ts, err := p.publisher.CheckExistingRelease(ctx, p.signer.PublicKey(), p.apkInfo.PackageID, p.apkInfo.VersionName)
		if err == nil {
			p.existingReleaseTimestamp = ts
		} else if p.opts.Global.Verbose {
			fmt.Printf("  Could not fetch existing release timestamp: %v\n", err)
		}
	}

	// Determine URLs and build events
	if p.isOffline() || p.signer.Type() == nostr.SignerNpub {
		return p.buildEventsWithoutUpload(ctx)
	}

	return p.uploadAndBuildEvents(ctx)
}

// createSigner creates the appropriate signer based on configuration.
func (p *Publisher) createSigner(ctx context.Context) error {
	signWith := config.GetSignWith()
	if signWith == "" {
		if p.opts.Publish.Quiet || p.opts.Publish.Offline {
			return fmt.Errorf("SIGN_WITH environment variable is required")
		}
		ui.PrintSectionHeader("Signing Setup")
		var err error
		signWith, err = config.PromptSignWith()
		if err != nil {
			return fmt.Errorf("signing setup failed: %w", err)
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

// isOffline returns true if running in offline mode.
func (p *Publisher) isOffline() bool {
	return p.opts.Publish.Offline
}

// buildEventsWithoutUpload builds events without uploading files (offline / npub mode).
func (p *Publisher) buildEventsWithoutUpload(ctx context.Context) error {
	primary := p.primaryAssetInfo()
	var err error
	p.iconURL, p.imageURLs, err = ResolveURLsWithoutUpload(ctx, p.cfg, primary, p.blossomURL, p.preDownloaded, p.opts)
	if err != nil {
		return err
	}

	p.events = nostr.BuildEventSet(nostr.BuildEventSetParams{
		Assets:                    p.buildAssetParams(),
		Config:                    p.cfg,
		Pubkey:                    p.signer.PublicKey(),
		BlossomServer:             p.blossomURL,
		IconURL:                   p.iconURL,
		ImageURLs:                 p.imageURLs,
		Changelog:                 p.releaseNotes,
		Commit:                    p.opts.Publish.Commit,
		Channel:                   p.opts.Publish.Channel,
		ReleaseURL:                p.getReleaseURL(),
		LegacyFormat:              p.opts.Publish.Legacy,
		ReleaseTimestamp:          p.getReleaseTimestamp(),
		UseReleaseTimestampForApp: p.opts.Publish.AppCreatedAtRelease,
		MinReleaseTimestamp:       p.existingReleaseTimestamp,
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
			Cfg:                 p.cfg,
			AssetInfos:          p.assetInfos,
			AssetPaths:          p.assetPaths,
			SelectedAssets:      p.selectedAssets,
			Release:             p.release,
			Client:              client,
			BlossomServer:       p.blossomURL,
			BatchSigner:         batchSigner,
			Pubkey:              p.signer.PublicKey(),
			RelayHint:           relayHint,
			PreDownloaded:       p.preDownloaded,
			VariantMatcher:      p.matchVariantFor,
			Commit:              p.opts.Publish.Commit,
			Channel:             p.opts.Publish.Channel,
			Opts:                p.opts,
			Legacy:              p.opts.Publish.Legacy,
			AppCreatedAtRelease: p.opts.Publish.AppCreatedAtRelease,
			MinReleaseTimestamp: p.existingReleaseTimestamp,
		})
		return err
	}

	// Regular signing mode: upload all assets
	var err error
	p.iconURL, p.imageURLs, err = UploadWithIndividualSigning(ctx, UploadParams{
		Cfg:            p.cfg,
		AssetInfos:     p.assetInfos,
		AssetPaths:     p.assetPaths,
		SelectedAssets: p.selectedAssets,
		Client:         client,
		Signer:         p.signer,
		PreDownloaded:  p.preDownloaded,
		Opts:           p.opts,
	})
	if err != nil {
		return err
	}

	p.events = nostr.BuildEventSet(nostr.BuildEventSetParams{
		Assets:                    p.buildAssetParams(),
		Config:                    p.cfg,
		Pubkey:                    p.signer.PublicKey(),
		BlossomServer:             p.blossomURL,
		IconURL:                   p.iconURL,
		ImageURLs:                 p.imageURLs,
		Changelog:                 p.releaseNotes,
		Commit:                    p.opts.Publish.Commit,
		Channel:                   p.opts.Publish.Channel,
		ReleaseURL:                p.getReleaseURL(),
		LegacyFormat:              p.opts.Publish.Legacy,
		ReleaseTimestamp:          p.getReleaseTimestamp(),
		UseReleaseTimestampForApp: p.opts.Publish.AppCreatedAtRelease,
		MinReleaseTimestamp:       p.existingReleaseTimestamp,
	})

	return nostr.SignEventSet(ctx, p.signer, p.events, relayHint)
}

// getRelayHint returns the first relay URL for event references.
func (p *Publisher) getRelayHint() string {
	relayHint := nostr.DefaultRelay
	if relaysEnv := config.GetEnv("RELAY_URLS"); relaysEnv != "" {
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

// getOriginalURLFor returns the original download URL for a specific asset.
func (p *Publisher) getOriginalURLFor(asset *source.Asset) string {
	if asset == nil {
		return ""
	}
	if asset.ExcludeURL {
		return ""
	}
	return asset.URL
}

// matchVariantFor returns the variant name if the asset matches a variant pattern.
func (p *Publisher) matchVariantFor(ai *artifact.AssetInfo) string {
	if len(p.cfg.Variants) == 0 {
		return ""
	}

	filename := filepath.Base(ai.FilePath)
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

// buildAssetParams creates AssetBuildParams for all selected assets.
func (p *Publisher) buildAssetParams() []nostr.AssetBuildParams {
	params := make([]nostr.AssetBuildParams, len(p.assetInfos))
	for i, ai := range p.assetInfos {
		var asset *source.Asset
		if i < len(p.selectedAssets) {
			asset = p.selectedAssets[i]
		}
		params[i] = nostr.AssetBuildParams{
			Asset:       ai,
			OriginalURL: p.getOriginalURLFor(asset),
			Variant:     p.matchVariantFor(ai),
		}
	}
	return params
}

// outputOffline outputs signed events to stdout and upload manifest to stderr.
func (p *Publisher) outputOffline() error {
	// Output events to stdout (JSON, one per line for piping to nak)
	OutputEventsToStdout(p.events)

	// Output upload manifest to stderr
	p.outputUploadManifest()

	return nil
}

// UploadManifestEntry represents a file that must be uploaded to Blossom.
type UploadManifestEntry struct {
	Description string // Human-readable description (e.g., "APK", "Icon", "Screenshot 1")
	FilePath    string // Local file path or "(from APK)" for extracted data
	SHA256      string // SHA256 hash of the file
	BlossomURL  string // Expected Blossom URL
}

// outputUploadManifest outputs the upload manifest to stderr.
func (p *Publisher) outputUploadManifest() {
	var entries []UploadManifestEntry

	// Asset entries
	for i, ai := range p.assetInfos {
		assetLabel := fmt.Sprintf("Asset %d", i+1)
		if len(p.assetInfos) == 1 {
			assetLabel = "Asset"
		}
		if ai.IsAPK() {
			assetLabel = "APK"
			if len(p.assetInfos) > 1 {
				assetLabel = fmt.Sprintf("APK %d", i+1)
			}
		}
		entries = append(entries, UploadManifestEntry{
			Description: assetLabel,
			FilePath:    p.assetPaths[i],
			SHA256:      ai.SHA256,
			BlossomURL:  fmt.Sprintf("%s/%s", p.blossomURL, ai.SHA256),
		})
	}

	// Icon entry
	if p.iconURL != "" {
		hash := extractHashFromBlossomURL(p.iconURL)
		iconPath := p.resolveIconPath(hash)
		entries = append(entries, UploadManifestEntry{
			Description: "Icon",
			FilePath:    iconPath,
			SHA256:      hash,
			BlossomURL:  p.iconURL,
		})
	}

	// Image entries
	for i, imgURL := range p.imageURLs {
		hash := extractHashFromBlossomURL(imgURL)
		imgPath := p.resolveImagePath(i, hash)
		entries = append(entries, UploadManifestEntry{
			Description: fmt.Sprintf("Screenshot %d", i+1),
			FilePath:    imgPath,
			SHA256:      hash,
			BlossomURL:  imgURL,
		})
	}

	// Output manifest to stderr
	OutputUploadManifest(entries, p.blossomURL)
}

// resolveIconPath returns the path to the icon file, saving APK-extracted icons to temp.
func (p *Publisher) resolveIconPath(hash string) string {
	// Config icon takes precedence
	if p.cfg.Icon != "" {
		if isRemoteURL(p.cfg.Icon) {
			// Pre-downloaded remote icon
			if p.preDownloaded != nil && p.preDownloaded.Icon != nil {
				return p.saveToTemp("icon", p.preDownloaded.Icon.Data, hash)
			}
			return p.cfg.Icon + " (download required)"
		}
		return resolvePath(p.cfg.Icon, p.cfg.BaseDir)
	}

	// Asset-extracted icon (APKs may embed icons)
	primary := p.primaryAssetInfo()
	if primary != nil && primary.Icon != nil {
		return p.saveToTemp("icon", primary.Icon, hash)
	}

	return "(none)"
}

// resolveImagePath returns the path to an image file, saving downloaded images to temp.
func (p *Publisher) resolveImagePath(index int, hash string) string {
	// Check pre-downloaded images first
	if p.preDownloaded != nil && index < len(p.preDownloaded.Images) {
		img := p.preDownloaded.Images[index]
		return p.saveToTemp(fmt.Sprintf("screenshot_%d", index+1), img.Data, hash)
	}

	// Config images
	if index < len(p.cfg.Images) {
		img := p.cfg.Images[index]
		if isRemoteURL(img) {
			return img + " (download required)"
		}
		return resolvePath(img, p.cfg.BaseDir)
	}

	return "(none)"
}

// saveToTemp saves data to a temp file and returns the path.
func (p *Publisher) saveToTemp(prefix string, data []byte, hash string) string {
	// Use hash as filename for easy identification
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("zsp_%s_%s", prefix, hash[:16]))
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Sprintf("(failed to save: %v)", err)
	}
	return tmpFile
}

// extractHashFromBlossomURL extracts the SHA256 hash from a Blossom URL.
func extractHashFromBlossomURL(url string) string {
	// URL format: https://cdn.zapstore.dev/{sha256}
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
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
		primary := p.primaryAssetInfo()
		if allSuccess {
			assetCountStr := ""
			if len(p.assetInfos) > 1 {
				assetCountStr = fmt.Sprintf(" (%d assets)", len(p.assetInfos))
			}
			ui.PrintCompletionSummary(true, fmt.Sprintf("Published %s v%s%s to %s",
				primary.Identifier, primary.Version, assetCountStr, strings.Join(p.publisher.RelayURLs(), ", ")))
		} else {
			ui.PrintCompletionSummary(false, "Published with some failures")
		}
	}

	// Show zapstore.dev URL if the app was successfully published to relay.zapstore.dev
	if allSuccess {
		p.showZapstoreURL(results)
	}

	return nil
}

// showZapstoreURL prints the zapstore.dev app URL if the app was published to relay.zapstore.dev.
func (p *Publisher) showZapstoreURL(results map[string][]nostr.PublishResult) {
	// Check if relay.zapstore.dev accepted the software_application event
	const zapstoreRelayHost = "relay.zapstore.dev"
	accepted := false
	for _, r := range results["software_application"] {
		if r.Success && strings.Contains(r.RelayURL, zapstoreRelayHost) {
			accepted = true
			break
		}
	}
	if !accepted {
		return
	}

	// Extract d tag (identifier) from the AppMetadata event
	event := p.events.AppMetadata
	identifier := ""
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			identifier = tag[1]
			break
		}
	}
	if identifier == "" {
		return
	}

	// Only show zapstore.dev URL for Android apps
	hasAndroid := false
	for _, ai := range p.assetInfos {
		if ai != nil && ai.IsAPK() {
			hasAndroid = true
			break
		}
	}
	if !hasAndroid {
		return
	}

	// Encode as naddr (kind 32267, pubkey, identifier, relay hint)
	naddr, err := nip19.EncodeEntity(event.PubKey, event.Kind, identifier, []string{"wss://" + zapstoreRelayHost})
	if err != nil {
		return
	}

	fmt.Printf("  View your app: https://zapstore.dev/apps/%s\n\n", naddr)
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

// deleteCachedAPK removes cached asset files after successful publishing.
func (p *Publisher) deleteCachedAPK() {
	for _, asset := range p.selectedAssets {
		if asset == nil || asset.URL == "" {
			continue // Local file or no URL, nothing to delete
		}
		_ = source.DeleteCachedDownload(asset.URL, asset.Name)
	}
}

// Close releases resources.
func (p *Publisher) Close() {
	if p.signer != nil {
		p.signer.Close()
	}
}

// ErrNothingToDo indicates no publishing is needed.
var ErrNothingToDo = fmt.Errorf("nothing to do")
