package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/localpath"
	"github.com/zapstore/zsp/internal/media"
	"github.com/zapstore/zsp/internal/netpolicy"
	"github.com/zapstore/zsp/internal/sanitize"
	"github.com/zapstore/zsp/internal/source"
)

const maxMediaSize = 20 << 20
const maxReleaseNotesSize = 2 << 20

// mediaFetchTimeout is how long one remote icon or screenshot may take to
// download before it is skipped. The APK icon is already on disk.
var mediaFetchTimeout = 15 * time.Second

var errMediaTimeout = errors.New("media download timeout")

type preparedBlob struct {
	label    string
	data     []byte
	hash     string
	mimeType string
	url      string
}

type mediaPlan struct {
	iconURL   string
	imageURLs []string
	blobs     []preparedBlob
}

func prepareMedia(ctx context.Context, config PublishConfig, apkInfo *apk.APKInfo, blossomURL string, compress bool) (mediaPlan, []string, error) {
	var plan mediaPlan
	warnings, err := plan.addIcon(ctx, config, apkInfo, blossomURL, compress)
	if err != nil {
		return mediaPlan{}, nil, err
	}
	sets, explicit := config.imageSets, false
	if len(config.Images) > 0 {
		sets, explicit = [][]string{config.Images}, true
	}
	screenshotWarnings, err := plan.addScreenshots(ctx, config.baseDir, sets, explicit, blossomURL, compress)
	if err != nil {
		return mediaPlan{}, nil, err
	}
	return plan, append(warnings, screenshotWarnings...), nil
}

// addIcon uses an explicit icon when one is set. Otherwise it uses the APK
// icon, then each metadata icon location. A metadata icon that cannot be used
// is skipped. An explicit local path that cannot be read fails the publish.
func (plan *mediaPlan) addIcon(ctx context.Context, config PublishConfig, apkInfo *apk.APKInfo, blossomURL string, compress bool) ([]string, error) {
	if config.Icon != "" {
		blob, err := prepareMediaBlob(ctx, "icon", config.baseDir, config.Icon, media.IconMaxWidth, blossomURL, compress)
		if err != nil {
			return nil, err
		}
		plan.useIcon(blob)
		return nil, nil
	}

	var warnings []string
	if apkInfo != nil && len(apkInfo.Icon) > 0 {
		processed, err := media.Process(apkInfo.Icon, media.IconMaxWidth, compress)
		if err == nil {
			plan.useIcon(newPreparedBlob("icon", processed.Data, processed.Hash, processed.MimeType, blossomURL))
			return nil, nil
		}
		if contextErr := contextOperationError(ctx.Err(), "process APK icon"); contextErr != nil {
			return nil, contextErr
		}
		warnings = append(warnings, "skipped APK icon"+causeDetail(err))
	}
	for _, location := range config.iconCandidates {
		if err := ctx.Err(); err != nil {
			if contextErr := contextOperationError(err, "load icon"); contextErr != nil {
				return nil, contextErr
			}
		}
		blob, err := loadPreparedMedia(ctx, "icon", config.baseDir, location, media.IconMaxWidth, blossomURL, compress)
		if err == nil {
			plan.useIcon(blob)
			return warnings, nil
		}
		if contextErr := contextOperationError(ctx.Err(), "load icon"); contextErr != nil {
			return nil, contextErr
		}
		warnings = append(warnings, skippedScreenshotWarning("icon", location, err))
	}
	return warnings, nil
}

func (plan *mediaPlan) useIcon(blob preparedBlob) {
	plan.iconURL = blob.url
	plan.blobs = append(plan.blobs, blob)
}

