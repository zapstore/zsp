package zsp

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/blossom"
	internalconfig "github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/identity"
	internalnostr "github.com/zapstore/zsp/internal/nostr"
)

// Publish uploads one verified APK and publishes its NIP-82 events.
func Publish(ctx context.Context, config PublishConfig, candidate *APK, options PublishOptions) (*PublishResult, error) {
	path, open := candidate.beginPublish()
	if !open {
		return nil, operationErr(ErrAPKNotChecked, false, "APK was not returned by Fetch")
	}
	verified := candidate.verified
	published := false
	var publicationResult *PublishResult
	defer func() {
		if !published {
			candidate.clearSourceCache()
		}
		if err := candidate.finishPublish(published); err != nil && publicationResult != nil {
			publicationResult.Warnings = append(publicationResult.Warnings, "temporary APK cleanup failed")
		}
	}()
	if verified.hash == "" {
		return nil, operationErr(ErrAPKNotChecked, false, "APK was not returned by Fetch")
	}
	if err := ctx.Err(); err != nil {
		return nil, contextOperationError(err, "publish")
	}
	if err := validatePublishInput(config, options); err != nil {
		return nil, err
	}
	blossomURL, relayURLs, err := effectivePublishTargets(options)
	if err != nil {
		return nil, err
	}
	parsed, err := apk.Parse(path)
	if err != nil {
		return nil, wrapOperationError(ErrInvalidAPK, err, false, "verify APK")
	}
	if parsed.SHA256 != verified.hash {
		return nil, operationErr(ErrInvalidAPK, false, "APK changed after Fetch")
	}
	if parsed.PackageID != verified.appID || parsed.VersionCode != verified.versionCode ||
		parsed.CertFingerprint != verified.certificateHash {
		return nil, operationErr(ErrInvalidAPK, false, "APK facts changed after Fetch")
	}
	signWith := internalconfig.GetSignWith()
	if signWith == "" {
		return nil, operationErr(ErrSigner, false, "SIGN_WITH is required")
	}
	signer, err := internalnostr.NewSigner(ctx, signWith)
	if err != nil {
		if contextErr := contextOperationError(err, "create signer"); contextErr != nil {
			return nil, contextErr
		}
		sentinel, retryable := networkErrorClassification(err)
		if errors.Is(sentinel, ErrRateLimited) || errors.Is(sentinel, ErrTemporaryFailure) {
			return nil, wrapOperationError(sentinel, err, retryable, "create signer")
		}
		return nil, operationErr(ErrSigner, false, "create signer")
	}
	defer signer.Close()
	result := &PublishResult{
		AppID: verified.appID, CertificateHash: verified.certificateHash,
		LineageHashes: append([]string(nil), verified.lineageHashes...),
	}
	publicationResult = result
	publisher := internalnostr.NewPublisher(relayURLs)
	unreachable, err := publisher.EnsureReachable(ctx)
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "reach relay"); contextErr != nil {
			return nil, contextErr
		}
		sentinel, retryable := relayQueryErrorClassification(err)
		return nil, wrapOperationError(sentinel, err, retryable, "reach relay")
	}
	for _, relayURL := range unreachable {
		result.Warnings = append(result.Warnings, "couldn't reach relay "+publicRelayURL(relayURL))
	}
	publishChannel := selectedChannel(options, config)
	var existingReleaseTimestamp time.Time
	c1Status := "not checked"
	authorizedPublishers := map[string]struct{}{signer.PublicKey(): {}}
	if !options.SkipProofCheck {
		reportPublish(options, "publish", "proof", "C1 ownership proof", 0, 0)
		proofs, proofWarnings, proofErr := publisher.FetchIdentityProofsByCertificate(ctx, parsed.CertFingerprint)
		if proofErr != nil {
			if contextErr := contextOperationError(ctx.Err(), "check C1 proof"); contextErr != nil {
				return nil, contextErr
			}
			sentinel, retryable := relayQueryErrorClassification(proofErr)
			return nil, wrapOperationError(sentinel, proofErr, retryable, "check C1 proof")
		}
		result.Warnings = append(result.Warnings, safeRelayQueryWarnings("C1 proof", proofWarnings)...)
		certificate, certificateErr := apk.ExtractCertificate(path)
		if certificateErr != nil {
			return nil, wrapOperationError(ErrInvalidAPK, certificateErr, false, "extract signing certificate")
		}
		authorizedPublishers = activeC1Publishers(proofs, parsed.CertFingerprint, certificate)
		if len(authorizedPublishers) == 0 {
			return nil, operationErr(ErrProofRequired, false, "no active proof found for certificate %s", parsed.CertFingerprint)
		}
		if _, authorized := authorizedPublishers[signer.PublicKey()]; !authorized {
			return nil, operationErr(ErrProofUnauthorized, false, "signer is not the active proof owner or delegate")
		}
		c1Status = "active"
		var publishedVersionCode int64
		reportPublish(options, "publish", "release", "published releases", 0, 0)
		versionCode, publishedAt, warnings, checkErr := publisher.HighestReleaseVersionCode(
			ctx, authorizedPublishers, parsed.PackageID, parsed.CertFingerprint, publishChannel,
		)
		if checkErr != nil {
			if contextErr := contextOperationError(ctx.Err(), "check published releases"); contextErr != nil {
				return nil, contextErr
			}
			sentinel, retryable := relayQueryErrorClassification(checkErr)
			return nil, wrapOperationError(sentinel, checkErr, retryable, "check published releases")
		}
		result.Warnings = append(result.Warnings, safeRelayQueryWarnings("published release", warnings)...)
		publishedVersionCode = versionCode
		existingReleaseTimestamp = publishedAt
		if parsed.VersionCode < publishedVersionCode {
			return nil, operationErr(ErrReleaseDowngrade, false, "release downgrade from %d to %d", publishedVersionCode, parsed.VersionCode)
		}
		if parsed.VersionCode == publishedVersionCode && !options.OverwriteRelease {
			return nil, operationCodeErr(ErrAlreadyPublished, "release_already_published", false, "version_code %d is already published", parsed.VersionCode)
		}
		if !options.OverwriteRelease || parsed.VersionCode != publishedVersionCode {
			existingReleaseTimestamp = time.Time{}
		}
	}

	reportPublish(options, "publish", "metadata", verified.appID, 0, 0)
	preparedConfig, eventConfig, metadataWarnings, err := preparePublishMetadata(ctx, config, candidate, parsed)
	if err != nil {
		return nil, err
	}
	result.Warnings = append(result.Warnings, metadataWarnings...)
	var media mediaPlan
	if !options.SkipAppEvent {
		media, err = prepareMedia(ctx, preparedConfig, parsed, blossomURL, !options.SkipMediaCompression)
		if err != nil {
			return nil, err
		}
	}
	releaseNotes, err := loadReleaseNotes(ctx, preparedConfig.ReleaseNotes)
	if err != nil {
		return nil, err
	}
	if releaseNotes == "" {
		releaseNotes = verified.releaseNotes
	}
	applicationPubkey := signer.PublicKey()
	if options.SkipAppEvent {
		appEvents, appWarnings, appErr := publisher.FetchApplicationEvents(ctx, parsed.PackageID, authorizedPublisherList(authorizedPublishers))
		if appErr != nil {
			if contextErr := contextOperationError(ctx.Err(), "find existing application event"); contextErr != nil {
				return nil, contextErr
			}
			sentinel, retryable := relayQueryErrorClassification(appErr)
			return nil, wrapOperationError(sentinel, appErr, retryable, "find existing application event")
		}
		result.Warnings = append(result.Warnings, safeRelayQueryWarnings("application event", appWarnings)...)
		if existing := newestAuthorizedApplication(appEvents, parsed.PackageID, authorizedPublishers); existing != nil {
			applicationPubkey = existing.PubKey
		}
	}
	events := internalnostr.BuildEventSet(internalnostr.BuildEventSetParams{
		APKInfo: parsed, Config: eventConfig, Pubkey: signer.PublicKey(), OriginalURL: verified.originalURL,
		IconURL: media.iconURL, ImageURLs: media.imageURLs, Changelog: releaseNotes,
		Channel: publishChannel,
		Commit:  options.Commit, ReleaseTimestamp: verified.releasedAt,
		MinReleaseTimestamp: existingReleaseTimestamp,
	})
	if options.SkipAppEvent {
		events.AppMetadata = nil
		events.SetApplicationPubkey(applicationPubkey)
	}
	internalnostr.FinalizeEventSet(events, firstRelay(relayURLs))
	if options.Preview {
		reportPublish(options, "publish", "preview", "Awaiting browser approval", 0, 0)
		previewData := internalnostr.BuildPreviewData(parsed, eventConfig, events, releaseNotes, blossomURL, publicRelayURLs(relayURLs))
		previewData.C1Status = c1Status
		previewData.IconURL = media.iconURL
		previewData.ImageURLs = append([]string(nil), media.imageURLs...)
		for _, blob := range media.blobs {
			if blob.label == "icon" {
				previewData.IconData = append([]byte(nil), blob.data...)
				continue
			}
			previewData.ImageData = append(previewData.ImageData, internalnostr.PreviewImageData{
				Data: append([]byte(nil), blob.data...), MimeType: blob.mimeType,
			})
		}
		preview := internalnostr.NewPreviewServer(previewData, releaseNotes, media.iconURL, options.BrowserPort)
		if _, err := preview.Start(); err != nil {
			return nil, wrapOperationError(ErrTemporaryFailure, err, true, "start preview")
		}
		approved, err := preview.WaitDecision(ctx)
		_ = preview.Close()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, contextOperationError(err, "wait for preview")
			}
			return nil, wrapOperationError(ErrTemporaryFailure, err, true, "wait for preview")
		}
		if !approved {
			return nil, operationErr(nil, false, "publication rejected in preview")
		}
	}
	if err := internalnostr.SignEventSet(ctx, signer, events); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, contextOperationError(err, "sign events")
		}
		return nil, wrapOperationError(ErrTemporaryFailure, err, true, "sign events")
	}
	if events.AppMetadata != nil {
		result.Events.Application = events.AppMetadata.ID
	}
	result.Events.Release = events.Release.ID
	for _, event := range events.SoftwareAssets {
		result.Events.Assets = append(result.Events.Assets, event.ID)
	}

	client := blossom.NewClient(blossomURL)
	for _, blob := range media.blobs {
		reportPublish(options, "publish", "upload", blob.label, 0, int64(len(blob.data)))
		upload, uploadErr := client.UploadBytes(ctx, blob.data, blob.hash, blob.mimeType, signer)
		if uploadErr != nil {
			result.Status = statusAfterExternal(result)
			result.Uploads = append(result.Uploads, BlobResult{
				URL: blob.url, Hash: blob.hash, Size: int64(len(blob.data)), Type: blob.mimeType,
				Accepted: false, Message: "upload failed",
			})
			sentinel, retryable := blossomErrorClassification(uploadErr)
			return result, wrapOperationError(sentinel, uploadErr, retryable, "upload "+blob.label)
		}
		if upload.URL != blob.url || upload.SHA256 != blob.hash || upload.Size != int64(len(blob.data)) || upload.Type != blob.mimeType {
			result.Status = statusAfterExternal(result)
			result.Uploads = append(result.Uploads, BlobResult{
				URL: upload.URL, Hash: upload.SHA256, Size: upload.Size, Type: upload.Type,
				Accepted: false, Message: "Blossom returned an unexpected descriptor",
			})
			return result, operationErr(ErrTemporaryFailure, true, "Blossom returned an invalid descriptor for %s", blob.label)
		}
		result.Uploads = append(result.Uploads, BlobResult{
			URL: upload.URL, Hash: upload.SHA256, Size: upload.Size, Type: upload.Type,
			Uploaded: upload.Size, Accepted: true,
		})
	}
	reportPublish(options, "publish", "upload", verified.filename, 0, verified.size)
	var uploadedBytes int64
	upload, err := client.Upload(ctx, path, verified.hash, signer, func(done, total int64) {
		uploadedBytes = done
		reportPublish(options, "publish", "upload", verified.filename, done, total)
	})
	if err != nil {
		result.Status = statusAfterExternal(result)
		result.Uploads = append(result.Uploads, BlobResult{
			URL: blossomURL + "/" + verified.hash + ".apk", Hash: verified.hash,
			Size: verified.size, Type: "application/vnd.android.package-archive",
			Uploaded: uploadedBytes, Accepted: false, Message: "upload failed",
		})
		sentinel, retryable := blossomErrorClassification(err)
		return result, wrapOperationError(sentinel, err, retryable, "upload APK")
	}
	expectedAPKURL := blossomURL + "/" + verified.hash + ".apk"
	if upload.URL != expectedAPKURL || upload.SHA256 != verified.hash || upload.Size != parsed.FileSize ||
		upload.Type != "application/vnd.android.package-archive" {
		result.Status = statusAfterExternal(result)
		result.Uploads = append(result.Uploads, BlobResult{
			URL: upload.URL, Hash: upload.SHA256, Size: upload.Size, Type: upload.Type,
			Accepted: false, Message: "Blossom returned an unexpected descriptor",
		})
		return result, operationErr(ErrTemporaryFailure, true, "Blossom returned an invalid APK descriptor")
	}
	result.Uploads = append(result.Uploads, BlobResult{
		URL: upload.URL, Hash: upload.SHA256, Size: upload.Size,
		Type: upload.Type, Uploaded: upload.Size, Accepted: true,
	})

	if events.AppMetadata != nil {
		reportPublish(options, "publish", "relay", "application", 0, 0)
		relayResults, accepted, publishErr := publishEvent(ctx, publisher, events.AppMetadata)
		result.Relays = append(result.Relays, relayResults...)
		if !accepted {
			result.Status = "partial"
			return result, publishErr
		}
		result.Warnings = append(result.Warnings, relayWarnings(relayResults)...)
	}
	for _, event := range events.SoftwareAssets {
		reportPublish(options, "publish", "relay", "asset", 0, 0)
		relayResults, accepted, publishErr := publishEvent(ctx, publisher, event)
		result.Relays = append(result.Relays, relayResults...)
		if !accepted {
			result.Status = "partial"
			return result, publishErr
		}
		result.Warnings = append(result.Warnings, relayWarnings(relayResults)...)
	}
	reportPublish(options, "publish", "relay", "release", 0, 0)
	relayResults, accepted, publishErr := publishEvent(ctx, publisher, events.Release)
	result.Relays = append(result.Relays, relayResults...)
	if !accepted {
		result.Status = "partial"
		return result, publishErr
	}
	result.Warnings = append(result.Warnings, relayWarnings(relayResults)...)
	result.ID = events.Release.ID
	result.Status = "published"
	published = true
	return result, nil
}

