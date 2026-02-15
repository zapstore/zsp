package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/artifact"
	"github.com/zapstore/zsp/internal/blossom"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/source"
	"github.com/zapstore/zsp/internal/ui"
)

// DownloadedImage holds pre-downloaded image data.
type DownloadedImage struct {
	URL      string // Original URL
	Data     []byte // Image bytes
	Hash     string // SHA256 hash (hex)
	MimeType string // MIME type
}

// PreDownloadedImages holds all pre-downloaded images for a release.
type PreDownloadedImages struct {
	Icon   *DownloadedImage   // Icon (from cfg.Icon if remote URL)
	Images []*DownloadedImage // Screenshots (from cfg.Images if remote URLs)
}

// VariantMatcher is a function that returns the variant name for an asset.
type VariantMatcher func(ai *artifact.AssetInfo) string

// UploadParams contains parameters for upload functions.
type UploadParams struct {
	Cfg                 *config.Config
	AssetInfos          []*artifact.AssetInfo // Multiple assets
	AssetPaths          []string              // Local paths per asset
	SelectedAssets      []*source.Asset       // Source assets (for URL info)
	Release             *source.Release
	Client              *blossom.Client
	BlossomServer       string
	BatchSigner         nostr.BatchSigner
	Signer              nostr.Signer
	Pubkey              string
	RelayHint           string
	PreDownloaded       *PreDownloadedImages
	VariantMatcher      VariantMatcher
	Commit              string
	Channel             string
	Opts                *cli.Options
	Legacy              bool
	AppCreatedAtRelease bool
	MinReleaseTimestamp time.Time // Bump Release.CreatedAt above this (--overwrite-release)

	// Deprecated single-asset fields (for backward compat in tests)
	AssetInfo   *artifact.AssetInfo
	AssetPath   string
	OriginalURL string
	Variant     string
}

// resolvedAssetInfos returns the asset info list, falling back to single-asset field.
func (p UploadParams) resolvedAssetInfos() []*artifact.AssetInfo {
	if len(p.AssetInfos) > 0 {
		return p.AssetInfos
	}
	if p.AssetInfo != nil {
		return []*artifact.AssetInfo{p.AssetInfo}
	}
	return nil
}

// resolvedAssetPaths returns the asset path list, falling back to single-asset field.
func (p UploadParams) resolvedAssetPaths() []string {
	if len(p.AssetPaths) > 0 {
		return p.AssetPaths
	}
	if p.AssetPath != "" {
		return []string{p.AssetPath}
	}
	return nil
}

// primaryAssetInfo returns the first asset info.
func (p UploadParams) primaryAssetInfo() *artifact.AssetInfo {
	infos := p.resolvedAssetInfos()
	if len(infos) > 0 {
		return infos[0]
	}
	return nil
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

// PreDownloadImages downloads cfg.Icon and cfg.Images if they are remote URLs.
func PreDownloadImages(ctx context.Context, cfg *config.Config, opts *cli.Options) (*PreDownloadedImages, error) {
	result := &PreDownloadedImages{}

	// Download icon if it's a remote URL
	if cfg.Icon != "" && isRemoteURL(cfg.Icon) {
		img, err := downloadImageWithSpinner(ctx, cfg.Icon, "icon", opts)
		if err != nil {
			return nil, fmt.Errorf("failed to download icon from %s: %w", cfg.Icon, err)
		}
		result.Icon = img
	}

	// Download screenshots if they are remote URLs
	remoteImages := countRemoteImages(cfg.Images)
	if remoteImages > 0 {
		var spinner *ui.Spinner
		if opts.Publish.ShouldShowSpinners() {
			spinner = ui.NewStatusSpinner("Downloading", fmt.Sprintf("0/%d screenshots...", remoteImages))
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
					spinner.Warn("Warning", fmt.Sprintf("Failed to download screenshot: %v", err))
				}
				continue
			}

			downloaded++
			if spinner != nil {
				spinner.UpdateMessage(fmt.Sprintf("Downloading %d/%d screenshots...", downloaded, remoteImages))
			}

			result.Images = append(result.Images, &DownloadedImage{
				URL:      img,
				Data:     data,
				Hash:     hash,
				MimeType: mimeType,
			})
		}

		if spinner != nil {
			spinner.Done("Downloaded", fmt.Sprintf("%d screenshots", len(result.Images)))
		}
	}

	return result, nil
}

// downloadImageWithSpinner downloads an image with spinner feedback.
func downloadImageWithSpinner(ctx context.Context, url, imageType string, opts *cli.Options) (*DownloadedImage, error) {
	var spinner *ui.Spinner
	if opts.Publish.ShouldShowSpinners() {
		spinner = ui.NewStatusSpinner("Downloading", imageType+"...")
		spinner.Start()
	}

	data, hash, mimeType, err := downloadRemoteImage(ctx, url)
	if err != nil {
		if spinner != nil {
			spinner.Fail("Error", fmt.Sprintf("Failed to download %s", imageType))
		}
		return nil, err
	}

	if spinner != nil {
		spinner.Done("Downloaded", imageType)
	}

	return &DownloadedImage{
		URL:      url,
		Data:     data,
		Hash:     hash,
		MimeType: mimeType,
	}, nil
}