// addScreenshots tries each set of screenshots in order. A remote screenshot
// that is missing, not an image, or too slow is skipped, and so is any
// screenshot a metadata source discovered. The first set that yields a usable
// screenshot is kept. An explicit local path that cannot be read fails the
// publish: the caller wrote that path.
func (plan *mediaPlan) addScreenshots(ctx context.Context, baseDir string, sets [][]string, explicit bool, blossomURL string, compress bool) ([]string, error) {
	var warnings []string
	for _, set := range sets {
		var blobs []preparedBlob
		var urls []string
		var setWarnings []string
		for index, location := range set {
			if err := ctx.Err(); err != nil {
				if contextErr := contextOperationError(err, "load image"); contextErr != nil {
					return nil, contextErr
				}
			}
			label := fmt.Sprintf("image %d", index+1)
			blob, err := loadPreparedMedia(ctx, label, baseDir, location, media.ScreenshotMaxWidth, blossomURL, compress)
			if err != nil {
				if contextErr := contextOperationError(ctx.Err(), "load "+label); contextErr != nil {
					return nil, contextErr
				}
				if explicit && !skippableScreenshot(location, err) {
					return nil, wrapMediaError(label, err)
				}
				setWarnings = append(setWarnings, skippedScreenshotWarning(label, location, err))
				continue
			}
			blobs = append(blobs, blob)
			urls = append(urls, blob.url)
		}
		warnings = append(warnings, setWarnings...)
		if len(blobs) > 0 {
			plan.imageURLs = urls
			plan.blobs = append(plan.blobs, blobs...)
			return warnings, nil
		}
	}
	return warnings, nil
}

func skippableScreenshot(location string, err error) bool {
	if strings.Contains(location, "://") {
		return true
	}
	return mediaProcessError(err)
}

func mediaProcessError(err error) bool {
	return errors.Is(err, media.ErrInvalidImage)
}

func skippedScreenshotWarning(label, location string, err error) string {
	target := location
	if strings.Contains(location, "://") {
		if sanitized := sanitize.URL(location); sanitized != "" {
			target = sanitized
		}
	} else {
		target = sanitize.Text(location)
	}
	return "skipped " + label + " " + target + causeDetail(err)
}

func wrapMediaError(label string, err error) error {
	if errors.Is(err, localpath.ErrRejected) {
		return wrapOperationError(ErrInvalidConfig, err, false, "load "+label+causeDetail(err))
	}
	if mediaProcessError(err) {
		return wrapOperationError(ErrInvalidConfig, err, false, "process "+label+causeDetail(err))
	}
	sentinel, retryable := sourceErrorClassification(err)
	return wrapOperationError(sentinel, err, retryable, "load "+label+causeDetail(err))
}

func prepareMediaBlob(ctx context.Context, label, baseDir, location string, maxWidth int, blossomURL string, compress bool) (preparedBlob, error) {
	blob, err := loadPreparedMedia(ctx, label, baseDir, location, maxWidth, blossomURL, compress)
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "load "+label); contextErr != nil {
			return preparedBlob{}, contextErr
		}
		return preparedBlob{}, wrapMediaError(label, err)
	}
	return blob, nil
}

func loadPreparedMedia(ctx context.Context, label, baseDir, location string, maxWidth int, blossomURL string, compress bool) (preparedBlob, error) {
	data, _, err := readMedia(ctx, baseDir, location)
	if err != nil {
		return preparedBlob{}, err
	}
	processed, err := media.Process(data, maxWidth, compress)
	if err != nil {
		return preparedBlob{}, err
	}
	return newPreparedBlob(label, processed.Data, processed.Hash, processed.MimeType, blossomURL), nil
}

func newPreparedBlob(label string, data []byte, hash, mimeType, blossomURL string) preparedBlob {
	return preparedBlob{
		label: label, data: data, hash: hash, mimeType: mimeType,
		url: strings.TrimRight(blossomURL, "/") + "/" + hash + normalizedExtension(mimeType),
	}
}