func blossomErrorClassification(err error) (error, bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err, false
	}
	if errors.Is(err, blossom.ErrInvalidAuthEvent) {
		return ErrInvalidConfig, false
	}
	var statusError *blossom.StatusError
	if errors.As(err, &statusError) {
		if statusError.StatusCode == http.StatusTooManyRequests {
			return ErrRateLimited, true
		}
		if statusError.StatusCode == http.StatusRequestTimeout ||
			statusError.StatusCode >= http.StatusInternalServerError {
			return ErrTemporaryFailure, true
		}
		return ErrUploadRejected, false
	}
	if errors.Is(err, blossom.ErrInvalidDescriptor) {
		return ErrTemporaryFailure, true
	}
	if errors.Is(err, blossom.ErrPreflightRejected) || errors.Is(err, blossom.ErrUploadRejected) {
		return ErrUploadRejected, false
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return ErrSourceFailed, false
	}
	return networkErrorClassification(err)
}

func networkErrorClassification(err error) (error, bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err, false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return ErrTemporaryFailure, true
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "rate limit") || strings.Contains(message, "status 429") {
		return ErrRateLimited, true
	}
	for _, fragment := range []string{"connection refused", "connection reset", "temporar", "timeout", "status 502", "status 503", "status 504"} {
		if strings.Contains(message, fragment) {
			return ErrTemporaryFailure, true
		}
	}
	return ErrSourceFailed, false
}