// ResolveURLsWithoutUpload computes Blossom URLs by downloading/reading files and computing hashes,
// but without actually uploading to Blossom. Used for offline and npub modes.
func ResolveURLsWithoutUpload(ctx context.Context, cfg *config.Config, assetInfo *artifact.AssetInfo, blossomURL string, preDownloaded *PreDownloadedImages, opts *cli.Options) (iconURL string, imageURLs []string, err error) {
	// Process icon
	iconURL, err = resolveIconURL(ctx, cfg, assetInfo, blossomURL, preDownloaded, opts)
	if err != nil {
		return "", nil, err
	}

	// Process images
	imageURLs, err = resolveImageURLs(ctx, cfg, blossomURL, preDownloaded)
	if err != nil {
		return "", nil, err
	}

	return iconURL, imageURLs, nil
}

// resolveIconURL resolves the icon URL without uploading.
func resolveIconURL(ctx context.Context, cfg *config.Config, assetInfo *artifact.AssetInfo, blossomURL string, preDownloaded *PreDownloadedImages, opts *cli.Options) (string, error) {
	if preDownloaded != nil && preDownloaded.Icon != nil {
		return fmt.Sprintf("%s/%s", blossomURL, preDownloaded.Icon.Hash), nil
	}

	if cfg.Icon != "" {
		if isRemoteURL(cfg.Icon) {
			var spinner *ui.Spinner
			if opts.Publish.ShouldShowSpinners() {
				spinner = ui.NewStatusSpinner("Fetching", "icon (for hash)...")
				spinner.Start()
			}
			_, hashStr, _, err := downloadRemoteImage(ctx, cfg.Icon)
			if err != nil {
				if spinner != nil {
					spinner.Fail("Error", "Failed to fetch icon")
				}
				return "", fmt.Errorf("failed to fetch icon from %s: %w", cfg.Icon, err)
			}
			if spinner != nil {
				spinner.Done("Fetched", "icon")
			}
			return fmt.Sprintf("%s/%s", blossomURL, hashStr), nil
		}

		// Local file
		iconPath := resolvePath(cfg.Icon, cfg.BaseDir)
		iconData, err := os.ReadFile(iconPath)
		if err != nil {
			return "", fmt.Errorf("failed to read icon file %s: %w", iconPath, err)
		}
		hash := sha256.Sum256(iconData)
		return fmt.Sprintf("%s/%s", blossomURL, hex.EncodeToString(hash[:])), nil
	}

	if assetInfo != nil && assetInfo.Icon != nil {
		hash := sha256.Sum256(assetInfo.Icon)
		return fmt.Sprintf("%s/%s", blossomURL, hex.EncodeToString(hash[:])), nil
	}

	return "", nil
}

// resolveImageURLs resolves image URLs without uploading.
func resolveImageURLs(ctx context.Context, cfg *config.Config, blossomURL string, preDownloaded *PreDownloadedImages) ([]string, error) {
	var imageURLs []string

	// Process pre-downloaded images first
	if preDownloaded != nil && len(preDownloaded.Images) > 0 {
		for _, img := range preDownloaded.Images {
			imageURLs = append(imageURLs, fmt.Sprintf("%s/%s", blossomURL, img.Hash))
		}
	}

	// Process remaining images from config
	for _, img := range cfg.Images {
		// Skip remote images that were already handled via pre-download
		if isRemoteURL(img) && preDownloaded != nil && findPreDownloadedImage(preDownloaded.Images, img) != nil {
			continue
		}

		if isRemoteURL(img) {
			_, hashStr, _, err := downloadRemoteImage(ctx, img)
			if err != nil {
				continue // Log warning but continue
			}
			imageURLs = append(imageURLs, fmt.Sprintf("%s/%s", blossomURL, hashStr))
		} else {
			imgPath := resolvePath(img, cfg.BaseDir)
			imgData, err := os.ReadFile(imgPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read image file %s: %w", imgPath, err)
			}
			hash := sha256.Sum256(imgData)
			imageURLs = append(imageURLs, fmt.Sprintf("%s/%s", blossomURL, hex.EncodeToString(hash[:])))
		}
	}

	return imageURLs, nil
}

