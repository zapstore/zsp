// Package zsp provides the public API for validating and publishing Zapstore
// releases. The implementation lives in internal/release; this file only
// re-exports it so the import path stays github.com/zapstore/zsp.
package zsp

import (
	"context"
	_ "embed"
	"io"

	"github.com/zapstore/zsp/internal/release"
)

// Skill is the agent skill for writing and maintaining zapstore.yaml, as
// printed by `zsp skill`.
//
//go:embed SKILL.md
var Skill string

type (
	// Config contains source resolution and publication metadata.
	Config = release.Config
	// FetchConfig identifies and filters an APK release source.
	FetchConfig = release.FetchConfig
	// PublishConfig contains application metadata used to build NIP-82 events.
	PublishConfig = release.PublishConfig
	// ReleaseSource describes a local, forge, F-Droid, or web release source.
	ReleaseSource = release.ReleaseSource
	// Extractor describes how to obtain a value from HTML, JSON, or headers.
	Extractor = release.Extractor
	// FetchOptions controls per-fetch behavior such as HTTP cache use and progress reporting.
	FetchOptions = release.FetchOptions
	// APK is a downloaded and verified publication candidate.
	APK = release.APK
	// Progress reports a synchronous operation update.
	Progress = release.Progress
	// PublishOptions controls proof checks, event construction, and publication.
	PublishOptions = release.PublishOptions
	// ProofSigner signs the NIP-C1 proof message with the APK certificate key.
	ProofSigner = release.ProofSigner
	// ProofSignature is a certificate signature over the C1 proof message.
	ProofSignature = release.ProofSignature
	// EventIDs identifies the events produced for a publication.
	EventIDs = release.EventIDs
	// PublishResult records all known upload and relay outcomes.
	PublishResult = release.PublishResult
	// BlobResult records one Blossom upload outcome.
	BlobResult = release.BlobResult
	// RelayResult records one event's outcome at one relay.
	RelayResult = release.RelayResult
	// CheckOptions controls a read-only listing check.
	CheckOptions = release.CheckOptions
	// CheckResult reports whether an APK is ready to publish.
	CheckResult = release.CheckResult
	// ProofStatus describes the active C1 proof for a certificate.
	ProofStatus = release.ProofStatus
	// Error is an operation error that tells callers whether identical input
	// may succeed when retried.
	Error = release.Error
)

// Public error classes support errors.Is classification.
var (
	ErrInvalidConfig        = release.ErrInvalidConfig
	ErrSourceFailed         = release.ErrSourceFailed
	ErrTooManyCandidates    = release.ErrTooManyCandidates
	ErrNoAPK                = release.ErrNoAPK
	ErrNoNewAPK             = release.ErrNoNewAPK
	ErrInvalidAPK           = release.ErrInvalidAPK
	ErrAPKNotChecked        = release.ErrAPKNotChecked
	ErrAPKSelectionRequired = release.ErrAPKSelectionRequired
	ErrProofRequired        = release.ErrProofRequired
	ErrProofUnauthorized    = release.ErrProofUnauthorized
	ErrSigner               = release.ErrSigner
	ErrAlreadyPublished     = release.ErrAlreadyPublished
	ErrReleaseDowngrade     = release.ErrReleaseDowngrade
	ErrUploadRejected       = release.ErrUploadRejected
	ErrPublishRejected      = release.ErrPublishRejected
	ErrRateLimited          = release.ErrRateLimited
	ErrTemporaryFailure     = release.ErrTemporaryFailure
)

// ParseConfig reads a complete zapstore.yaml document.
func ParseConfig(r io.Reader) (Config, error) { return release.ParseConfig(r) }

// ParseFetchConfig reads only the release source fields of zapstore.yaml.
func ParseFetchConfig(r io.Reader) (FetchConfig, error) { return release.ParseFetchConfig(r) }

// ParsePublishConfig reads only the metadata fields of zapstore.yaml.
func ParsePublishConfig(r io.Reader) (PublishConfig, error) { return release.ParsePublishConfig(r) }

// LoadConfig reads zapstore.yaml from path and resolves local paths relative
// to its directory.
func LoadConfig(path string) (Config, error) { return release.LoadConfig(path) }

// Fetch downloads and verifies the APK candidates for config.
func Fetch(ctx context.Context, config FetchConfig, options FetchOptions) ([]*APK, error) {
	return release.Fetch(ctx, config, options)
}

// Check reports whether candidate can be published, without publishing.
func Check(ctx context.Context, config PublishConfig, candidate *APK, options CheckOptions) (*CheckResult, error) {
	return release.Check(ctx, config, candidate, options)
}

// Publish uploads one verified APK and publishes its NIP-82 events.
func Publish(ctx context.Context, config PublishConfig, candidate *APK, options PublishOptions) (*PublishResult, error) {
	return release.Publish(ctx, config, candidate, options)
}

// Icon returns the launcher icon extracted from an APK, as PNG bytes.
// A missing icon is nil, nil.
func Icon(ctx context.Context, path string) ([]byte, error) { return release.Icon(ctx, path) }
