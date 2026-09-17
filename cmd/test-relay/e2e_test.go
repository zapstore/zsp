//go:build !race

// The pinned go-nostr Relay.Close has an upstream race between connection
// shutdown goroutines. Keep this network boundary in the normal suite while
// race-testing ZSP's packages separately.
package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/identity"
	internalnostr "github.com/zapstore/zsp/internal/nostr"
)

func TestClientAPIPublishesThroughTestRelayEndToEnd(t *testing.T) {
	relayAddress := unusedLoopbackAddress(t)
	blossomAddress := unusedLoopbackAddress(t)
	relayURL := "ws://" + relayAddress
	blossomURL := "http://" + blossomAddress

	ownerSecret := gonostr.GeneratePrivateKey()
	ownerPubkey, err := gonostr.GetPublicKey(ownerSecret)
	if err != nil {
		t.Fatal(err)
	}
	delegateSecret := gonostr.GeneratePrivateKey()
	delegatePubkey, err := gonostr.GetPublicKey(delegateSecret)
	if err != nil {
		t.Fatal(err)
	}
	indexerSecret := gonostr.GeneratePrivateKey()
	indexerPubkey, err := gonostr.GetPublicKey(indexerSecret)
	if err != nil {
		t.Fatal(err)
	}
	partialSecret := gonostr.GeneratePrivateKey()
	partialPubkey, err := gonostr.GetPublicKey(partialSecret)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("RELAY_ADDRESS", relayAddress)
	t.Setenv("RELAY_BLOSSOM_ADDRESS", blossomAddress)
	t.Setenv("RELAY_BLOSSOM_HOSTNAME", strings.Split(blossomAddress, ":")[0])
	t.Setenv("RELAY_AUTHORIZED_KEYS", ownerPubkey+","+delegatePubkey+","+indexerPubkey)
	t.Setenv("RELAY_BLOSSOM_AUTHORIZED_KEYS", ownerPubkey+","+delegatePubkey+","+indexerPubkey+","+partialPubkey)
	t.Setenv("CATALOG_FIXTURE_DIR", t.TempDir())
	t.Setenv("CATALOG_SOURCE_DB", filepath.Join(t.TempDir(), "missing.db"))
	t.Setenv("SIGN_WITH", ownerSecret)

	serverCtx, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx)
	}()
	t.Cleanup(func() {
		stopServer()
		select {
		case err := <-serverDone:
			if err != nil {
				t.Errorf("stop test relay: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("test relay did not stop")
		}
	})
	waitForTestRelay(t, "http://"+relayAddress+"/_test/state", serverDone)

	apkDirectory := t.TempDir()
	keystore := filepath.Join(apkDirectory, "release.jks")
	firstAPK := buildE2EAPK(t, apkDirectory, keystore, 42)
	certificateKey, certificate := loadE2ECertificate(t, keystore)
	publishE2EC1Proof(t, relayURL, ownerSecret, ownerPubkey, delegatePubkey, certificateKey, certificate)

	firstBytes, err := os.ReadFile(firstAPK)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(apkDirectory, "zapstore.yaml")
	configContents := fmt.Sprintf(
		"release_source: %q\nname: E2E App\nsummary: Published through test-relay\nmetadata_sources: []\n",
		firstAPK,
	)
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := zsp.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}

	first := fetchOneE2EAPK(t, config.FetchConfig)
	firstResult := publishE2EAPK(t, config.PublishConfig, first, blossomURL, relayURL, false)
	assertPublishedBlob(t, firstResult, firstBytes)
	assertRelayPublication(t, relayURL, ownerPubkey, []string{ownerPubkey, delegatePubkey}, 1, 1, 1)

	equal := fetchOneE2EAPK(t, config.FetchConfig)
	_, err = zsp.Publish(t.Context(), config.PublishConfig, equal, zsp.PublishOptions{
		BlossomURL: blossomURL,
		Relays:     []string{relayURL},
	})
	if !errors.Is(err, zsp.ErrAlreadyPublished) {
		t.Fatalf("equal version Publish error = %v, want ErrAlreadyPublished", err)
	}
	equalResult, err := zsp.Publish(t.Context(), config.PublishConfig, equal, zsp.PublishOptions{
		BlossomURL:       blossomURL,
		Relays:           []string{relayURL},
		OverwriteRelease: true,
	})
	if err != nil || equalResult.Status != "published" {
		t.Fatalf("overwrite equal version: result=%+v error=%v", equalResult, err)
	}

	downgradeAPK := buildE2EAPK(t, apkDirectory, keystore, 41)
	downgradeFetchConfig := config.FetchConfig
	downgradeSource := *downgradeFetchConfig.ReleaseSource
	downgradeSource.LocalPath = downgradeAPK
	downgradeFetchConfig.ReleaseSource = &downgradeSource
	downgrade := fetchOneE2EAPK(t, downgradeFetchConfig)
	for _, overwrite := range []bool{false, true} {
		_, err := zsp.Publish(t.Context(), config.PublishConfig, downgrade, zsp.PublishOptions{
			BlossomURL:       blossomURL,
			Relays:           []string{relayURL},
			OverwriteRelease: overwrite,
		})
		if !errors.Is(err, zsp.ErrReleaseDowngrade) {
			t.Fatalf("downgrade Publish(overwrite=%v) error = %v, want ErrReleaseDowngrade", overwrite, err)
		}
	}
	unreachableRelay := "ws://" + unusedLoopbackAddress(t)
	_, err = zsp.Publish(t.Context(), config.PublishConfig, downgrade, zsp.PublishOptions{
		BlossomURL:     blossomURL,
		Relays:         []string{unreachableRelay},
		SkipProofCheck: true,
	})
	if !errors.Is(err, zsp.ErrTemporaryFailure) {
		t.Fatalf("relay lookup failure = %v, want ErrTemporaryFailure", err)
	}

	secondAPK := buildE2EAPK(t, apkDirectory, keystore, 43)
	secondBytes, err := os.ReadFile(secondAPK)
	if err != nil {
		t.Fatal(err)
	}
	secondFetchConfig := config.FetchConfig
	secondSource := *secondFetchConfig.ReleaseSource
	secondSource.LocalPath = secondAPK
	secondFetchConfig.ReleaseSource = &secondSource
	second := fetchOneE2EAPK(t, secondFetchConfig)
	t.Setenv("SIGN_WITH", delegateSecret)
	secondResult, err := zsp.Publish(t.Context(), config.PublishConfig, second, zsp.PublishOptions{
		BlossomURL:   blossomURL,
		Relays:       []string{relayURL, unreachableRelay},
		SkipAppEvent: true,
	})
	if err != nil {
		t.Fatalf("partial-relay Publish: %v (result: %+v)", err, secondResult)
	}
	if len(secondResult.Warnings) == 0 {
		t.Fatal("partial-relay Publish returned no warnings")
	}
	assertPublishedBlob(t, secondResult, secondBytes)
	assertRelayPublication(t, relayURL, ownerPubkey, []string{ownerPubkey, delegatePubkey}, 1, 2, 3)
	if secondResult.Events.Application != "" {
		t.Fatalf("skip-app publication returned application event %q", secondResult.Events.Application)
	}

	thirdAPK := buildE2EAPK(t, apkDirectory, keystore, 44)
	thirdFetchConfig := config.FetchConfig
	thirdSource := *thirdFetchConfig.ReleaseSource
	thirdSource.LocalPath = thirdAPK
	thirdFetchConfig.ReleaseSource = &thirdSource
	third := fetchOneE2EAPK(t, thirdFetchConfig)
	t.Setenv("SIGN_WITH", indexerSecret)
	thirdResult, err := zsp.Publish(t.Context(), config.PublishConfig, third, zsp.PublishOptions{
		BlossomURL:     blossomURL,
		Relays:         []string{relayURL},
		SkipAppEvent:   true,
		SkipProofCheck: true,
	})
	if err != nil {
		t.Fatalf("indexer-style Publish: %v (result: %+v)", err, thirdResult)
	}
	if thirdResult.Status != "published" || thirdResult.ID == "" {
		t.Fatalf("indexer-style Publish result = %+v", thirdResult)
	}
	assertReleasePublishedBy(t, relayURL, thirdResult.ID, indexerPubkey)

	retryAPK := buildE2EAPK(t, apkDirectory, keystore, 45)
	retryFetchConfig := config.FetchConfig
	retrySource := *retryFetchConfig.ReleaseSource
	retrySource.LocalPath = retryAPK
	retryFetchConfig.ReleaseSource = &retrySource
	retryCandidate := fetchOneE2EAPK(t, retryFetchConfig)
	t.Setenv("SIGN_WITH", partialSecret)
	partialResult, err := zsp.Publish(t.Context(), config.PublishConfig, retryCandidate, zsp.PublishOptions{
		BlossomURL:     blossomURL,
		Relays:         []string{relayURL},
		SkipProofCheck: true,
	})
	if !errors.Is(err, zsp.ErrPublishRejected) {
		t.Fatalf("partial Publish error = %v, want ErrPublishRejected", err)
	}
	if partialResult == nil || partialResult.Status != "partial" || len(partialResult.Uploads) != 1 ||
		!partialResult.Uploads[0].Accepted {
		t.Fatalf("partial Publish result = %+v", partialResult)
	}

	t.Setenv("SIGN_WITH", indexerSecret)
	retryResult, err := zsp.Publish(t.Context(), config.PublishConfig, retryCandidate, zsp.PublishOptions{
		BlossomURL:     blossomURL,
		Relays:         []string{relayURL},
		SkipProofCheck: true,
	})
	if err != nil || retryResult.Status != "published" {
		t.Fatalf("retry same APK: result=%+v error=%v", retryResult, err)
	}
	assertOperationSuffix(t, "http://"+relayAddress+"/_test/state", []string{
		"blob",
		"event:32267",
		"event:3063",
		"event:30063",
	})
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitForTestRelay(t *testing.T, stateURL string, serverDone <-chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-serverDone:
			t.Fatalf("test relay stopped during startup: %v", err)
		default:
		}
		response, err := http.Get(stateURL)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test relay did not become ready")
}