// UploadAndSignWithBatch handles uploads and signing when using a batch signer.
func UploadAndSignWithBatch(ctx context.Context, params UploadParams) (*nostr.EventSet, error) {
	var uploads []uploadItem
	var iconURL string
	var imageURLs []string
	expiration := time.Now().Add(blossom.AuthExpiration)

	// Collect icon upload
	iconURL, iconUploads := collectIconUpload(ctx, params, expiration)
	uploads = append(uploads, iconUploads...)

	// Collect image uploads
	imgURLs, imgUploads := collectImageUploads(ctx, params, expiration)
	imageURLs = append(imageURLs, imgURLs...)
	uploads = append(uploads, imgUploads...)

	// Add asset uploads for each asset
	assetInfos := params.resolvedAssetInfos()
	assetPaths := params.resolvedAssetPaths()
	for i, ai := range assetInfos {
		uploads = append(uploads, uploadItem{
			isAPK:     true, // reuse field name — means "is the main asset"
			apkPath:   assetPaths[i],
			hash:      ai.SHA256,
			authEvent: nostr.BuildBlossomAuthEvent(ai.SHA256, params.Pubkey, expiration),
		})
	}

	// Build asset params for multi-asset event set
	assetBuildParams := buildAssetBuildParams(params)

	primary := params.primaryAssetInfo()
	releaseNotes := ""
	if params.Release != nil {
		releaseNotes = params.Release.Changelog
	}
	if params.Cfg.ReleaseNotes != "" {
		var err error
		releaseNotes, err = source.FetchReleaseNotes(ctx, params.Cfg.ReleaseNotes, primary.Version, params.Cfg.BaseDir)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch release notes: %w", err)
		}
	}

	var releaseURL string
	var releaseTimestamp time.Time
	if params.Release != nil {
		releaseURL = params.Release.URL
		releaseTimestamp = params.Release.CreatedAt
	}

	events := nostr.BuildEventSet(nostr.BuildEventSetParams{
		Assets:                    assetBuildParams,
		Config:                    params.Cfg,
		Pubkey:                    params.Pubkey,
		BlossomServer:             params.BlossomServer,
		IconURL:                   iconURL,
		ImageURLs:                 imageURLs,
		Changelog:                 releaseNotes,
		Commit:                    params.Commit,
		Channel:                   params.Channel,
		ReleaseURL:                releaseURL,
		LegacyFormat:              params.Legacy,
		ReleaseTimestamp:          releaseTimestamp,
		UseReleaseTimestampForApp: params.AppCreatedAtRelease,
		MinReleaseTimestamp:       params.MinReleaseTimestamp,
	})

	// Pre-compute asset event IDs
	for _, asset := range events.SoftwareAssets {
		asset.PubKey = params.Pubkey
		assetID := asset.GetID()
		events.AddAssetReference(assetID, params.RelayHint)
	}

	// Collect ALL events to sign
	allEvents := make([]*gonostr.Event, 0, len(uploads)+2+len(events.SoftwareAssets))
	for _, u := range uploads {
		allEvents = append(allEvents, u.authEvent)
	}
	allEvents = append(allEvents, events.AppMetadata, events.Release)
	allEvents = append(allEvents, events.SoftwareAssets...)

	// Pre-check existence for non-APK uploads
	existsMap := checkUploadsExist(ctx, params.Client, uploads, params.Opts)

	// Batch sign everything
	var signSpinner *ui.Spinner
	if params.Opts.Publish.ShouldShowSpinners() {
		signSpinner = ui.NewStatusSpinner("Signing", fmt.Sprintf("%d events...", len(allEvents)))
		signSpinner.Start()
	}
	if err := params.BatchSigner.SignBatch(ctx, allEvents); err != nil {
		if signSpinner != nil {
			signSpinner.Fail("Error", "Failed to sign events")
		}
		return nil, fmt.Errorf("failed to batch sign events: %w", err)
	}
	if signSpinner != nil {
		signSpinner.Done("Signed", "events")
	}

	// Perform uploads
	if err := performUploads(ctx, params.Client, uploads, existsMap, params.Opts); err != nil {
		return nil, err
	}

	return events, nil
}

// buildAssetBuildParams creates AssetBuildParams from UploadParams.
func buildAssetBuildParams(params UploadParams) []nostr.AssetBuildParams {
	assetInfos := params.resolvedAssetInfos()
	result := make([]nostr.AssetBuildParams, len(assetInfos))
	for i, ai := range assetInfos {
		originalURL := ""
		variant := ""
		if i < len(params.SelectedAssets) && params.SelectedAssets[i] != nil {
			asset := params.SelectedAssets[i]
			if !asset.ExcludeURL {
				originalURL = asset.URL
			}
		}
		if params.VariantMatcher != nil {
			variant = params.VariantMatcher(ai)
		}
		result[i] = nostr.AssetBuildParams{
			Asset:       ai,
			OriginalURL: originalURL,
			Variant:     variant,
		}
	}
	return result
}

