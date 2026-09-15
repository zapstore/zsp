//go:build remote

// Package remote_test exercises live source providers and the public Zapstore
// relay. It is intentionally excluded from the normal, hermetic test suite.
package remote_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	nostr "github.com/nbd-wtf/go-nostr"
	zsp "github.com/zapstore/zsp"
	internalconfig "github.com/zapstore/zsp/internal/config"
	internalnostr "github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/source"
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
	skipAppEvent    bool
	commit          string
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
			skipAppEvent:    true,
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
			commit:          "remote-e2e-forgejo",
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

	signerSecret := nostr.GeneratePrivateKey()
	signerPubkey, err := nostr.GetPublicKey(signerSecret)
	if err != nil {
		t.Fatalf("derive test signer pubkey: %v", err)
	}
	t.Setenv("SIGN_WITH", signerSecret)

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

			local := startTestRelay(t, signerPubkey)
			publishAndAssertLocal(t, ctx, local, signerPubkey, candidates[0], test)
		})
	}
}

func TestRemoteRepositoryZapstoreYAML(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	indexerConfig := &internalconfig.Config{
		Repository: "https://github.com/zapstore/zapstore",
		Name:       "indexer fallback",
		License:    "fallback",
	}
	resolved, err := source.ResolveIndexerConfig(ctx, indexerConfig)
	if err != nil {
		t.Fatalf("resolve repository zapstore.yaml: %v", err)
	}
	if resolved == indexerConfig {
		t.Fatal("repository zapstore.yaml was not loaded from $repo/zapstore.yaml")
	}
	if resolved.Repository != "https://github.com/zapstore/zapstore" {
		t.Fatalf("repository = %q", resolved.Repository)
	}
	if resolved.License != "MIT" {
		t.Fatalf("license = %q, want MIT from repository config", resolved.License)
	}
	if resolved.Name == indexerConfig.Name || resolved.License == indexerConfig.License {
		t.Fatal("repository config did not replace the indexer fallback")
	}
	if err := resolved.Validate(); err != nil {
		t.Fatalf("validate fetched repository config: %v", err)
	}
}

type localTestRelay struct {
	relayURL   string
	blossomURL string
	stateURL   string
}

func startTestRelay(t *testing.T, signerPubkey string) localTestRelay {
	t.Helper()
	binary := os.Getenv("TEST_RELAY_BINARY")
	if binary == "" {
		t.Fatal("TEST_RELAY_BINARY is required; run this suite with make test-remote")
	}
	relayAddress := unusedLoopbackAddress(t)
	blossomAddress := unusedLoopbackAddress(t)
	var logs bytes.Buffer
	command := exec.Command(binary)
	command.Stdout = &logs
	command.Stderr = &logs
	command.Env = append(os.Environ(),
		"RELAY_ADDRESS="+relayAddress,
		"RELAY_BLOSSOM_ADDRESS="+blossomAddress,
		"RELAY_BLOSSOM_HOSTNAME=127.0.0.1",
		"RELAY_AUTHORIZED_KEYS="+signerPubkey,
		"RELAY_BLOSSOM_AUTHORIZED_KEYS="+signerPubkey,
		"CATALOG_FIXTURE_DIR="+t.TempDir(),
		"CATALOG_SOURCE_DB="+filepath.Join(t.TempDir(), "missing.db"),
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start test-relay: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case err := <-done:
			if err != nil && !strings.Contains(err.Error(), "signal: interrupt") {
				t.Errorf("stop test-relay: %v\n%s", err, logs.String())
			}
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Errorf("test-relay did not stop gracefully\n%s", logs.String())
		}
	})

	stateURL := "http://" + relayAddress + "/_test/state"
	waitForTestRelay(t, stateURL, &logs)
	return localTestRelay{
		relayURL:   "ws://" + relayAddress,
		blossomURL: "http://" + blossomAddress,
		stateURL:   stateURL,
	}
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate loopback address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback address: %v", err)
	}
	return address
}

func waitForTestRelay(t *testing.T, stateURL string, logs *bytes.Buffer) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get(stateURL)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("test-relay did not become ready: %v\n%s", err, logs.String())
		case <-ticker.C:
		}
	}
}

