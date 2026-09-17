// Package zsp provides the public API for validating and publishing Zapstore releases.
package zsp

import (
	"errors"
	"sync"
	"time"
)

// Config contains source resolution and publication metadata.
type Config struct {
	FetchConfig   `json:"fetch_config"`
	PublishConfig `json:"publish_config"`
}

// FetchConfig identifies and filters an APK release source.
type FetchConfig struct {
	Repository        string         `json:"repository"`
	ReleaseSource     *ReleaseSource `json:"release_source"`
	ReleaseFilter     string         `json:"release_filter"`
	Match             string         `json:"match"`
	PrereleaseChannel string         `json:"prerelease_channel"`
	// SkipHTTPCache disables conditional requests. When false (the default),
	// Fetch sends stored validators (ETag, Last-Modified, Content-Length) and
	// returns ErrNoNewAPK if the source reports no change. Publish deletes
	// that cache if publication fails.
	SkipHTTPCache bool `json:"skip_http_cache"`
	baseDir       string
}

// PublishConfig contains application metadata used to build NIP-82 events.
type PublishConfig struct {
	Channel               string   `json:"channel"`
	Name                  string   `json:"name"`
	Summary               string   `json:"summary"`
	Description           string   `json:"description"`
	Tags                  []string `json:"tags"`
	License               string   `json:"license"`
	Website               string   `json:"website"`
	Icon                  string   `json:"icon"`
	Images                []string `json:"images"`
	ReleaseNotes          string   `json:"release_notes"`
	SupportedNIPs         []string `json:"supported_nips"`
	MinAllowedVersion     string   `json:"min_allowed_version"`
	MinAllowedVersionCode int64    `json:"min_allowed_version_code"`
	// MetadataSources is nil for automatic source selection, empty to disable
	// metadata fetching, or non-empty to select explicit sources.
	MetadataSources []string `json:"metadata_sources"`
	baseDir         string
}

// ReleaseSource describes a local, forge, F-Droid, or web release source.
type ReleaseSource struct {
	URL              string     `json:"url"`
	LocalPath        string     `json:"local_path"`
	Type             string     `json:"type"`
	AssetURL         string     `json:"asset_url"`
	VersionExtractor *Extractor `json:"version_extractor"`
	AssetExtractor   *Extractor `json:"asset_extractor"`
}

// Extractor describes how to obtain a value from HTML, JSON, or headers.
type Extractor struct {
	URL       string `json:"url"`
	Selector  string `json:"selector"`
	Attribute string `json:"attribute"`
	Path      string `json:"path"`
	Header    string `json:"header"`
	Match     string `json:"match"`
}

// FetchOptions controls source resolution and progress reporting.
type FetchOptions struct {
	OnProgress func(Progress) `json:"-"`
}

// APK is a downloaded and verified publication candidate.
type APK struct {
	Hash            string   `json:"hash"`
	Filename        string   `json:"filename"`
	SourceURL       string   `json:"source_url,omitempty"`
	Size            int64    `json:"size"`
	AppID           string   `json:"app_id"`
	VersionName     string   `json:"version_name"`
	VersionCode     int64    `json:"version_code"`
	MinSDK          int32    `json:"min_sdk"`
	TargetSDK       int32    `json:"target_sdk"`
	Name            string   `json:"name"`
	CertificateHash string   `json:"certificate_hash"`
	LineageHashes   []string `json:"lineage_hashes,omitempty"`
	Architectures   []string `json:"architectures"`

	ownership   *apkOwnership
	fetchConfig FetchConfig
	verified    verifiedAPK
}

// verifiedAPK is the immutable Fetch result used by Publish. APK's exported
// fields are inspection copies and must not influence a signed publication.
type verifiedAPK struct {
	hash            string
	filename        string
	size            int64
	appID           string
	versionCode     int64
	certificateHash string
	lineageHashes   []string
	originalURL     string
	releaseNotes    string
	releasedAt      time.Time
}