// UploadWithIndividualSigning handles uploads with regular signers.
func UploadWithIndividualSigning(ctx context.Context, params UploadParams) (iconURL string, imageURLs []string, err error) {
	// Process icon
	iconURL, err = uploadIcon(ctx, params)
	if err != nil {
		return "", nil, err
	}

	// Process pre-downloaded images
	if params.PreDownloaded != nil && len(params.PreDownloaded.Images) > 0 {
		urls, err := uploadPreDownloadedImages(ctx, params)
		if err != nil {
			return "", nil, err
		}
		imageURLs = append(imageURLs, urls...)
	}

	// Process remaining config images
	urls, err := uploadConfigImages(ctx, params)
	if err != nil {
		return "", nil, err
	}
	imageURLs = append(imageURLs, urls...)

	// Upload all assets
	assetInfos := params.resolvedAssetInfos()
	assetPaths := params.resolvedAssetPaths()
	for i, ai := range assetInfos {
		if err := uploadAsset(ctx, params, ai, assetPaths[i]); err != nil {
			return "", nil, err
		}
	}

	return iconURL, imageURLs, nil
}

// uploadIcon handles icon upload with various sources.
func uploadIcon(ctx context.Context, params UploadParams) (string, error) {
	client := params.Client
	signer := params.Signer
	opts := params.Opts

	// Pre-downloaded icon
	if params.PreDownloaded != nil && params.PreDownloaded.Icon != nil {
		return uploadIconData(ctx, client, signer, params.PreDownloaded.Icon, opts)
	}

	// Config icon
	if params.Cfg.Icon != "" {
		if isRemoteURL(params.Cfg.Icon) {
			if isBlossomURL(params.Cfg.Icon, client.ServerURL()) {
				return params.Cfg.Icon, nil
			}
			// Download and upload
			img, err := downloadAndUploadIcon(ctx, client, signer, params.Cfg.Icon, opts)
			if err != nil {
				return "", err
			}
			return img, nil
		}

		// Local file
		return uploadLocalIcon(ctx, client, signer, params.Cfg.Icon, params.Cfg.BaseDir, opts)
	}

	// Asset-embedded icon (APKs may embed icons)
	primary := params.primaryAssetInfo()
	if primary != nil && primary.Icon != nil {
		return uploadAPKIcon(ctx, client, signer, primary.Icon, opts)
	}

	return "", nil
}

// uploadIconData uploads pre-downloaded icon data.
func uploadIconData(ctx context.Context, client *blossom.Client, signer nostr.Signer, img *DownloadedImage, opts *cli.Options) (string, error) {
	var spinner *ui.Spinner
	if opts.Publish.ShouldShowSpinners() {
		spinner = ui.NewStatusSpinner("Uploading", "icon...")
		spinner.Start()
	}

	result, err := client.UploadBytes(ctx, img.Data, img.Hash, img.MimeType, signer)
	if err != nil {
		if spinner != nil {
			spinner.Fail("Error", "Failed to upload icon")
		}
		return "", fmt.Errorf("failed to upload icon: %w", err)
	}

	if spinner != nil {
		if result.Existed {
			spinner.Done("Exists", fmt.Sprintf("Icon (%s)", result.URL))
		} else {
			spinner.Done("Uploaded", "icon")
		}
	}

	return result.URL, nil
}

// downloadAndUploadIcon downloads and uploads a remote icon.
func downloadAndUploadIcon(ctx context.Context, client *blossom.Client, signer nostr.Signer, url string, opts *cli.Options) (string, error) {
	var spinner *ui.Spinner
	if opts.Publish.ShouldShowSpinners() {
		spinner = ui.NewStatusSpinner("Fetching", "icon...")
		spinner.Start()
	}

	iconData, hashStr, mimeType, err := downloadRemoteImage(ctx, url)
	if err != nil {
		if spinner != nil {
			spinner.Fail("Error", "Failed to fetch icon")
		}
		return "", fmt.Errorf("failed to fetch icon from %s: %w", url, err)
	}

	if spinner != nil {
		spinner.UpdateMessage("Uploading icon...")
	}

	result, err := client.UploadBytes(ctx, iconData, hashStr, mimeType, signer)
	if err != nil {
		if spinner != nil {
			spinner.Fail("Error", "Failed to upload icon")
		}
		return "", fmt.Errorf("failed to upload icon: %w", err)
	}

	if spinner != nil {
		if result.Existed {
			spinner.Done("Exists", fmt.Sprintf("Icon (%s)", result.URL))
		} else {
			spinner.Done("Uploaded", "icon")
		}
	}

	return result.URL, nil
}