func buildE2EAPK(t *testing.T, directory, keystore string, versionCode int64) string {
	t.Helper()
	root := "/opt/homebrew/share/android-commandlinetools"
	aapt := e2eAndroidTool(t, filepath.Join(root, "build-tools", "*", "aapt"))
	apksigner := e2eAndroidTool(t, filepath.Join(root, "build-tools", "*", "apksigner"))
	androidJar := e2eAndroidTool(t, filepath.Join(root, "platforms", "android-*", "android.jar"))

	buildDirectory := filepath.Join(directory, strconv.FormatInt(versionCode, 10))
	if err := os.MkdirAll(buildDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(buildDirectory, "AndroidManifest.xml")
	manifestXML := fmt.Sprintf(
		`<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="com.example.zspe2e" android:versionCode="%d" android:versionName="1.%d"><uses-sdk android:minSdkVersion="23" android:targetSdkVersion="35"/><application android:label="ZSP E2E"/></manifest>`,
		versionCode,
		versionCode,
	)
	if err := os.WriteFile(manifest, []byte(manifestXML), 0o600); err != nil {
		t.Fatal(err)
	}
	unsignedAPK := filepath.Join(buildDirectory, "unsigned.apk")
	runE2ECommand(t, aapt, "package", "-f", "-M", manifest, "-I", androidJar, "-F", unsignedAPK)

	if _, err := os.Stat(keystore); os.IsNotExist(err) {
		runE2ECommand(t, "keytool", "-genkeypair", "-alias", "release", "-keystore", keystore,
			"-storetype", "JKS", "-storepass", "storepass", "-keypass", "storepass",
			"-dname", "CN=zsp-e2e", "-keyalg", "RSA", "-keysize", "2048", "-validity", "1", "-noprompt")
	} else if err != nil {
		t.Fatal(err)
	}

	signedAPK := filepath.Join(buildDirectory, "app.apk")
	runE2ECommand(t, apksigner, "sign", "--ks", keystore, "--ks-key-alias", "release",
		"--ks-pass", "pass:storepass", "--key-pass", "pass:storepass", "--out", signedAPK, unsignedAPK)
	runE2ECommand(t, apksigner, "verify", "--verbose", signedAPK)
	return signedAPK
}

func e2eAndroidTool(t *testing.T, pattern string) string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	for index := len(matches) - 1; index >= 0; index-- {
		if info, err := os.Stat(matches[index]); err == nil && !info.IsDir() {
			return matches[index]
		}
	}
	t.Fatalf("required Android tool not found: %s", pattern)
	return ""
}