func publishAndAssertLocal(
	t *testing.T,
	ctx context.Context,
	local localTestRelay,
	signerPubkey string,
	candidate *zsp.APK,
	test providerCase,
) {
	t.Helper()
	name := "Remote E2E " + test.name
	publishPhases := make(map[string]bool)
	publishCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, err := zsp.Publish(publishCtx, zsp.PublishConfig{
		Name:            name,
		Summary:         "Remote source integration test",
		Description:     "Published to the local test relay.",
		Tags:            []string{"remote", test.name},
		License:         "MIT",
		Website:         test.repository,
		SupportedNIPs:   []string{"01", "82"},
		MetadataSources: []string{},
	}, candidate, zsp.PublishOptions{
		BlossomURL:     local.blossomURL,
		Relays:         []string{local.relayURL},
		Commit:         test.commit,
		SkipAppEvent:   test.skipAppEvent,
		SkipProofCheck: true,
		OnProgress: func(progress zsp.Progress) {
			publishPhases[progress.Phase] = true
		},
	})
	if err != nil {
		t.Fatalf("publish remote candidate: %v (result: %+v)", err, result)
	}
	if result.Status != "published" || result.ID == "" || result.AppID != candidate.AppID {
		t.Fatalf("publish result = %+v", result)
	}
	if result.CertificateHash != candidate.CertificateHash || len(result.Uploads) == 0 {
		t.Fatalf("publish identity/uploads = %+v", result)
	}
	var apkUpload *zsp.BlobResult
	for index := range result.Uploads {
		upload := &result.Uploads[index]
		if !upload.Accepted || !sha256Pattern.MatchString(upload.Hash) || upload.Size <= 0 {
			t.Fatalf("blob upload = %+v", upload)
		}
		assertPublishedBlob(t, publishCtx, *upload)
		if upload.Type == "application/vnd.android.package-archive" && upload.Hash == candidate.Hash {
			apkUpload = upload
		}
	}
	if apkUpload == nil || apkUpload.Size != candidate.Size {
		t.Fatalf("no APK upload for candidate %s in %+v", candidate.Hash, result.Uploads)
	}
	assertLocalEvents(t, publishCtx, local.relayURL, signerPubkey, result, candidate, test, name, apkUpload.URL)
	assertOperationOrder(t, local.stateURL, test.skipAppEvent)
	assertPhases(t, "publish", publishPhases, "metadata", "upload", "relay")
}

func assertPublishedBlob(t *testing.T, ctx context.Context, upload zsp.BlobResult) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, upload.URL, nil)
	if err != nil {
		t.Fatalf("create blob request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("download published blob: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("published blob status = %d", response.StatusCode)
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, response.Body)
	if err != nil {
		t.Fatalf("hash published blob: %v", err)
	}
	if size != upload.Size || hex.EncodeToString(hasher.Sum(nil)) != upload.Hash {
		t.Fatalf("published blob size/hash = %d/%x, want %d/%s", size, hasher.Sum(nil), upload.Size, upload.Hash)
	}
}

