package release

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/blossom"
	internalconfig "github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/identity"
	internalnostr "github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/sanitize"
	"github.com/zapstore/zsp/internal/source"
)

// Publish uploads one verified APK and publishes its NIP-82 events.
func Publish(ctx context.Context, config PublishConfig, candidate *APK, options PublishOptions) (result *PublishResult, err error) {
	path, open := candidate.beginPublish()
	if !open {
		return nil, operationErr(ErrAPKNotChecked, false, "APK was not returned by Fetch")
	}
	verified := candidate.verified
	published := false
	defer func() {
		if copyErr := candidate.retainPublished(published, err); copyErr != nil {
			if published && result != nil {
				result.Warnings = append(result.Warnings, "APK cache copy failed")
			} else {
				err = operationErr(nil, false, "copy APK to cache%s", causeDetail(copyErr))
			}
		}
		if shouldClearHTTPCache(err) {
			candidate.clearHTTPCache()
		}
		if cleanupErr := candidate.finishPublish(published); cleanupErr != nil && result != nil {
			result.Warnings = append(result.Warnings, "temporary APK cleanup failed")
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
	parsed, err := apk.Parse(ctx, path)
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "verify APK"); contextErr != nil {
			return nil, contextErr
		}
		return nil, wrapOperationError(ErrInvalidAPK, err, false, "verify APK"+causeDetail(err))
	}
	if parsed.SHA256 != verified.hash {
		return nil, operationErr(ErrInvalidAPK, false, "APK changed after Fetch")
	}
	if parsed.PackageID != verified.appID || parsed.VersionCode != verified.versionCode ||
		parsed.CertFingerprint != verified.certificateHash {
		return nil, operationErr(ErrInvalidAPK, false, "APK facts changed after Fetch")
	}
	signWith, err := internalconfig.GetSignWith()
	if err != nil {
		return nil, wrapOperationError(ErrInvalidConfig, err, false, "read SIGN_WITH"+causeDetail(err))
	}
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
			return nil, wrapOperationError(sentinel, err, retryable, "create signer"+causeDetail(err))
		}
		return nil, operationErr(ErrSigner, false, "%s", "create signer"+causeDetail(err))
	}
	defer signer.Close()
	result = &PublishResult{
		AppID: verified.appID, CertificateHash: verified.certificateHash,
		LineageHashes: append([]string(nil), verified.lineageHashes...),
	}
	publisher := internalnostr.NewPublisher(relayURLs)
	unreachable, err := publisher.EnsureReachable(ctx)
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "reach relay"); contextErr != nil {
			return nil, contextErr
		}
		sentinel, retryable := relayQueryErrorClassification(err)
		return nil, wrapOperationError(sentinel, err, retryable, "reach relay"+causeDetail(err))
	}
	for _, relayURL := range unreachable {
		result.Warnings = append(result.Warnings, "couldn't reach relay "+publicRelayURL(relayURL))
	}
	publishChannel := selectedChannel(options, config)
	var existingReleaseTimestamp time.Time
	c1Status := "not checked"
	authorizedPublishers := map[string]struct{}{signer.PublicKey(): {}}
	var pendingProof bool
	var previousProof *gonostr.Event
	var proofCertificate *x509.Certificate
	if !options.SkipProofCheck {
		reportPublish(options, "publish", "proof", "C1 ownership proof", 0, 0)
		proofs, proofWarnings, proofErr := publisher.FetchIdentityProofs(ctx, []string{parsed.CertificateHashes()[0]})
		if proofErr != nil {
			if contextErr := contextOperationError(ctx.Err(), "check C1 proof"); contextErr != nil {
				return nil, contextErr
			}
			sentinel, retryable := relayQueryErrorClassification(proofErr)
			return nil, wrapOperationError(sentinel, proofErr, retryable, "check C1 proof"+causeDetail(proofErr))
		}
		result.Warnings = append(result.Warnings, safeRelayQueryWarnings("C1 proof", proofWarnings)...)
		certificate := parsed.Certificate
		active := activeC1Proof(proofs, certificate, time.Now())
		c1Status = "active"
		authorizedPublishers = c1Publishers(active)
		if options.ProofSigner != nil && c1ProofNeedsRenewal(active, signer.PublicKey(), time.Now()) {
			c1Status = "created"
			if active != nil {
				c1Status = "renewed"
			}
			pendingProof, previousProof, proofCertificate = true, active, certificate
			authorizedPublishers = pendingC1Publishers(signer.PublicKey(), active)
		}
		if len(authorizedPublishers) == 0 {
			return nil, operationErr(ErrProofRequired, false, "no active proof found for certificate %s", parsed.CertFingerprint)
		}
		if _, authorized := authorizedPublishers[signer.PublicKey()]; !authorized {
			return nil, operationErr(ErrProofUnauthorized, false, "signer is not the active proof owner or delegate")
		}
		var publishedVersionCode int64
		reportPublish(options, "publish", "release", "published releases", 0, 0)
		versionCode, publishedAt, warnings, checkErr := publisher.HighestReleaseVersionCode(
			ctx, authorizedPublishers, parsed.PackageID, parsed.CertificateHashes(), publishChannel,
		)
		if checkErr != nil {
			if contextErr := contextOperationError(ctx.Err(), "check published releases"); contextErr != nil {
				return nil, contextErr
			}
			sentinel, retryable := relayQueryErrorClassification(checkErr)
			return nil, wrapOperationError(sentinel, checkErr, retryable, "check published releases"+causeDetail(checkErr))
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
		var mediaWarnings []string
		media, mediaWarnings, err = prepareMedia(ctx, preparedConfig, parsed, blossomURL, !options.SkipMediaCompression)
		if err != nil {
			return nil, err
		}
		result.Warnings = append(result.Warnings, mediaWarnings...)
	}
	releaseNotes, notesWarning, err := loadReleaseNotes(ctx, preparedConfig.baseDir, preparedConfig.ReleaseNotes, parsed.VersionName, verified.releaseVersion)
	if err != nil {
		return nil, err
	}
	if notesWarning != "" {
		result.Warnings = append(result.Warnings, notesWarning)
	} else if releaseNotes == "" {
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
			return nil, wrapOperationError(sentinel, appErr, retryable, "find existing application event"+causeDetail(appErr))
		}
		result.Warnings = append(result.Warnings, safeRelayQueryWarnings("application event", appWarnings)...)
		if existing := newestAuthorizedApplication(appEvents, parsed.PackageID, authorizedPublishers); existing != nil {
			applicationPubkey = existing.PubKey
		}
	}
	events := internalnostr.BuildEventSet(internalnostr.BuildEventSetParams{
		APKInfo: parsed, Config: eventConfig, Pubkey: signer.PublicKey(), OriginalURL: verified.originalURL,
		BlossomURL: blossomURL + "/" + verified.hash + ".apk",
		IconURL:    media.iconURL, ImageURLs: media.imageURLs, Changelog: releaseNotes,
		Channel: publishChannel,
		Commit:  options.Commit, ReleaseVersion: verified.releaseVersion, ReleaseTimestamp: verified.releasedAt,
		AppCreatedAtRelease: options.AppCreatedAtRelease, MinReleaseTimestamp: existingReleaseTimestamp,
	})
	if options.SkipAppEvent {
		events.AppMetadata = nil
		events.SetApplicationPubkey(applicationPubkey)
	} else if !options.OverwriteAppEvent {
		appEvents, appWarnings, appErr := publisher.FetchApplicationEvents(ctx, parsed.PackageID, []string{signer.PublicKey()})
		if appErr != nil {
			if contextErr := contextOperationError(ctx.Err(), "find existing application event"); contextErr != nil {
				return nil, contextErr
			}
			result.Warnings = append(result.Warnings, safeRelayQueryWarnings("application event", appWarnings)...)
			result.Warnings = append(result.Warnings, "couldn't verify existing application event")
		} else {
			result.Warnings = append(result.Warnings, safeRelayQueryWarnings("application event", appWarnings)...)
			existing := newestAuthorizedApplication(appEvents, parsed.PackageID, map[string]struct{}{signer.PublicKey(): {}})
			if internalnostr.SameAppMetadata(existing, events.AppMetadata) {
				events.AppMetadata = nil
				media.blobs = nil
				result.Warnings = append(result.Warnings, "application event unchanged")
			}
		}
	}
	internalnostr.FinalizeEventSet(events, firstRelay(relayURLs))
	if options.Preview {
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
		previewURL, err := preview.Start()
		if err != nil {
			return nil, wrapOperationError(ErrTemporaryFailure, err, true, "start preview"+causeDetail(err))
		}
		reportPublish(options, "publish", "preview", previewURL, 0, 0)
		approved, err := preview.WaitDecision(ctx)
		_ = preview.Close()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, contextOperationError(err, "wait for preview")
			}
			return nil, wrapOperationError(ErrTemporaryFailure, err, true, "wait for preview"+causeDetail(err))
		}
		if !approved {
			return nil, operationErr(nil, false, "publication rejected in preview")
		}
	}
	if err := internalnostr.SignEventSet(ctx, signer, events); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, contextOperationError(err, "sign events")
		}
		return nil, wrapOperationError(ErrTemporaryFailure, err, true, "sign events"+causeDetail(err))
	}
	var proofEvent *gonostr.Event
	if pendingProof {
		reportPublish(options, "publish", "proof", "signing C1 proof", 0, 0)
		proofEvent, err = buildC1Proof(ctx, options.ProofSigner, signer, proofCertificate, previousProof, time.Now())
		if err != nil {
			return nil, err
		}
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
			return result, wrapOperationError(sentinel, uploadErr, retryable, "upload "+blob.label+uploadFailureDetail(uploadErr))
		}
		if mismatch := descriptorMismatch(upload, blob.url, blob.hash, int64(len(blob.data)), blob.mimeType); mismatch != "" {
			result.Status = statusAfterExternal(result)
			result.Uploads = append(result.Uploads, BlobResult{
				URL: sanitize.URL(upload.URL), Hash: upload.SHA256, Size: upload.Size, Type: upload.Type,
				Accepted: false, Message: "Blossom returned an unexpected descriptor",
			})
			return result, operationErr(ErrTemporaryFailure, true, "Blossom returned an invalid descriptor for %s%s", blob.label, mismatch)
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
		return result, wrapOperationError(sentinel, err, retryable, "upload APK"+uploadFailureDetail(err))
	}
	expectedAPKURL := blossomURL + "/" + verified.hash + ".apk"
	if mismatch := descriptorMismatch(upload, expectedAPKURL, verified.hash, parsed.FileSize,
		"application/vnd.android.package-archive"); mismatch != "" {
		result.Status = statusAfterExternal(result)
		result.Uploads = append(result.Uploads, BlobResult{
			URL: sanitize.URL(upload.URL), Hash: upload.SHA256, Size: upload.Size, Type: upload.Type,
			Accepted: false, Message: "Blossom returned an unexpected descriptor",
		})
		return result, operationErr(ErrTemporaryFailure, true, "Blossom returned an invalid APK descriptor%s", mismatch)
	}
	result.Uploads = append(result.Uploads, BlobResult{
		URL: sanitize.URL(upload.URL), Hash: upload.SHA256, Size: upload.Size,
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
	if proofEvent != nil {
		reportPublish(options, "publish", "relay", "C1 proof", 0, 0)
		relayResults, accepted, publishErr := publishEvent(ctx, publisher, proofEvent)
		result.Relays = append(result.Relays, relayResults...)
		if !accepted {
			result.Status = "partial"
			return result, publishErr
		}
		result.Warnings = append(result.Warnings, relayWarnings(relayResults)...)
	}
	result.ID = events.Release.ID
	result.Status = "published"
	published = true
	return result, nil
}

// retainPublished copies the verified APK to APK_CACHE/<sha256> when that
// directory is set and this publish accepted the release, or the release was
// already published. An unset variable copies nothing. Any other outcome
// writes nothing. The copy is a new file: the fetch temp may sit on another
// filesystem.
func (apk *APK) retainPublished(published bool, publishErr error) error {
	if apk == nil || (!published && !errors.Is(publishErr, ErrAlreadyPublished)) {
		return nil
	}
	dir := strings.TrimSpace(internalconfig.GetEnv("APK_CACHE"))
	if dir == "" {
		return nil
	}
	hash := apk.verified.hash
	if !sha256Hex(hash) {
		return fmt.Errorf("APK hash is not sha256")
	}
	src, err := apk.Path()
	if err != nil {
		return err
	}
	return copyFileAs(src, filepath.Join(dir, hash))
}

func sha256Hex(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// copyFileAs copies src to dest, replacing dest. The write lands in dest's
// directory first so the rename stays on one filesystem.
func copyFileAs(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".partial-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	done = true
	return nil
}

// shouldClearHTTPCache reports whether a Publish error means the next Fetch
// should ignore stored validators. The cache answers "did the source change?",
// not "did we publish?". Permanent caller, developer, or config outcomes keep
// the cache; SkipHTTPCache is the lever when those inputs change.
func shouldClearHTTPCache(err error) bool {
	var operationError Error
	return errors.As(err, &operationError) && operationError.Retryable()
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

// uploadErrorDetail renders why Blossom rejected an upload: the HTTP status and
// server-supplied reason for a rejected request, or the specific disagreement
// for a descriptor that passed the status check but failed validation. A
// failure that would otherwise read as a bare "blob upload rejected" says what
// actually went wrong. Server text and any URL in it are reduced to their
// credential-free forms first.
func uploadErrorDetail(err error) string {
	var statusError *blossom.StatusError
	if errors.As(err, &statusError) {
		detail := fmt.Sprintf(": status %d", statusError.StatusCode)
		if reason := publicDetail(statusError.Reason); reason != "" {
			detail += ": " + reason
		}
		return detail
	}
	if reason := publicDetail(descriptorReason(err)); reason != "" {
		return ": " + reason
	}
	return ""
}

// causeDetail renders an error's message as a suffix for an operation error,
// which wrapOperationError would otherwise discard, so callers can see why
// image or media preparation failed instead of just which step failed.
func causeDetail(err error) string {
	if err == nil {
		return ""
	}
	if detail := publicDetail(err.Error()); detail != "" {
		return ": " + detail
	}
	return ""
}

// uploadFailureDetail renders why an upload failed: the Blossom status and
// server reason when the server answered, otherwise the sanitized cause text
// (for example a connection error).
func uploadFailureDetail(err error) string {
	if detail := uploadErrorDetail(err); detail != "" {
		return detail
	}
	return causeDetail(err)
}

// descriptorReason returns the specific disagreement reported by a descriptor
// validation error, without the sentinel prefix, or "" when err is not one.
func descriptorReason(err error) string {
	var descriptorErr *blossom.DescriptorError
	if !errors.As(err, &descriptorErr) {
		return ""
	}
	if descriptorErr.Err != nil {
		return descriptorErr.Reason + ": " + descriptorErr.Err.Error()
	}
	return descriptorErr.Reason
}

// descriptorMismatch names the fields where the descriptor a server returned
// disagrees with the upload zsp performed. The client validates every field
// against the request already, so this reports the stricter exact-URL check.
func descriptorMismatch(got *blossom.UploadResult, wantURL, wantHash string, wantSize int64, wantType string) string {
	var different []string
	if got.URL != wantURL {
		different = append(different, "url")
	}
	if got.SHA256 != wantHash {
		different = append(different, "sha256")
	}
	if got.Size != wantSize {
		different = append(different, "size")
	}
	if got.Type != wantType {
		different = append(different, "type")
	}
	if len(different) == 0 {
		return ""
	}
	return ": mismatch in " + strings.Join(different, ", ")
}

// publicDetail strips credentials, query parameters, fragments, and other
// private material from arbitrary detail text: text from a server or a fetch
// may echo a configured endpoint that carries a token.
func publicDetail(detail string) string {
	return sanitize.Text(strings.TrimSpace(detail))
}

func networkErrorClassification(err error) (error, bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err, false
	}
	if errors.Is(err, source.ErrRateLimited) || internalnostr.IsRateLimited(err) {
		return ErrRateLimited, true
	}
	if source.IsTransient(err) {
		return ErrTemporaryFailure, true
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

func activeC1Publishers(proofs []*gonostr.Event, certificate *x509.Certificate) map[string]struct{} {
	return c1Publishers(activeC1Proof(proofs, certificate, time.Now()))
}

// activeC1Proof returns the newest valid proof for certificate. A proof for
// an ancestor does not count.
func activeC1Proof(proofs []*gonostr.Event, certificate *x509.Certificate, now time.Time) *gonostr.Event {
	if certificate == nil {
		return nil
	}
	want := identity.ComputeCertHash(certificate)
	var activeProof *gonostr.Event
	for _, proof := range proofs {
		if proof == nil {
			continue
		}
		tag := proof.Tags.GetFirst([]string{"d"})
		if tag == nil || len(*tag) < 2 || strings.ToLower((*tag)[1]) != want {
			continue
		}
		if _, validationErr := identity.ValidateActiveProofEvent(proof, certificate, now); validationErr != nil {
			continue
		}
		if activeProof == nil || proof.CreatedAt > activeProof.CreatedAt ||
			(proof.CreatedAt == activeProof.CreatedAt && proof.ID < activeProof.ID) {
			activeProof = proof
		}
	}
	return activeProof
}

func c1Publishers(activeProof *gonostr.Event) map[string]struct{} {
	if activeProof == nil {
		return nil
	}
	authorized := map[string]struct{}{activeProof.PubKey: {}}
	if delegation := activeProof.Tags.GetFirst([]string{"delegation"}); delegation != nil {
		authorized[(*delegation)[1]] = struct{}{}
	}
	return authorized
}

const c1RenewBefore = 30 * 24 * time.Hour

// c1ProofNeedsRenewal reports whether the signer should publish a proof:
// none is active, or the signer's own proof expires soon. A proof owned by
// another key is never replaced.
func c1ProofNeedsRenewal(active *gonostr.Event, pubkey string, now time.Time) bool {
	if active == nil {
		return true
	}
	if active.PubKey != pubkey {
		return false
	}
	proof, err := identity.ParseIdentityProofFromEvent(active)
	return err == nil && time.Unix(proof.Expiry, 0).Sub(now) < c1RenewBefore
}

// pendingC1Publishers is the authorized set once the signer's new or renewed
// proof is published. A renewal keeps the delegate of the signer's own proof.
func pendingC1Publishers(signer string, previous *gonostr.Event) map[string]struct{} {
	authorized := map[string]struct{}{signer: {}}
	if previous != nil && previous.PubKey == signer {
		if delegation := previous.Tags.GetFirst([]string{"delegation"}); delegation != nil && len(*delegation) == 2 {
			authorized[(*delegation)[1]] = struct{}{}
		}
	}
	return authorized
}

// buildC1Proof asks proofSigner for the certificate signature and signs the
// 30509 event with the Nostr signer. A renewal keeps the delegation of the
// proof it replaces.
func buildC1Proof(ctx context.Context, proofSigner ProofSigner, signer internalnostr.Signer, certificate *x509.Certificate, previous *gonostr.Event, now time.Time) (*gonostr.Event, error) {
	createdAt := now.Unix()
	expiry := now.Add(identity.DefaultExpiry).Unix()
	signed, err := proofSigner.SignProof(ctx, createdAt, expiry)
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "sign C1 proof"); contextErr != nil {
			return nil, contextErr
		}
		return nil, operationErr(ErrSigner, false, "%s", "sign C1 proof"+causeDetail(err))
	}
	certHash := identity.ComputeCertHash(certificate)
	if signed.PublicKey != signer.PublicKey() {
		return nil, operationErr(ErrSigner, false, "C1 proof signer names a different Nostr key than SIGN_WITH")
	}
	if signed.CertificateHash != certHash {
		return nil, operationErr(ErrSigner, false, "C1 proof signer holds a different certificate than the APK")
	}
	proof := &identity.IdentityProof{
		CertHash:  certHash,
		Signature: base64.StdEncoding.EncodeToString(signed.Signature),
		CreatedAt: createdAt,
		Expiry:    expiry,
	}
	event := internalnostr.BuildIdentityProofEvent(proof.ToEventTags(), signer.PublicKey(), createdAt)
	if previous != nil && previous.PubKey == signer.PublicKey() {
		if delegation := previous.Tags.GetFirst([]string{"delegation"}); delegation != nil {
			event.Tags = append(event.Tags, gonostr.Tag{"delegation", (*delegation)[1]})
		}
	}
	if err := signer.Sign(ctx, event); err != nil {
		if contextErr := contextOperationError(ctx.Err(), "sign C1 proof event"); contextErr != nil {
			return nil, contextErr
		}
		return nil, operationErr(ErrSigner, false, "%s", "sign C1 proof event"+causeDetail(err))
	}
	if _, err := identity.ValidateActiveProofEvent(event, certificate, now); err != nil {
		return nil, operationErr(ErrSigner, false, "%s", "C1 proof is invalid"+causeDetail(err))
	}
	return event, nil
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
	for _, relayURL := range relays(values) {
		if _, ok := internalconfig.RelaySocketPath(relayURL); ok {
			continue
		}
		if public := publicRelayURL(relayURL); public != "" {
			return public
		}
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
			} else if detail := publicDetail(relay.Message); detail != "" {
				message = detail
			} else {
				message = "relay rejected event"
			}
			message = sanitize.Truncate(message, 1000)
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