func runE2ECommand(t *testing.T, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", filepath.Base(name), err, output)
	}
}

func loadE2ECertificate(t *testing.T, keystore string) (crypto.PrivateKey, *x509.Certificate) {
	t.Helper()
	privateKey, certificate, err := identity.LoadJKSFile(keystore, "storepass", "storepass", "release")
	if err != nil {
		t.Fatal(err)
	}
	return privateKey, certificate
}

func publishE2EC1Proof(
	t *testing.T,
	relayURL, ownerSecret, ownerPubkey, delegatePubkey string,
	certificateKey crypto.PrivateKey,
	certificate *x509.Certificate,
) {
	t.Helper()
	proof, err := identity.GenerateIdentityProof(
		certificateKey,
		certificate,
		ownerPubkey,
		&identity.IdentityProofOptions{Expiry: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	event := internalnostr.BuildIdentityProofEvent(proof.ToEventTags(), ownerPubkey, proof.CreatedAt)
	event.Tags = append(event.Tags, gonostr.Tag{"delegation", delegatePubkey})
	if err := event.Sign(ownerSecret); err != nil {
		t.Fatal(err)
	}
	results := internalnostr.NewPublisher([]string{relayURL}).Publish(t.Context(), event)
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("publish C1 proof = %+v", results)
	}
}

func fetchOneE2EAPK(t *testing.T, config zsp.FetchConfig) *zsp.APK {
	t.Helper()
	candidates, err := zsp.Fetch(t.Context(), config, zsp.FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("Fetch returned %d candidates, want 1", len(candidates))
	}
	t.Cleanup(func() {
		_ = candidates[0].Close()
	})
	return candidates[0]
}

func publishE2EAPK(
	t *testing.T,
	config zsp.PublishConfig,
	candidate *zsp.APK,
	blossomURL, relayURL string,
	skipApp bool,
) *zsp.PublishResult {
	t.Helper()
	result, err := zsp.Publish(t.Context(), config, candidate, zsp.PublishOptions{
		BlossomURL:   blossomURL,
		Relays:       []string{relayURL},
		SkipAppEvent: skipApp,
	})
	if err != nil {
		t.Fatalf("Publish: %v (result: %+v)", err, result)
	}
	if result.Status != "published" || result.ID == "" {
		t.Fatalf("Publish result = %+v", result)
	}
	return result
}

func assertPublishedBlob(t *testing.T, result *zsp.PublishResult, expected []byte) {
	t.Helper()
	if len(result.Uploads) != 1 || !result.Uploads[0].Accepted {
		t.Fatalf("uploads = %+v, want one accepted APK", result.Uploads)
	}
	response, err := http.Get(result.Uploads[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	actual, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(actual, expected) {
		t.Fatalf("downloaded blob status=%d bytes=%d, want status=200 bytes=%d", response.StatusCode, len(actual), len(expected))
	}
}

func assertRelayPublication(
	t *testing.T,
	relayURL, applicationPubkey string,
	publisherPubkeys []string,
	applicationCount, releaseCount, assetCount int,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	relay, err := gonostr.RelayConnect(ctx, relayURL)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	applications, err := relay.QuerySync(ctx, gonostr.Filter{
		Kinds: []int{internalnostr.KindAppMetadata},
		Tags:  gonostr.TagMap{"d": []string{"com.example.zspe2e"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	releases, err := relay.QuerySync(ctx, gonostr.Filter{
		Kinds:   []int{internalnostr.KindRelease},
		Authors: publisherPubkeys,
		Tags:    gonostr.TagMap{"i": []string{"com.example.zspe2e"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assets, err := relay.QuerySync(ctx, gonostr.Filter{
		Kinds:   []int{internalnostr.KindSoftwareAsset},
		Authors: publisherPubkeys,
		Tags:    gonostr.TagMap{"i": []string{"com.example.zspe2e"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applications) != applicationCount || len(releases) != releaseCount || len(assets) != assetCount {
		t.Fatalf("relay events: applications=%d releases=%d assets=%d", len(applications), len(releases), len(assets))
	}
	for _, application := range applications {
		valid, err := application.CheckSignature()
		if err != nil || !valid || application.PubKey != applicationPubkey {
			t.Fatalf("invalid application event: pubkey=%s signature_error=%v", application.PubKey, err)
		}
	}
	assetIDs := make(map[string]struct{}, len(assets))
	for _, asset := range assets {
		valid, err := asset.CheckSignature()
		if err != nil || !valid {
			t.Fatalf("invalid asset signature: %v", err)
		}
		assetIDs[asset.ID] = struct{}{}
	}
	for _, release := range releases {
		valid, err := release.CheckSignature()
		if err != nil || !valid {
			t.Fatalf("invalid release signature: %v", err)
		}
		application := release.Tags.GetFirst([]string{"a"})
		if application == nil || len(*application) < 2 ||
			(*application)[1] != "32267:"+applicationPubkey+":com.example.zspe2e" {
			t.Fatalf("release application coordinate = %v", application)
		}
		linked := false
		for _, tag := range release.Tags {
			if len(tag) >= 2 && tag[0] == "e" {
				if _, ok := assetIDs[tag[1]]; ok {
					linked = true
				}
			}
		}
		if !linked {
			t.Fatalf("release %s does not link a published asset", release.ID)
		}
	}
}

func assertReleasePublishedBy(t *testing.T, relayURL, eventID, pubkey string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	relay, err := gonostr.RelayConnect(ctx, relayURL)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	events, err := relay.QuerySync(ctx, gonostr.Filter{
		IDs:     []string{eventID},
		Kinds:   []int{internalnostr.KindRelease},
		Authors: []string{pubkey},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("release %s by %s not found", eventID, pubkey)
	}
	valid, err := events[0].CheckSignature()
	if err != nil || !valid {
		t.Fatalf("invalid indexer-style release signature: %v", err)
	}
}

func assertOperationSuffix(t *testing.T, stateURL string, want []string) {
	t.Helper()
	response, err := http.Get(stateURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var state struct {
		Operations []string `json:"operations"`
	}
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
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
