package zsp

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/media"
	"github.com/zapstore/zsp/internal/source"
)

const maxMediaSize = 20 << 20
const maxReleaseNotesSize = 2 << 20

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

func prepareMedia(ctx context.Context, config PublishConfig, apkInfo *apk.APKInfo, blossomURL string, compress bool) (mediaPlan, error) {
	var plan mediaPlan
	if config.Icon != "" {
		blob, err := prepareMediaBlob(ctx, "icon", config.Icon, media.IconMaxWidth, blossomURL, compress)
		if err != nil {
			return mediaPlan{}, err
		}
		plan.iconURL = blob.url
		plan.blobs = append(plan.blobs, blob)
	} else if len(apkInfo.Icon) > 0 {
		processed, err := media.Process(apkInfo.Icon, "image/png", media.IconMaxWidth, compress)
		if err != nil {
			return mediaPlan{}, wrapOperationError(ErrInvalidConfig, err, false, "process APK icon")
		}
		blob := newPreparedBlob("icon", processed.Data, processed.Hash, processed.MimeType, blossomURL)
		plan.iconURL = blob.url
		plan.blobs = append(plan.blobs, blob)
	}
	for index, image := range config.Images {
		blob, err := prepareMediaBlob(ctx, fmt.Sprintf("image %d", index+1), image, media.ScreenshotMaxWidth, blossomURL, compress)
		if err != nil {
			return mediaPlan{}, err
		}
		plan.imageURLs = append(plan.imageURLs, blob.url)
		plan.blobs = append(plan.blobs, blob)
	}
	return plan, nil
}

func prepareMediaBlob(ctx context.Context, label, location string, maxWidth int, blossomURL string, compress bool) (preparedBlob, error) {
	data, contentType, err := readMedia(ctx, location)
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "load "+label); contextErr != nil {
			return preparedBlob{}, contextErr
		}
		sentinel, retryable := sourceErrorClassification(err)
		return preparedBlob{}, wrapOperationError(sentinel, err, retryable, "load "+label)
	}
	processed, err := media.Process(data, contentType, maxWidth, compress)
	if err != nil {
		return preparedBlob{}, wrapOperationError(ErrInvalidConfig, err, false, "process "+label)
	}
	return newPreparedBlob(label, processed.Data, processed.Hash, processed.MimeType, blossomURL), nil
}

func newPreparedBlob(label string, data []byte, hash, mimeType, blossomURL string) preparedBlob {
	return preparedBlob{
		label: label, data: data, hash: hash, mimeType: mimeType,
		url: strings.TrimRight(blossomURL, "/") + "/" + hash + normalizedExtension(mimeType),
	}
}

func readMedia(ctx context.Context, location string) ([]byte, string, error) {
	if !strings.Contains(location, "://") {
		if !filepath.IsAbs(location) {
			return nil, "", fmt.Errorf("local media path must be absolute")
		}
		file, err := os.Open(location)
		if err != nil {
			return nil, "", err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxMediaSize+1))
		if err != nil {
			return nil, "", err
		}
		if len(data) > maxMediaSize {
			return nil, "", fmt.Errorf("media exceeds %d bytes", maxMediaSize)
		}
		return data, mime.TypeByExtension(strings.ToLower(filepath.Ext(location))), nil
	}
	parsed, err := url.Parse(location)
	if err != nil || !allowedRemoteURL(parsed) {
		return nil, "", fmt.Errorf("remote media URL must use HTTPS outside loopback")
	}
	client := &http.Client{
		Timeout: 45 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 || !allowedRemoteURL(request.URL) {
				return fmt.Errorf("unsafe media redirect")
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	response, err := source.DoWithTorFallback(ctx, client, request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("media returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxMediaSize+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxMediaSize {
		return nil, "", fmt.Errorf("media exceeds %d bytes", maxMediaSize)
	}
	return data, response.Header.Get("Content-Type"), nil
}

func loadReleaseNotes(ctx context.Context, location string) (string, error) {
	if location == "" {
		return "", nil
	}
	if !strings.Contains(location, "://") {
		if !filepath.IsAbs(location) {
			return "", operationErr(ErrInvalidConfig, false, "local release notes path must be absolute")
		}
		file, err := os.Open(location)
		if err != nil {
			return "", wrapOperationError(ErrInvalidConfig, err, false, "read release notes")
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxReleaseNotesSize+1))
		if err != nil {
			return "", wrapOperationError(ErrInvalidConfig, err, false, "read release notes")
		}
		if len(data) > maxReleaseNotesSize {
			return "", operationErr(ErrInvalidConfig, false, "release notes exceed %d bytes", maxReleaseNotesSize)
		}
		return string(data), nil
	}
	parsed, err := url.Parse(location)
	if err != nil || !allowedRemoteURL(parsed) {
		return "", operationErr(ErrInvalidConfig, false, "release notes URL must use HTTPS outside loopback")
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 || !allowedRemoteURL(request.URL) {
				return fmt.Errorf("unsafe release notes redirect")
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", wrapOperationError(ErrInvalidConfig, err, false, "create release notes request")
	}
	response, err := source.DoWithTorFallback(ctx, client, request)
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "fetch release notes"); contextErr != nil {
			return "", contextErr
		}
		sentinel, retryable := sourceErrorClassification(err)
		return "", wrapOperationError(sentinel, err, retryable, "fetch release notes")
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
		return "", wrapOperationError(ErrTemporaryFailure, err, true, "read release notes")
	}
	if len(data) > maxReleaseNotesSize {
		return "", operationErr(ErrInvalidConfig, false, "release notes exceed %d bytes", maxReleaseNotesSize)
	}
	return string(data), nil
}

func allowedRemoteURL(value *url.URL) bool {
	if value == nil || value.Hostname() == "" || value.User != nil || value.Fragment != "" {
		return false
	}
	if value.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(value.Hostname())
	return value.Scheme == "http" && (value.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
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