func readMedia(ctx context.Context, baseDir, location string) ([]byte, string, error) {
	if !strings.Contains(location, "://") {
		if !filepath.IsAbs(location) {
			return nil, "", fmt.Errorf("local media path must be absolute")
		}
		data, err := localpath.ReadFile(baseDir, location, maxMediaSize)
		if err != nil {
			return nil, "", err
		}
		return data, mime.TypeByExtension(strings.ToLower(filepath.Ext(location))), nil
	}
	parsed, err := url.Parse(location)
	if err != nil || !allowedRemoteURL(parsed) {
		return nil, "", fmt.Errorf("remote media URL must use HTTPS outside loopback")
	}
	fetchCtx, cancel := context.WithTimeout(ctx, mediaFetchTimeout)
	defer cancel()
	client := &http.Client{
		Transport:     netpolicy.NewTransport(),
		CheckRedirect: netpolicy.CheckRedirect,
	}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	response, err := source.DoWithTorFallback(fetchCtx, client, request)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return nil, "", errMediaTimeout
		}
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("media returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxMediaSize+1))
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return nil, "", errMediaTimeout
		}
		return nil, "", err
	}
	if len(data) > maxMediaSize {
		return nil, "", fmt.Errorf("media exceeds %d bytes", maxMediaSize)
	}
	return data, response.Header.Get("Content-Type"), nil
}

func loadReleaseNotes(ctx context.Context, baseDir, location string) (string, error) {
	if location == "" {
		return "", nil
	}
	if !strings.Contains(location, "://") {
		if !filepath.IsAbs(location) {
			return "", operationErr(ErrInvalidConfig, false, "local release notes path must be absolute")
		}
		data, err := localpath.ReadFile(baseDir, location, maxReleaseNotesSize)
		if errors.Is(err, localpath.ErrTooLarge) {
			return "", operationErr(ErrInvalidConfig, false, "release notes exceed %d bytes", maxReleaseNotesSize)
		}
		if err != nil {
			return "", wrapOperationError(ErrInvalidConfig, err, false, "read release notes"+causeDetail(err))
		}
		return string(data), nil
	}
	parsed, err := url.Parse(location)
	if err != nil || !allowedRemoteURL(parsed) {
		return "", operationErr(ErrInvalidConfig, false, "release notes URL must use HTTPS outside loopback")
	}
	client := &http.Client{
		Timeout:       30 * time.Second,
		Transport:     netpolicy.NewTransport(),
		CheckRedirect: netpolicy.CheckRedirect,
	}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", wrapOperationError(ErrInvalidConfig, err, false, "create release notes request"+causeDetail(err))
	}
	response, err := source.DoWithTorFallback(ctx, client, request)
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "fetch release notes"); contextErr != nil {
			return "", contextErr
		}
		sentinel, retryable := sourceErrorClassification(err)
		return "", wrapOperationError(sentinel, err, retryable, "fetch release notes"+causeDetail(err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusTooManyRequests {
			return "", operationErr(ErrRateLimited, true, "release notes returned status %d", response.StatusCode)
		}
		if response.StatusCode >= 500 {
			return "", operationErr(ErrTemporaryFailure, true, "release notes returned status %d", response.StatusCode)
		}
		return "", operationErr(ErrSourceFailed, false, "release notes returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseNotesSize+1))
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "read release notes"); contextErr != nil {
			return "", contextErr
		}
		return "", wrapOperationError(ErrTemporaryFailure, err, true, "read release notes"+causeDetail(err))
	}
	if len(data) > maxReleaseNotesSize {
		return "", operationErr(ErrInvalidConfig, false, "release notes exceed %d bytes", maxReleaseNotesSize)
	}
	return string(data), nil
}

func allowedRemoteURL(value *url.URL) bool {
	return value != nil && netpolicy.ValidateParsedURL(value) == nil
}

func normalizedExtension(mimeType string) string {
	switch strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/svg+xml":
		return ".svg"
	case "application/vnd.android.package-archive":
		return ".apk"
	default:
		return ""
	}
}