// uploadLocalIcon uploads a local icon file.
func uploadLocalIcon(ctx context.Context, client *blossom.Client, signer nostr.Signer, iconPath, baseDir string, opts *cli.Options) (string, error) {
	fullPath := resolvePath(iconPath, baseDir)
	iconData, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to read icon file %s: %w", fullPath, err)
	}

	hash := sha256.Sum256(iconData)
	hashStr := hex.EncodeToString(hash[:])
	mimeType := detectImageMimeType(fullPath)

	var spinner *ui.Spinner
	if opts.Publish.ShouldShowSpinners() {
		spinner = ui.NewStatusSpinner("Uploading", "icon...")
		spinner.Start()
	}

	result, err := client.UploadBytes(ctx, iconData, hashStr, mimeType, signer)
	if err != nil {
		if spinner != nil {
			spinner.Fail("Error", "Failed to upload icon")
		}
		return "", fmt.Errorf("failed to upload icon: %w", err)
	}

	if spinner != nil {
		if result.Existed {
			spinner.Done("Exists", fmt.Sprintf("Icon (%s)", result.URL))
		} else {
			spinner.Done("Uploaded", "icon")
		}
	}

	return result.URL, nil
}

// uploadAPKIcon uploads the icon extracted from the APK.
func uploadAPKIcon(ctx context.Context, client *blossom.Client, signer nostr.Signer, iconData []byte, opts *cli.Options) (string, error) {
	hash := sha256.Sum256(iconData)
	hashStr := hex.EncodeToString(hash[:])

	var spinner *ui.Spinner
	if opts.Publish.ShouldShowSpinners() {
		spinner = ui.NewStatusSpinner("Uploading", "icon...")
		spinner.Start()
	}

	result, err := client.UploadBytes(ctx, iconData, hashStr, "image/png", signer)
	if err != nil {
		if spinner != nil {
			spinner.Fail("Error", "Failed to upload icon")
		}
		return "", fmt.Errorf("failed to upload icon: %w", err)
	}

	if spinner != nil {
		if result.Existed {
			spinner.Done("Exists", fmt.Sprintf("Icon (%s)", result.URL))
		} else {
			spinner.Done("Uploaded", "icon")
		}
	}

	return result.URL, nil
}

// uploadPreDownloadedImages uploads pre-downloaded screenshots.
func uploadPreDownloadedImages(ctx context.Context, params UploadParams) ([]string, error) {
	var urls []string
	total := len(params.PreDownloaded.Images)

	for i, img := range params.PreDownloaded.Images {
		var spinner *ui.Spinner
		if params.Opts.Publish.ShouldShowSpinners() {
			spinner = ui.NewStatusSpinner("Uploading", fmt.Sprintf("screenshot (%d/%d)...", i+1, total))
			spinner.Start()
		}

		result, err := params.Client.UploadBytes(ctx, img.Data, img.Hash, img.MimeType, params.Signer)
		if err != nil {
			if spinner != nil {
				spinner.Fail("Error", fmt.Sprintf("Failed to upload screenshot %d", i+1))
			}
			return nil, fmt.Errorf("failed to upload screenshot: %w", err)
		}

		if spinner != nil {
			if result.Existed {
spinner.Done("Exists", fmt.Sprintf("Screenshot (%d/%d) (%s)", i+1, total, result.URL))
				} else {
					spinner.Done("Uploaded", fmt.Sprintf("screenshot (%d/%d)", i+1, total))
			}
		}

		urls = append(urls, result.URL)
	}

	return urls, nil
}

// uploadConfigImages uploads images from config that weren't pre-downloaded.
func uploadConfigImages(ctx context.Context, params UploadParams) ([]string, error) {
	var urls []string
	total := len(params.Cfg.Images)

	for i, img := range params.Cfg.Images {
		// Skip images already handled via pre-download
		if isRemoteURL(img) && params.PreDownloaded != nil && findPreDownloadedImage(params.PreDownloaded.Images, img) != nil {
			continue
		}

		if isRemoteURL(img) {
			if isBlossomURL(img, params.Client.ServerURL()) {
				urls = append(urls, img)
				continue
			}

			// Download and upload
			url, err := downloadAndUploadImage(ctx, params.Client, params.Signer, img, i+1, total, params.Opts)
			if err != nil {
				if params.Opts.Global.Verbosity >= 1 {
					ui.WarningStatus("Warning", fmt.Sprintf("failed to upload screenshot: %v", err))
				}
				continue
			}
			urls = append(urls, url)
		} else {
			url, err := uploadLocalImage(ctx, params.Client, params.Signer, img, params.Cfg.BaseDir, params.Opts)
			if err != nil {
				return nil, err
			}
			urls = append(urls, url)
		}
	}

	return urls, nil
}