func assertLocalEvents(
	t *testing.T,
	ctx context.Context,
	relayURL, signerPubkey string,
	result *zsp.PublishResult,
	candidate *zsp.APK,
	test providerCase,
	name string,
	apkURL string,
) {
	t.Helper()
	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		t.Fatalf("connect local relay: %v", err)
	}
	defer relay.Close()

	applications := queryLocalEvents(t, ctx, relay, nostr.Filter{
		Kinds:   []int{internalnostr.KindAppMetadata},
		Authors: []string{signerPubkey},
		Tags:    nostr.TagMap{"d": []string{candidate.AppID}},
	})
	assets := queryLocalEvents(t, ctx, relay, nostr.Filter{
		Kinds:   []int{internalnostr.KindSoftwareAsset},
		Authors: []string{signerPubkey},
		Tags:    nostr.TagMap{"i": []string{candidate.AppID}},
	})
	releases := queryLocalEvents(t, ctx, relay, nostr.Filter{
		Kinds:   []int{internalnostr.KindRelease},
		Authors: []string{signerPubkey},
		Tags:    nostr.TagMap{"i": []string{candidate.AppID}},
	})

	wantApplications := 1
	if test.skipAppEvent {
		wantApplications = 0
	}
	if len(applications) != wantApplications || len(assets) != 1 || len(releases) != 1 {
		t.Fatalf("local events: applications=%d assets=%d releases=%d", len(applications), len(assets), len(releases))
	}
	if test.skipAppEvent {
		if result.Events.Application != "" {
			t.Fatalf("skipped application event ID = %q", result.Events.Application)
		}
	} else {
		application := applications[0]
		assertSignedBy(t, application, signerPubkey)
		if result.Events.Application != application.ID || tagValue(application, "name") != name ||
			tagValue(application, "repository") != test.repository ||
			tagValue(application, "summary") != "Remote source integration test" ||
			application.Content != "Published to the local test relay." {
			t.Fatalf("application event = %+v", application)
		}
	}

	asset := assets[0]
	assertSignedBy(t, asset, signerPubkey)
	if len(result.Events.Assets) != 1 || result.Events.Assets[0] != asset.ID ||
		tagValue(asset, "i") != candidate.AppID ||
		tagValue(asset, "x") != candidate.Hash ||
		tagValue(asset, "version") != candidate.VersionName ||
		tagValue(asset, "version_code") != strconv.FormatInt(candidate.VersionCode, 10) ||
		tagValue(asset, "size") != strconv.FormatInt(candidate.Size, 10) ||
		tagValue(asset, "apk_certificate_hash") != candidate.CertificateHash ||
		tagValue(asset, "m") != "application/vnd.android.package-archive" {
		t.Fatalf("asset event = %+v", asset)
	}
	if hasTag(asset, "url", apkURL) {
		t.Fatalf("asset event contains derived Blossom URL %q: %+v", apkURL, asset)
	}
	if candidate.SourceURL == "" {
		if tagValue(asset, "url") != "" {
			t.Fatalf("asset event has a source URL for a local or ephemeral APK: %+v", asset)
		}
	} else if !hasTag(asset, "url", candidate.SourceURL) {
		t.Fatalf("asset event does not contain verified source URL %q: %+v", candidate.SourceURL, asset)
	}
	if len(candidate.Architectures) > 0 && !hasTagPrefix(asset, "f", "android") {
		t.Fatalf("native asset event has no Android platform tag: %+v", asset)
	}
	if test.commit != "" && tagValue(asset, "commit") != test.commit {
		t.Fatalf("asset commit = %q, want %q", tagValue(asset, "commit"), test.commit)
	}

	release := releases[0]
	assertSignedBy(t, release, signerPubkey)
	if result.ID != release.ID || result.Events.Release != release.ID ||
		tagValue(release, "d") != candidate.AppID+"@"+candidate.VersionName ||
		tagValue(release, "version") != candidate.VersionName ||
		tagValue(release, "version_code") != strconv.FormatInt(candidate.VersionCode, 10) ||
		tagValue(release, "c") != "main" ||
		!hasTag(release, "a", "32267:"+signerPubkey+":"+candidate.AppID) ||
		!hasTag(release, "e", asset.ID) {
		t.Fatalf("release event = %+v", release)
	}
}

func queryLocalEvents(t *testing.T, ctx context.Context, relay *nostr.Relay, filter nostr.Filter) []*nostr.Event {
	t.Helper()
	events, err := relay.QuerySync(ctx, filter)
	if err != nil {
		t.Fatalf("query local relay: %v", err)
	}
	return events
}

func assertSignedBy(t *testing.T, event *nostr.Event, pubkey string) {
	t.Helper()
	assertValidEvent(t, event)
	if event.PubKey != pubkey {
		t.Fatalf("event %s pubkey = %s, want %s", event.ID, event.PubKey, pubkey)
	}
}

func assertOperationOrder(t *testing.T, stateURL string, skipApp bool) {
	t.Helper()
	response, err := http.Get(stateURL)
	if err != nil {
		t.Fatalf("read test-relay state: %v", err)
	}
	defer response.Body.Close()
	var state struct {
		Operations []string `json:"operations"`
	}
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatalf("decode test-relay state: %v", err)
	}
	want := []string{"blob"}
	if !skipApp {
		want = append(want, "event:32267")
	}
	want = append(want, "event:3063", "event:30063")
	if len(state.Operations) < len(want) {
		t.Fatalf("operations = %v, want suffix %v", state.Operations, want)
	}
	got := state.Operations[len(state.Operations)-len(want):]
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("operations suffix = %v, want %v", got, want)
		}
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
		Kinds: []int{internalnostr.KindAppMetadata},
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
		Kinds: []int{internalnostr.KindSoftwareAsset},
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

func hasTagPrefix(event *nostr.Event, key, prefix string) bool {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == key && strings.HasPrefix(tag[1], prefix) {
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
