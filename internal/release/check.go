package release

import (
	"context"
	"time"

	"github.com/zapstore/zsp/internal/apk"
	internalconfig "github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/identity"
	internalnostr "github.com/zapstore/zsp/internal/nostr"
)

// CheckOptions selects the relays and channel Check asks about.
type CheckOptions struct {
	Relays  []string
	Channel string
}

// CheckResult is what RELAYS know about a verified APK.
type CheckResult struct {
	AppID                string
	CertificateHash      string
	Listed               bool
	Proof                *ProofStatus // nil when no proof is active
	PublishedVersionCode int64
	Signer               string // SIGN_WITH public key, lowercase hex; empty when unset
	Authorized           bool
	Warnings             []string
}

// ProofStatus is the active C1 proof for an APK certificate.
type ProofStatus struct {
	Owner     string
	Delegate  string
	ExpiresAt time.Time
}

// Check reports whether the APK's app is listed, which proof is active for
// its certificate, the greatest published version code, and whether
// SIGN_WITH may publish. It signs, uploads, and publishes nothing, and
// leaves apk open.
func Check(ctx context.Context, config PublishConfig, candidate *APK, options CheckOptions) (*CheckResult, error) {
	if candidate == nil || candidate.verified.hash == "" {
		return nil, operationErr(ErrAPKNotChecked, false, "APK was not returned by Fetch")
	}
	path, err := candidate.Path()
	if err != nil {
		return nil, operationErr(ErrAPKNotChecked, false, "APK was not returned by Fetch")
	}
	if err := validateRelayURLs(options.Relays); err != nil {
		return nil, err
	}
	relayURLs := relays(options.Relays)
	if err := validateRelayURLs(relayURLs); err != nil {
		return nil, err
	}
	verified := candidate.verified
	certificates, err := apk.SigningCertificates(ctx, path)
	if err != nil {
		return nil, wrapOperationError(ErrInvalidAPK, err, false, "extract signing certificate"+causeDetail(err))
	}
	result := &CheckResult{AppID: verified.appID, CertificateHash: verified.certificateHash}

	signWith, err := internalconfig.GetSignWith()
	if err != nil {
		return nil, wrapOperationError(ErrInvalidConfig, err, false, "read SIGN_WITH"+causeDetail(err))
	}
	if signWith != "" {
		signer, signerErr := internalnostr.NewSigner(ctx, signWith)
		if signerErr != nil {
			if contextErr := contextOperationError(signerErr, "create signer"); contextErr != nil {
				return nil, contextErr
			}
			return nil, operationErr(ErrSigner, false, "%s", "create signer"+causeDetail(signerErr))
		}
		result.Signer = signer.PublicKey()
		signer.Close()
	}

	publisher := internalnostr.NewPublisher(relayURLs)
	locations, appWarnings, err := publisher.FindAppEvents(ctx, verified.appID)
	if err != nil {
		return nil, checkQueryError(ctx, err, "find app")
	}
	result.Listed = len(locations.ApplicationRelays) > 0
	result.Warnings = append(result.Warnings, safeRelayQueryWarnings("application event", appWarnings)...)

	if len(certificates) == 0 {
		return nil, operationErr(ErrInvalidAPK, false, "extract signing certificate")
	}
	current := certificates[len(certificates)-1]
	proofs, proofWarnings, err := publisher.FetchIdentityProofs(ctx, []string{identity.ComputeCertHash(current)})
	if err != nil {
		return nil, checkQueryError(ctx, err, "check C1 proof")
	}
	result.Warnings = append(result.Warnings, safeRelayQueryWarnings("C1 proof", proofWarnings)...)
	active := activeC1Proof(proofs, current, time.Now())
	if active == nil {
		return result, nil
	}
	proof, err := identity.ParseIdentityProofFromEvent(active)
	if err != nil {
		return result, nil
	}
	result.Proof = &ProofStatus{Owner: active.PubKey, ExpiresAt: proof.ExpiryTime().UTC()}
	if delegation := active.Tags.GetFirst([]string{"delegation"}); delegation != nil && len(*delegation) == 2 {
		result.Proof.Delegate = (*delegation)[1]
	}
	publishers := c1Publishers(active)
	_, result.Authorized = publishers[result.Signer]

	channel := selectedChannel(PublishOptions{Channel: options.Channel}, config)
	versionCode, _, releaseWarnings, err := publisher.HighestReleaseVersionCode(ctx, publishers, verified.appID, verified.certificateHashes(), channel)
	if err != nil {
		return nil, checkQueryError(ctx, err, "check published releases")
	}
	result.PublishedVersionCode = versionCode
	result.Warnings = append(result.Warnings, safeRelayQueryWarnings("published release", releaseWarnings)...)
	return result, nil
}

func checkQueryError(ctx context.Context, err error, action string) error {
	if contextErr := contextOperationError(ctx.Err(), action); contextErr != nil {
		return contextErr
	}
	sentinel, retryable := relayQueryErrorClassification(err)
	return wrapOperationError(sentinel, err, retryable, action+causeDetail(err))
}