// downloadAndUploadImage downloads and uploads a remote image.
func downloadAndUploadImage(ctx context.Context, client *blossom.Client, signer nostr.Signer, url string, index, total int, opts *cli.Options) (string, error) {
	var spinner *ui.Spinner
	if opts.Publish.ShouldShowSpinners() {
		spinner = ui.NewStatusSpinner("Fetching", fmt.Sprintf("screenshot (%d/%d)...", index, total))
		spinner.Start()
	}

	imgData, hashStr, mimeType, err := downloadRemoteImage(ctx, url)
	if err != nil {
		if spinner != nil {
			spinner.Fail("Error", fmt.Sprintf("Failed to fetch screenshot %d", index))
		}
		return "", err
	}

	if spinner != nil {
		spinner.UpdateMessage(fmt.Sprintf("Uploading screenshot (%d/%d)...", index, total))
	}

	result, err := client.UploadBytes(ctx, imgData, hashStr, mimeType, signer)
	if err != nil {
		if spinner != nil {
			spinner.Fail("Error", fmt.Sprintf("Failed to upload screenshot %d", index))
		}
		return "", err
	}

	if spinner != nil {
		if result.Existed {
spinner.Done("Exists", fmt.Sprintf("Screenshot (%d/%d) (%s)", index, total, result.URL))
			} else {
				spinner.Done("Uploaded", fmt.Sprintf("screenshot (%d/%d)", index, total))
		}
	}

	return result.URL, nil
}

// uploadLocalImage uploads a local image file.
func uploadLocalImage(ctx context.Context, client *blossom.Client, signer nostr.Signer, imgPath, baseDir string, opts *cli.Options) (string, error) {
	fullPath := resolvePath(imgPath, baseDir)
	imgData, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to read image file %s: %w", fullPath, err)
	}

	hash := sha256.Sum256(imgData)
	hashStr := hex.EncodeToString(hash[:])
	mimeType := detectImageMimeType(fullPath)

	var spinner *ui.Spinner
	if opts.Publish.ShouldShowSpinners() {
		spinner = ui.NewStatusSpinner("Uploading", fmt.Sprintf("image %s...", imgPath))
		spinner.Start()
	}

	result, err := client.UploadBytes(ctx, imgData, hashStr, mimeType, signer)
	if err != nil {
		if spinner != nil {
			spinner.Fail("Error", fmt.Sprintf("Failed to upload image %s", imgPath))
		}
		return "", fmt.Errorf("failed to upload image %s: %w", imgPath, err)
	}

	if spinner != nil {
		if result.Existed {
			spinner.Done("Exists", fmt.Sprintf("Image %s (%s)", imgPath, result.URL))
		} else {
			spinner.Done("Uploaded", fmt.Sprintf("image %s", imgPath))
		}
	}

	return result.URL, nil
}

// uploadAsset uploads a single asset file to Blossom.
func uploadAsset(ctx context.Context, params UploadParams, ai *artifact.AssetInfo, assetPath string) error {
	label := filepath.Base(assetPath)
	if ai.IsAPK() {
		label = "APK " + label
	}

	var tracker *ui.DownloadTracker
	var uploadCallback func(uploaded, total int64)
	if params.Opts.Publish.ShouldShowSpinners() {
		fileInfo, _ := os.Stat(assetPath)
		var size int64
		if fileInfo != nil {
			size = fileInfo.Size()
		}
		tracker = ui.NewDownloadTracker(fmt.Sprintf("Uploading %s to %s", label, params.Client.ServerURL()), size)
		uploadCallback = tracker.Callback()
	}

	result, err := params.Client.Upload(ctx, assetPath, ai.SHA256, params.Signer, uploadCallback)
	if err != nil {
		return fmt.Errorf("failed to upload %s: %w", label, err)
	}

	if tracker != nil {
		if result.Existed {
			tracker.DoneWithMessage(fmt.Sprintf("%s already exists (%s)", label, result.URL))
		} else {
			tracker.Done()
		}
	}

	return nil
}