func relayQueryErrorClassification(err error) (error, bool) {
	if errors.Is(err, context.Canceled) {
		return context.Canceled, false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTemporaryFailure, true
	}
	sentinel, retryable := networkErrorClassification(err)
	if errors.Is(sentinel, ErrRateLimited) {
		return sentinel, retryable
	}
	return ErrTemporaryFailure, true
}

func safeRelayQueryWarnings(operation string, warnings []error) []string {
	result := make([]string, len(warnings))
	for index := range warnings {
		result[index] = operation + " query failed on one relay"
	}
	return result
}

func activeC1Publishers(proofs []*gonostr.Event, certHash string, certificate *x509.Certificate) map[string]struct{} {
	var activeProof *gonostr.Event
	for _, proof := range proofs {
		decodedProof, validationErr := identity.ValidateActiveProofEvent(proof, certificate, time.Now())
		if validationErr != nil || decodedProof.CertHash != certHash {
			continue
		}
		if activeProof == nil || proof.CreatedAt > activeProof.CreatedAt ||
			(proof.CreatedAt == activeProof.CreatedAt && proof.ID < activeProof.ID) {
			activeProof = proof
		}
	}
	if activeProof == nil {
		return nil
	}
	authorized := map[string]struct{}{activeProof.PubKey: {}}
	if delegation := activeProof.Tags.GetFirst([]string{"delegation"}); delegation != nil {
		authorized[(*delegation)[1]] = struct{}{}
	}
	return authorized
}

