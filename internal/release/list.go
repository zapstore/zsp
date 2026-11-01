package release

import (
	"context"
	"errors"
	"time"

	"github.com/zapstore/zsp/v2/internal/apk"
	"github.com/zapstore/zsp/v2/internal/source"
)

// ListedAsset is an APK a release carries, as its forge describes it.
type ListedAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Size is the length in bytes, or zero when the forge does not say.
	Size int64 `json:"size,omitempty"`
	// SHA256 is the digest the forge computed (lowercase hex), or empty. An
	// APK with a digest already seen need not be read again.
	SHA256 string `json:"sha256,omitempty"`
}

// ListedRelease is one release of a forge, as it lists it, whether or not Fetch
// would pick it.
type ListedRelease struct {
	Tag   string `json:"tag"`
	Title string `json:"title,omitempty"`
	// Notes are the forge's release notes, cut to 8 KiB.
	Notes      string    `json:"notes,omitempty"`
	Prerelease bool      `json:"prerelease,omitempty"`
	Date       time.Time `json:"date,omitzero"`
	// Assets are the release's APKs and nothing else.
	Assets []ListedAsset `json:"assets,omitempty"`
}

// ListOptions controls ListReleases.
type ListOptions struct {
	// MaxReleases is how many of the newest releases to list. Zero uses 10; the
	// most is 100.
	MaxReleases int
}

// ListReleases returns the newest releases of the config's forge, newest first,
// with the APKs each carries. It downloads nothing and stores nothing.
//
// Unlike Fetch it applies neither release_filter, match nor the pre-release
// choice: it shows what those would hide. Drafts are left out. Only GitHub,
// GitLab and Gitea-compatible forges list releases; any other source fails with
// [ErrListUnsupported].
func ListReleases(ctx context.Context, config FetchConfig, options ListOptions) ([]ListedRelease, error) {
	internal, err := fetchInternalConfig(config)
	if err != nil {
		return nil, err
	}
	src, err := source.NewWithOptions(internal, source.Options{
		IncludePreReleases: true,
		SkipHTTPCache:      true,
		MaxReleases:        options.MaxReleases,
	})
	if err != nil {
		return nil, wrapOperationError(ErrInvalidConfig, err, false, "create source"+causeDetail(err))
	}
	lister, ok := src.(source.Lister)
	if !ok {
		return nil, operationErr(ErrListUnsupported, false, "a %s source does not list releases", src.Type())
	}

	releases, err := lister.ListReleases(ctx, options.MaxReleases)
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "list"); contextErr != nil {
			return nil, contextErr
		}
		sentinel, retryable := sourceErrorClassification(err)
		return nil, wrapOperationError(sentinel, err, retryable, "list releases"+causeDetail(err))
	}

	listed := make([]ListedRelease, 0, len(releases))
	for _, release := range releases {
		item := ListedRelease{
			Tag:        release.TagName,
			Title:      release.Name,
			Notes:      truncateReleaseNotes(release.Changelog),
			Prerelease: release.PreRelease,
			Date:       release.CreatedAt,
		}
		for _, asset := range release.Assets {
			item.Assets = append(item.Assets, ListedAsset{Name: asset.Name, URL: asset.URL, Size: asset.Size, SHA256: asset.SHA256})
		}
		listed = append(listed, item)
	}
	return listed, nil
}

// APKIdentity is what an APK says about itself, read without downloading it.
//
// It is the APK's claim: nothing is verified, so it only says whether an APK is
// worth fetching. Fetch is what verifies one.
type APKIdentity struct {
	AppID       string `json:"app_id"`
	VersionName string `json:"version_name,omitempty"`
	VersionCode int64  `json:"version_code"`
	// Architectures are the ABIs of the APK's lib/ directories. Empty for an APK
	// with no native code, or whose native code is stored elsewhere.
	Architectures []string `json:"architectures,omitempty"`
	// CertificateHash is the SHA-256 (lowercase hex) of the signing certificate
	// the APK names, as APK.CertificateHash would be after Fetch.
	CertificateHash string `json:"certificate_hash"`
	// BytesRead is how much of the APK was fetched to learn this.
	BytesRead int64 `json:"bytes_read"`
}

// ProbeOptions controls ProbeAPK.
type ProbeOptions struct {
	// MaxBytes bounds what is fetched from one APK. Zero uses 8 MiB. An APK's
	// identity takes a small part of the file, so a server that makes it take
	// more is not worth reading.
	MaxBytes int64
}

// ProbeAPK reads the identity of a listed APK with byte-range requests: the end
// of the file for its index and signing block, and the part holding the
// manifest. That is a small part of the APK, however large, where Fetch takes
// all of it.
//
// A server that does not answer range requests fails with [ErrRangeUnsupported];
// the caller decides whether the APK is worth downloading instead. An APK that
// cannot be read, or has no signature or several signers, fails with
// [ErrInvalidAPK].
func ProbeAPK(ctx context.Context, asset ListedAsset, options ProbeOptions) (*APKIdentity, error) {
	if asset.URL == "" {
		return nil, operationErr(ErrInvalidConfig, false, "asset %s has no URL", asset.Name)
	}
	reader, err := source.OpenRange(ctx, asset.URL, asset.Size, options.MaxBytes, nil)
	if err != nil {
		return nil, probeReadError(ctx, err)
	}

	identity, parseErr := apk.ReadIdentity(ctx, reader, reader.Size())
	// A parser may take a failed read for a missing structure, so what the
	// reader saw outranks what the parser concluded.
	if readErr := reader.Err(); readErr != nil {
		return nil, probeReadError(ctx, readErr)
	}
	if parseErr != nil {
		if contextErr := contextOperationError(ctx.Err(), "probe"); contextErr != nil {
			return nil, contextErr
		}
		return nil, wrapOperationError(ErrInvalidAPK, parseErr, false, "read APK identity"+causeDetail(parseErr))
	}

	bytesRead, _ := reader.Fetched()
	return &APKIdentity{
		AppID:           identity.PackageID,
		VersionName:     identity.VersionName,
		VersionCode:     identity.VersionCode,
		Architectures:   identity.Architectures,
		CertificateHash: identity.CertFingerprint,
		BytesRead:       bytesRead,
	}, nil
}

// probeReadError classifies a failure to fetch part of an APK.
func probeReadError(ctx context.Context, err error) error {
	if contextErr := contextOperationError(ctx.Err(), "probe"); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, source.ErrRangeUnsupported) {
		return wrapOperationError(ErrRangeUnsupported, err, false, "probe APK")
	}
	sentinel, retryable := sourceErrorClassification(err)
	return wrapOperationError(sentinel, err, retryable, "probe APK"+causeDetail(err))
}
