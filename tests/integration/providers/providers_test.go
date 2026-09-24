//go:build providers

package providers

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	nostr "github.com/nbd-wtf/go-nostr"
	zsp "github.com/zapstore/zsp"
)

const (
	kindAppMetadata   = 32267
	kindSoftwareAsset = 3063
)

const remoteRelayURL = "wss://relay.zapstore.dev"

type providerCase struct {
	name            string
	repository      string
	releaseSource   *zsp.ReleaseSource
	match           string
	appID           string
	sourceHost      string
	relaySourceHost string
}

func TestRemoteProvidersEndToEnd(t *testing.T) {
	tests := []providerCase{
		{
			name:       "github",
			repository: "https://github.com/greenart7c3/Citrine",
			match:      `(?i)^citrine-arm64-v8a-.*\.apk$`,
			appID:      "com.greenart7c3.citrine",
			sourceHost: "github.com",
		},
		{
			name:       "gitlab",
			repository: "https://gitlab.com/AuroraOSS/AuroraStore",
			match:      `^AuroraStore-[0-9].*\.apk$`,
			appID:      "com.aurora.store",
			sourceHost: "gitlab.com",
		},
		{
			name:       "self-hosted gitlab",
			repository: "https://framagit.org/android1/etagere",
			releaseSource: &zsp.ReleaseSource{
				URL:  "https://framagit.org/android1/etagere",
				Type: "gitlab",
			},
			match:           `^etagere-release\.apk$`,
			appID:           "fr.framagit.jeromeo.etagere",
			sourceHost:      "framagit.org",
			relaySourceHost: "f-droid.org",
		},
		{
			name:       "codeberg",
			repository: "https://codeberg.org/tkuenneth/mintime",
			match:      `^app-release\.apk$`,
			appID:      "com.thomaskuenneth.mintime",
			sourceHost: "codeberg.org",
		},
		{
			name:       "self-hosted gitea",
			repository: "https://gitea.angry.im/jmp-sim/jmp-sim-manager",
			releaseSource: &zsp.ReleaseSource{
				URL:  "https://gitea.angry.im/jmp-sim/jmp-sim-manager",
				Type: "gitea",
			},
			match:      `^app-unpriv-jmp-release\.apk$`,
			appID:      "chat.jmp.simmanager",
			sourceHost: "gitea.angry.im",
		},
		{
			name:       "forgejo",
			repository: "https://forjalibre.eu/ferlagod/rocinante_android",
			releaseSource: &zsp.ReleaseSource{
				URL:  "https://forjalibre.eu/ferlagod/rocinante_android",
				Type: "gitea",
			},
			match:           `^Rocinante_.*\.apk$`,
			appID:           "com.ferlagod.rocinante",
			sourceHost:      "forjalibre.eu",
			relaySourceHost: "f-droid.org",
		},
		{
			name:       "fdroid",
			repository: "https://codeberg.org/tkuenneth/mintime",
			releaseSource: &zsp.ReleaseSource{
				URL: "https://f-droid.org/packages/com.thomaskuenneth.mintime",
			},
			appID:           "com.thomaskuenneth.mintime",
			sourceHost:      "f-droid.org",
			relaySourceHost: "codeberg.org",
		},
		{
			name:       "web json extractor",
			repository: "https://codeberg.org/tkuenneth/mintime",
			releaseSource: &zsp.ReleaseSource{
				AssetURL: "https://codeberg.org/tkuenneth/mintime/releases/download/{version}/app-release.apk",
				VersionExtractor: &zsp.Extractor{
					URL:  "https://codeberg.org/api/v1/repos/tkuenneth/mintime/releases/latest",
					Path: "$.tag_name",
				},
			},
			appID:      "com.thomaskuenneth.mintime",
			sourceHost: "codeberg.org",
		},
		{
			name:       "web direct URL",
			repository: "https://codeberg.org/tkuenneth/mintime",
			releaseSource: &zsp.ReleaseSource{
				URL: "https://codeberg.org/tkuenneth/mintime/releases/download/2.0.4/app-release.apk",
			},
			appID:      "com.thomaskuenneth.mintime",
			sourceHost: "codeberg.org",
		},
	}

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	relay, err := nostr.RelayConnect(ctx, remoteRelayURL)
	if err != nil {
		t.Fatalf("connect to %s: %v", remoteRelayURL, err)
	}
	defer relay.Close()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertRelayApp(t, ctx, relay, test)

			fetchCtx, fetchCancel := context.WithTimeout(ctx, 4*time.Minute)
			defer fetchCancel()
			fetchPhases := make(map[string]bool)
			candidates, err := zsp.Fetch(fetchCtx, zsp.FetchConfig{
				Repository:    test.repository,
				ReleaseSource: test.releaseSource,
				Match:         test.match,
			}, zsp.FetchOptions{
				OnProgress: func(progress zsp.Progress) {
					fetchPhases[progress.Phase] = true
				},
			})
			if err != nil {
				t.Fatalf("fetch from %s: %v", test.sourceHost, err)
			}
			defer closeCandidates(t, candidates)
			if len(candidates) != 1 {
				t.Fatalf("candidates = %d, want exactly 1; narrow the canary match if the upstream release layout changed", len(candidates))
			}
			assertAPK(t, candidates[0], test)
			assertRelayAsset(t, ctx, relay, test)
			assertPhases(t, "fetch", fetchPhases, "resolve", "download", "verify")
		})
	}
}