func newestAuthorizedApplication(events []*gonostr.Event, appID string, authorized map[string]struct{}) *gonostr.Event {
	var newest *gonostr.Event
	for _, event := range events {
		if event == nil || event.Kind != internalnostr.KindAppMetadata {
			continue
		}
		identifier, validIdentifier := singleNostrTagValue(event.Tags, "d")
		if !validIdentifier || identifier != appID {
			continue
		}
		if _, ok := authorized[event.PubKey]; !ok {
			continue
		}
		valid, err := event.CheckSignature()
		if err != nil || !valid {
			continue
		}
		if newest == nil || event.CreatedAt > newest.CreatedAt ||
			(event.CreatedAt == newest.CreatedAt && event.ID < newest.ID) {
			newest = event
		}
	}
	return newest
}

func singleNostrTagValue(tags gonostr.Tags, name string) (string, bool) {
	var value string
	found := false
	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != name {
			continue
		}
		if found || len(tag) != 2 || tag[1] == "" {
			return "", false
		}
		found = true
		value = tag[1]
	}
	return value, found
}

func authorizedPublisherList(publishers map[string]struct{}) []string {
	result := make([]string, 0, len(publishers))
	for publisher := range publishers {
		result = append(result, publisher)
	}
	return result
}

func statusAfterExternal(result *PublishResult) string {
	if len(result.Uploads) > 0 || len(result.Relays) > 0 {
		return "partial"
	}
	return "failed"
}