// collectIconUpload collects icon upload data for batch signing.
func collectIconUpload(ctx context.Context, params UploadParams, expiration time.Time) (string, []uploadItem) {
	var uploads []uploadItem
	var iconURL string
	client := params.Client

	if params.PreDownloaded != nil && params.PreDownloaded.Icon != nil {
		iconURL = fmt.Sprintf("%s/%s", client.ServerURL(), params.PreDownloaded.Icon.Hash)
		uploads = append(uploads, uploadItem{
			data:       params.PreDownloaded.Icon.Data,
			hash:       params.PreDownloaded.Icon.Hash,
			mimeType:   params.PreDownloaded.Icon.MimeType,
			authEvent:  nostr.BuildBlossomAuthEvent(params.PreDownloaded.Icon.Hash, params.Pubkey, expiration),
			uploadType: "icon",
		})
		return iconURL, uploads
	}

	if params.Cfg.Icon != "" {
		if isRemoteURL(params.Cfg.Icon) {
			if isBlossomURL(params.Cfg.Icon, client.ServerURL()) {
				return params.Cfg.Icon, nil
			}
			// Download for batch
			iconData, iconHash, mimeType, err := downloadRemoteImage(ctx, params.Cfg.Icon)
			if err == nil {
				iconURL = fmt.Sprintf("%s/%s", client.ServerURL(), iconHash)
				uploads = append(uploads, uploadItem{
					data:       iconData,
					hash:       iconHash,
					mimeType:   mimeType,
					authEvent:  nostr.BuildBlossomAuthEvent(iconHash, params.Pubkey, expiration),
					uploadType: "icon",
				})
			}
		} else {
			iconPath := resolvePath(params.Cfg.Icon, params.Cfg.BaseDir)
			iconData, err := os.ReadFile(iconPath)
			if err == nil {
				hash := sha256.Sum256(iconData)
				iconHash := hex.EncodeToString(hash[:])
				iconURL = fmt.Sprintf("%s/%s", client.ServerURL(), iconHash)
				uploads = append(uploads, uploadItem{
					data:       iconData,
					hash:       iconHash,
					mimeType:   detectImageMimeType(iconPath),
					authEvent:  nostr.BuildBlossomAuthEvent(iconHash, params.Pubkey, expiration),
					uploadType: "icon",
				})
			}
		}
		return iconURL, uploads
	}

	primary := params.primaryAssetInfo()
	if primary != nil && primary.Icon != nil {
		hash := sha256.Sum256(primary.Icon)
		iconHash := hex.EncodeToString(hash[:])
		iconURL = fmt.Sprintf("%s/%s", client.ServerURL(), iconHash)
		uploads = append(uploads, uploadItem{
			data:       primary.Icon,
			hash:       iconHash,
			mimeType:   "image/png",
			authEvent:  nostr.BuildBlossomAuthEvent(iconHash, params.Pubkey, expiration),
			uploadType: "icon",
		})
	}

	return iconURL, uploads
}

// collectImageUploads collects image upload data for batch signing.
func collectImageUploads(ctx context.Context, params UploadParams, expiration time.Time) ([]string, []uploadItem) {
	var imageURLs []string
	var uploads []uploadItem
	client := params.Client

	// Pre-downloaded images
	if params.PreDownloaded != nil && len(params.PreDownloaded.Images) > 0 {
		for _, img := range params.PreDownloaded.Images {
			imageURLs = append(imageURLs, fmt.Sprintf("%s/%s", client.ServerURL(), img.Hash))
			uploads = append(uploads, uploadItem{
				data:       img.Data,
				hash:       img.Hash,
				mimeType:   img.MimeType,
				authEvent:  nostr.BuildBlossomAuthEvent(img.Hash, params.Pubkey, expiration),
				uploadType: "screenshot",
			})
		}
	}

	// Config images not pre-downloaded
	for _, img := range params.Cfg.Images {
		if isRemoteURL(img) && params.PreDownloaded != nil && findPreDownloadedImage(params.PreDownloaded.Images, img) != nil {
			continue
		}

		if isRemoteURL(img) {
			if isBlossomURL(img, client.ServerURL()) {
				imageURLs = append(imageURLs, img)
				continue
			}
			imgData, imgHash, mimeType, err := downloadRemoteImage(ctx, img)
			if err == nil {
				imageURLs = append(imageURLs, fmt.Sprintf("%s/%s", client.ServerURL(), imgHash))
				uploads = append(uploads, uploadItem{
					data:       imgData,
					hash:       imgHash,
					mimeType:   mimeType,
					authEvent:  nostr.BuildBlossomAuthEvent(imgHash, params.Pubkey, expiration),
					uploadType: "screenshot",
				})
			}
		} else {
			imgPath := resolvePath(img, params.Cfg.BaseDir)
			imgData, err := os.ReadFile(imgPath)
			if err == nil {
				hash := sha256.Sum256(imgData)
				imgHash := hex.EncodeToString(hash[:])
				imageURLs = append(imageURLs, fmt.Sprintf("%s/%s", client.ServerURL(), imgHash))
				uploads = append(uploads, uploadItem{
					data:       imgData,
					hash:       imgHash,
					mimeType:   detectImageMimeType(imgPath),
					authEvent:  nostr.BuildBlossomAuthEvent(imgHash, params.Pubkey, expiration),
					uploadType: "image",
				})
			}
		}
	}

	return imageURLs, uploads
}

// checkUploadsExist checks which uploads already exist on the server.
func checkUploadsExist(ctx context.Context, client *blossom.Client, uploads []uploadItem, opts *cli.Options) map[string]bool {
	var nonAPKHashes []string
	for _, u := range uploads {
		if !u.isAPK {
			nonAPKHashes = append(nonAPKHashes, u.hash)
		}
	}

	if len(nonAPKHashes) == 0 {
		return nil
	}

	var spinner *ui.Spinner
	if opts.Publish.ShouldShowSpinners() {
		spinner = ui.NewStatusSpinner("Checking", fmt.Sprintf("%d files...", len(nonAPKHashes)))
		spinner.Start()
	}

	existsMap := client.ExistsBatch(ctx, nonAPKHashes, 4)

	if spinner != nil {
		existCount := 0
		for _, exists := range existsMap {
			if exists {
				existCount++
			}
		}
		if existCount > 0 {
spinner.Done("Checked", fmt.Sprintf("files (%d already exist)", existCount))
			} else {
				spinner.Done("Checked", "files")
		}
	}

	return existsMap
}