func assertPhases(t *testing.T, operation string, got map[string]bool, want ...string) {
	t.Helper()
	for _, phase := range want {
		if !got[phase] {
			t.Errorf("%s progress phases = %v, missing %q", operation, got, phase)
		}
	}
}

func assertRelayApp(t *testing.T, ctx context.Context, relay *nostr.Relay, test providerCase) {
	t.Helper()
	events, err := relay.QuerySync(ctx, nostr.Filter{
		Kinds: []int{kindAppMetadata},
		Tags:  nostr.TagMap{"d": []string{test.appID}},
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("query application %s: %v", test.appID, err)
	}
	if len(events) == 0 {
		t.Fatalf("application %s is absent from %s", test.appID, remoteRelayURL)
	}
	for _, event := range events {
		assertValidEvent(t, event)
		if tagValue(event, "repository") == test.repository {
			if !hasTag(event, "f", "android-arm64-v8a") {
				t.Fatalf("application event %s lacks android-arm64-v8a platform", event.ID)
			}
			return
		}
	}
	t.Fatalf("application %s has no signed event for repository %s", test.appID, test.repository)
}

func assertRelayAsset(t *testing.T, ctx context.Context, relay *nostr.Relay, test providerCase) {
	t.Helper()
	events, err := relay.QuerySync(ctx, nostr.Filter{
		Kinds: []int{kindSoftwareAsset},
		Tags:  nostr.TagMap{"i": []string{test.appID}},
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("query assets for %s: %v", test.appID, err)
	}
	host := test.relaySourceHost
	if host == "" {
		host = test.sourceHost
	}
	for _, event := range events {
		assertValidEvent(t, event)
		if !eventHasURLHost(event, host) {
			continue
		}
		if !sha256Pattern.MatchString(tagValue(event, "x")) {
			t.Fatalf("asset event %s has invalid SHA-256", event.ID)
		}
		if tagValue(event, "version") == "" || tagValue(event, "filename") == "" {
			t.Fatalf("asset event %s lacks version or filename", event.ID)
		}
		if tagValue(event, "m") != "application/vnd.android.package-archive" {
			t.Fatalf("asset event %s has invalid media type %q", event.ID, tagValue(event, "m"))
		}
		if !positiveTagNumber(event, "size") || !positiveTagNumber(event, "version_code") {
			t.Fatalf("asset event %s has invalid size or version_code", event.ID)
		}
		if !sha256Pattern.MatchString(tagValue(event, "apk_certificate_hash")) {
			t.Fatalf("asset event %s has invalid certificate hash", event.ID)
		}
		return
	}
	t.Fatalf("no signed APK asset for %s points to %s", test.appID, host)
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func assertAPK(t *testing.T, candidate *zsp.APK, test providerCase) {
	t.Helper()
	if candidate.AppID != test.appID {
		t.Fatalf("package ID = %q, want %q", candidate.AppID, test.appID)
	}
	if !sha256Pattern.MatchString(candidate.Hash) || !sha256Pattern.MatchString(candidate.CertificateHash) {
		t.Fatalf("invalid APK or certificate hash: apk=%q certificate=%q", candidate.Hash, candidate.CertificateHash)
	}
	if candidate.VersionName == "" || candidate.VersionCode <= 0 {
		t.Fatalf("invalid version: name=%q code=%d", candidate.VersionName, candidate.VersionCode)
	}
	if candidate.Size <= 1024 || candidate.MinSDK <= 0 || candidate.TargetSDK < candidate.MinSDK {
		t.Fatalf("invalid APK bounds: size=%d minSDK=%d targetSDK=%d", candidate.Size, candidate.MinSDK, candidate.TargetSDK)
	}
	if candidate.Name == "" || !strings.HasSuffix(strings.ToLower(candidate.Filename), ".apk") {
		t.Fatalf("invalid APK identity: name=%q filename=%q", candidate.Name, candidate.Filename)
	}
	// An empty list is an architecture-independent APK. Native APKs must
	// include arm64-v8a; Fetch has already rejected unsupported-only builds.
	if len(candidate.Architectures) > 0 && !contains(candidate.Architectures, "arm64-v8a") {
		t.Fatalf("architectures = %v, want universal or arm64-v8a", candidate.Architectures)
	}
	parsed, err := url.Parse(candidate.SourceURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != test.sourceHost {
		t.Fatalf("source URL = %q, want HTTPS host %s", candidate.SourceURL, test.sourceHost)
	}
}

func assertValidEvent(t *testing.T, event *nostr.Event) {
	t.Helper()
	valid, err := event.CheckSignature()
	if err != nil || !valid {
		t.Fatalf("invalid relay event %s signature: valid=%v err=%v", event.ID, valid, err)
	}
}

func eventHasURLHost(event *nostr.Event, host string) bool {
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "url" {
			continue
		}
		parsed, err := url.Parse(tag[1])
		if err == nil && parsed.Scheme == "https" && parsed.Hostname() == host {
			return true
		}
	}
	return false
}

func positiveTagNumber(event *nostr.Event, key string) bool {
	value, err := strconv.ParseInt(tagValue(event, key), 10, 64)
	return err == nil && value > 0
}

func tagValue(event *nostr.Event, key string) string {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func hasTag(event *nostr.Event, key, value string) bool {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func closeCandidates(t *testing.T, candidates []*zsp.APK) {
	t.Helper()
	for _, candidate := range candidates {
		if err := candidate.Close(); err != nil {
			t.Errorf("close candidate %s: %v", candidate.Filename, err)
		}
	}
}