// effectivePublishTargets resolves every network destination before signer
// construction, including environment defaults.
func effectivePublishTargets(options PublishOptions) (string, []string, error) {
	blossomURL := strings.TrimRight(options.BlossomURL, "/")
	if blossomURL == "" {
		blossomURL = strings.TrimRight(internalconfig.GetEnv("BLOSSOM_URL"), "/")
	}
	if blossomURL == "" {
		blossomURL = blossom.DefaultServer
	}
	parsedURL, err := url.Parse(blossomURL)
	if err != nil || !allowedRemoteURL(parsedURL) || parsedURL.RawQuery != "" {
		return "", nil, operationErr(ErrInvalidConfig, false, "BLOSSOM_URL must use HTTPS outside loopback")
	}
	relayURLs := relays(options.Relays)
	if err := validateRelayURLs(relayURLs); err != nil {
		return "", nil, err
	}
	return blossomURL, relayURLs, nil
}

func relays(values []string) []string {
	if len(values) == 0 {
		value := internalconfig.GetEnv("RELAYS")
		if value == "" {
			return []string{internalnostr.DefaultRelay}
		}
		values = strings.Split(value, ",")
	}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	if len(cleaned) == 0 {
		return []string{internalnostr.DefaultRelay}
	}
	return cleaned
}