// performUploads performs the actual uploads after batch signing.
func performUploads(ctx context.Context, client *blossom.Client, uploads []uploadItem, existsMap map[string]bool, opts *cli.Options) error {
	for _, u := range uploads {
		if u.isAPK {
			var tracker *ui.DownloadTracker
			var callback func(uploaded, total int64)
			if opts.Publish.ShouldShowSpinners() {
				fileInfo, _ := os.Stat(u.apkPath)
				var size int64
				if fileInfo != nil {
					size = fileInfo.Size()
				}
				tracker = ui.NewDownloadTracker(fmt.Sprintf("Uploading APK to %s", client.ServerURL()), size)
				callback = tracker.Callback()
			}

			result, err := client.UploadWithAuth(ctx, u.apkPath, u.hash, u.authEvent, callback)
			if err != nil {
				return fmt.Errorf("failed to upload APK: %w", err)
			}

			if tracker != nil {
				if result.Existed {
					tracker.DoneWithMessage(fmt.Sprintf("APK already exists (%s)", result.URL))
				} else {
					tracker.Done()
				}
			}
		} else {
			existed := existsMap[u.hash]
			if existed {
				if opts.Publish.ShouldShowSpinners() {
					ui.Status("Exists", fmt.Sprintf("%s (%s/%s)", u.uploadType, client.ServerURL(), u.hash))
				}
			} else {
				var spinner *ui.Spinner
				if opts.Publish.ShouldShowSpinners() {
					spinner = ui.NewStatusSpinner("Uploading", u.uploadType+"...")
					spinner.Start()
				}

				_, err := client.UploadBytesWithAuthPreChecked(ctx, u.data, u.hash, u.mimeType, u.authEvent, false)
				if err != nil {
					if spinner != nil {
						spinner.Fail("Error", fmt.Sprintf("Failed to upload %s", u.uploadType))
					}
					return fmt.Errorf("failed to upload file: %w", err)
				}

				if spinner != nil {
					spinner.Done("Uploaded", u.uploadType)
				}
			}
		}
	}

	return nil
}

// Helper functions

func isRemoteURL(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
}

func hasRemoteImages(images []string) bool {
	for _, img := range images {
		if isRemoteURL(img) {
			return true
		}
	}
	return false
}

func countRemoteImages(images []string) int {
	count := 0
	for _, img := range images {
		if isRemoteURL(img) {
			count++
		}
	}
	return count
}

func findPreDownloadedImage(images []*DownloadedImage, url string) *DownloadedImage {
	for _, img := range images {
		if img.URL == url {
			return img
		}
	}
	return nil
}

func isBlossomURL(url, blossomServer string) bool {
	return strings.HasPrefix(url, blossomServer)
}

func resolvePath(path, baseDir string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if baseDir != "" {
		return filepath.Join(baseDir, path)
	}
	return path
}

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

// maxImageDownloadSize is the maximum size for remote image downloads (20MB)
const maxImageDownloadSize = 20 * 1024 * 1024

func downloadRemoteImage(ctx context.Context, url string) (data []byte, hashStr string, mimeType string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("download failed with status %d: %s", resp.StatusCode, url)
	}

	// Security: Check Content-Length header before reading
	if resp.ContentLength > maxImageDownloadSize {
		return nil, "", "", fmt.Errorf("image too large: %d bytes (max %d)", resp.ContentLength, maxImageDownloadSize)
	}

	// Security: Limit read size to prevent memory exhaustion
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxImageDownloadSize+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to read response: %w", err)
	}

	// Check if we hit the limit (read more than allowed)
	if len(data) > maxImageDownloadSize {
		return nil, "", "", fmt.Errorf("image too large: exceeds %d bytes", maxImageDownloadSize)
	}

	hash := sha256.Sum256(data)
	hashStr = hex.EncodeToString(hash[:])

	mimeType = resp.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = detectMimeTypeFromData(data)
	}

	return data, hashStr, mimeType, nil
}

func detectMimeTypeFromData(data []byte) string {
	if len(data) < 8 {
		return "application/octet-stream"
	}

	// PNG
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "image/png"
	}

	// JPEG
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}

	// GIF
	if string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a" {
		return "image/gif"
	}

	// WebP
	if string(data[:4]) == "RIFF" && len(data) >= 12 && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}

	return "application/octet-stream"
}