// apkOwnership is shared by all copies of an APK value. In particular, it
// prevents a copied APK from becoming a second apparent owner of one managed
// temporary file.
type apkOwnership struct {
	mu             sync.Mutex
	path           string
	tempDir        string
	managed        bool
	closed         bool
	publishing     bool
	closeRequested bool
	clearHTTPCache func() error
}

// Progress reports a synchronous operation update.
type Progress struct {
	Operation string `json:"operation"`
	// Phase is "warning" for a safe candidate-rejection diagnostic; in that
	// case Target contains the diagnostic and numeric fields are zero.
	Phase     string `json:"phase"`
	Target    string `json:"target"`
	Completed int64  `json:"completed"`
	Total     int64  `json:"total"`
}

// PublishOptions controls proof checks, event construction, and publication.
type PublishOptions struct {
	Channel              string         `json:"channel"`
	BlossomURL           string         `json:"blossom_url"`
	Relays               []string       `json:"relays"`
	Commit               string         `json:"commit"`
	SkipAppEvent         bool           `json:"skip_app_event"`
	SkipMediaCompression bool           `json:"skip_media_compression"`
	SkipProofCheck       bool           `json:"skip_proof_check"`
	OverwriteRelease     bool           `json:"overwrite_release"`
	Preview              bool           `json:"preview"`
	BrowserPort          int            `json:"browser_port"`
	OnProgress           func(Progress) `json:"-"`
}

// EventIDs identifies the events produced for a publication.
type EventIDs struct {
	Application string   `json:"application"`
	Release     string   `json:"release"`
	Assets      []string `json:"assets"`
}

// PublishResult records all known upload and relay outcomes.
type PublishResult struct {
	ID              string        `json:"id"`
	Status          string        `json:"status"`
	AppID           string        `json:"app_id"`
	CertificateHash string        `json:"certificate_hash"`
	LineageHashes   []string      `json:"lineage_hashes,omitempty"`
	Events          EventIDs      `json:"events"`
	Uploads         []BlobResult  `json:"uploads"`
	Relays          []RelayResult `json:"relays"`
	Warnings        []string      `json:"warnings"`
}

// BlobResult records one Blossom upload outcome.
type BlobResult struct {
	URL      string `json:"url"`
	Hash     string `json:"hash"`
	Size     int64  `json:"size"`
	Type     string `json:"type"`
	Uploaded int64  `json:"uploaded"`
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

// RelayResult records one event's outcome at one relay.
type RelayResult struct {
	RelayURL  string `json:"relay_url"`
	EventID   string `json:"event_id"`
	Accepted  bool   `json:"accepted"`
	Duplicate bool   `json:"duplicate"`
	Message   string `json:"message,omitempty"`
}

// Error is an operation error that tells callers whether identical input may
// succeed when retried.
type Error interface {
	error
	Retryable() bool
}

// Public error classes support errors.Is classification.
var (
	ErrInvalidConfig        = errors.New("config is invalid")
	ErrSourceFailed         = errors.New("source failed")
	ErrTooManyCandidates    = errors.New("too many APK candidates")
	ErrNoAPK                = errors.New("no APK found")
	ErrNoNewAPK             = errors.New("no new APKs")
	ErrInvalidAPK           = errors.New("APK is invalid")
	ErrAPKNotChecked        = errors.New("APK was not returned by Fetch")
	ErrAPKSelectionRequired = errors.New("APK selection required")
	ErrProofRequired        = errors.New("active proof required")
	ErrProofUnauthorized    = errors.New("signer is not authorized by the proof")
	ErrSigner               = errors.New("signer is unavailable")
	ErrAlreadyPublished     = errors.New("release already published")
	ErrReleaseDowngrade     = errors.New("release version code is a downgrade")
	ErrUploadRejected       = errors.New("blob upload rejected")
	ErrPublishRejected      = errors.New("relay publication rejected")
	ErrRateLimited          = errors.New("rate limited")
	ErrTemporaryFailure     = errors.New("temporary failure")
)