func firstRelay(values []string) string {
	configured := relays(values)
	if len(configured) == 0 {
		return internalnostr.DefaultRelay
	}
	if public := publicRelayURL(configured[0]); public != "" {
		return public
	}
	return internalnostr.DefaultRelay
}

func publishEvent(ctx context.Context, publisher *internalnostr.Publisher, event *gonostr.Event) ([]RelayResult, bool, error) {
	published := publisher.Publish(ctx, event)
	results := make([]RelayResult, 0, len(published))
	accepted := false
	for _, relay := range published {
		message := ""
		if relay.Error != nil {
			if relay.IsDuplicate {
				message = "event already exists"
			} else {
				message = "relay rejected event"
			}
		}
		results = append(results, RelayResult{
			RelayURL: publicRelayURL(relay.RelayURL), EventID: event.ID, Accepted: relay.Success,
			Duplicate: relay.IsDuplicate, Message: message,
		})
		accepted = accepted || relay.Success
	}
	if accepted {
		return results, true, nil
	}
	return results, false, relayPublicationError(ctx, published)
}

func publicRelayURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func publicRelayURLs(raw []string) []string {
	result := make([]string, 0, len(raw))
	for _, relayURL := range raw {
		if public := publicRelayURL(relayURL); public != "" {
			result = append(result, public)
		}
	}
	return result
}

func relayPublicationError(ctx context.Context, published []internalnostr.PublishResult) error {
	if err := ctx.Err(); err != nil {
		return operationCodeErr(err, "cancelled", false, "relay publication cancelled")
	}
	for _, relay := range published {
		sentinel, retryable := networkErrorClassification(relay.Error)
		if errors.Is(sentinel, ErrRateLimited) {
			return operationErr(ErrRateLimited, retryable, "relay publication was rate limited")
		}
	}
	for _, relay := range published {
		sentinel, retryable := networkErrorClassification(relay.Error)
		if errors.Is(sentinel, ErrTemporaryFailure) {
			return operationErr(ErrTemporaryFailure, retryable, "relay publication failed temporarily")
		}
	}
	return operationErr(ErrPublishRejected, false, "event rejected by all relays")
}

func relayWarnings(results []RelayResult) []string {
	var warnings []string
	for _, result := range results {
		if !result.Accepted {
			warnings = append(warnings, "event rejected by one relay")
		}
	}
	return warnings
}

func reportPublish(options PublishOptions, operation, phase, target string, completed, total int64) {
	if options.OnProgress != nil {
		options.OnProgress(Progress{Operation: operation, Phase: phase, Target: target, Completed: completed, Total: total})
	}
}
