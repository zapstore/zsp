//go:build publish && !race

// Publish assertions against the relay started by TestMain.
// go-nostr's Relay.Close races under -race, so this file stays out of that run.
package publish

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	keystore "github.com/pavlo-v-chernykh/keystore-go/v4"
	"github.com/zapstore/zsp"
)

const (
	kindAppMetadata   = 32267
	kindRelease       = 30063
	kindSoftwareAsset = 3063
	kindIdentityProof = 30509
)

func TestMain(m *testing.M) {
	relayURL, blossomURL, stop, err := startLocalRelay()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("RELAY_URL", relayURL)
	os.Setenv("BLOSSOM_URL", blossomURL)
	code := m.Run()
	stop()
	os.Exit(code)
}

func TestClientAPIPublishesThroughLocalRelay(t *testing.T) {
	relayURL := os.Getenv("RELAY_URL")
	blossomURL := os.Getenv("BLOSSOM_URL")
	if relayURL == "" || blossomURL == "" {
		t.Fatal("RELAY_URL and BLOSSOM_URL are required; run make test-integration")
	}

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
		"release_source: %q\nname: E2E App\nsummary: Published through the local relay\nmetadata_sources: []\n",
		firstAPK,
	)
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := zsp.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("SIGN_WITH", ownerSecret)
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
	if equalResult.Events.Application != "" {
		t.Fatalf("unchanged application published %q", equalResult.Events.Application)
	}
	if !containsWarning(equalResult.Warnings, "application event unchanged") {
		t.Fatalf("overwrite equal version warnings = %v, want application event unchanged", equalResult.Warnings)
	}
	if len(equalResult.Uploads) != 1 {
		t.Fatalf("unchanged application uploads = %+v, want APK only", equalResult.Uploads)
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
	t.Setenv("SIGN_WITH", ownerSecret)
	retryResult, err := zsp.Publish(t.Context(), config.PublishConfig, retryCandidate, zsp.PublishOptions{
		BlossomURL:     blossomURL,
		Relays:         []string{relayURL},
		SkipProofCheck: true,
	})
	if err != nil || retryResult.Status != "published" {
		t.Fatalf("retry same APK: result=%+v error=%v", retryResult, err)
	}
	assertReleasePublishedBy(t, relayURL, retryResult.ID, ownerPubkey)
	assertPublishedBlob(t, retryResult, mustRead(t, retryAPK))

	unchangedAPK := buildE2EAPK(t, apkDirectory, keystore, 46)
	unchangedFetchConfig := config.FetchConfig
	unchangedSource := *unchangedFetchConfig.ReleaseSource
	unchangedSource.LocalPath = unchangedAPK
	unchangedFetchConfig.ReleaseSource = &unchangedSource
	unchanged := fetchOneE2EAPK(t, unchangedFetchConfig)
	unchangedResult, err := zsp.Publish(t.Context(), config.PublishConfig, unchanged, zsp.PublishOptions{
		BlossomURL:     blossomURL,
		Relays:         []string{relayURL},
		SkipProofCheck: true,
	})
	if err != nil || unchangedResult.Status != "published" {
		t.Fatalf("unchanged application Publish: result=%+v error=%v", unchangedResult, err)
	}
	if unchangedResult.Events.Application != "" {
		t.Fatalf("version bump republished application %q", unchangedResult.Events.Application)
	}
	if !containsWarning(unchangedResult.Warnings, "application event unchanged") {
		t.Fatalf("version bump warnings = %v, want application event unchanged", unchangedResult.Warnings)
	}

	forcedAPK := buildE2EAPK(t, apkDirectory, keystore, 47)
	forcedFetchConfig := config.FetchConfig
	forcedSource := *forcedFetchConfig.ReleaseSource
	forcedSource.LocalPath = forcedAPK
	forcedFetchConfig.ReleaseSource = &forcedSource
	forced := fetchOneE2EAPK(t, forcedFetchConfig)
	forcedResult, err := zsp.Publish(t.Context(), config.PublishConfig, forced, zsp.PublishOptions{
		BlossomURL:        blossomURL,
		Relays:            []string{relayURL},
		SkipProofCheck:    true,
		OverwriteAppEvent: true,
	})
	if err != nil || forcedResult.Status != "published" || forcedResult.Events.Application == "" {
		t.Fatalf("overwrite application: result=%+v error=%v", forcedResult, err)
	}
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

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
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
	libDir := filepath.Join(buildDirectory, "lib", "arm64-v8a")
	if err := os.MkdirAll(libDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libe2e.so"), []byte("e2e"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsignedAPK := filepath.Join(buildDirectory, "unsigned.apk")
	runE2ECommand(t, aapt, "package", "-f", "-M", manifest, "-I", androidJar, "-F", unsignedAPK)
	addNativeLib := exec.Command(aapt, "add", "-f", unsignedAPK, "lib/arm64-v8a/libe2e.so")
	addNativeLib.Dir = buildDirectory
	if output, err := addNativeLib.CombinedOutput(); err != nil {
		t.Fatalf("aapt add failed: %v\n%s", err, output)
	}

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

func loadE2ECertificate(t *testing.T, path string) (crypto.PrivateKey, *x509.Certificate) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store := keystore.New()
	if err := store.Load(bytes.NewReader(data), []byte("storepass")); err != nil {
		t.Fatal(err)
	}
	entry, err := store.GetPrivateKeyEntry("release", []byte("storepass"))
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.ParsePKCS8PrivateKey(entry.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.CertificateChain) == 0 {
		t.Fatal("JKS alias release has no certificate")
	}
	certificate, err := x509.ParseCertificate(entry.CertificateChain[0].Content)
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
	rsaKey, ok := certificateKey.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("certificate key type = %T, want RSA", certificateKey)
	}
	createdAt := time.Now().Unix()
	expiry := createdAt + int64(time.Hour.Seconds())
	sum := sha256.Sum256(certificate.Raw)
	message := fmt.Sprintf("Verifying at %d until %d that I control the following Nostr public key: %s", createdAt, expiry, ownerPubkey)
	digest := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	event := &gonostr.Event{
		Kind:      kindIdentityProof,
		PubKey:    ownerPubkey,
		CreatedAt: gonostr.Timestamp(createdAt),
		Tags: gonostr.Tags{
			{"d", hex.EncodeToString(sum[:])},
			{"signature", base64.StdEncoding.EncodeToString(signature)},
			{"expiry", strconv.FormatInt(expiry, 10)},
			{"delegation", delegatePubkey},
		},
	}
	if err := event.Sign(ownerSecret); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	relay, err := gonostr.RelayConnect(ctx, relayURL)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if err := relay.Publish(ctx, *event); err != nil {
		t.Fatalf("publish C1 proof: %v", err)
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
		Kinds: []int{kindAppMetadata},
		Tags:  gonostr.TagMap{"d": []string{"com.example.zspe2e"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	releases, err := relay.QuerySync(ctx, gonostr.Filter{
		Kinds:   []int{kindRelease},
		Authors: publisherPubkeys,
		Tags:    gonostr.TagMap{"i": []string{"com.example.zspe2e"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assets, err := relay.QuerySync(ctx, gonostr.Filter{
		Kinds:   []int{kindSoftwareAsset},
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

func containsWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if warning == want {
			return true
		}
	}
	return false
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
		Kinds:   []int{kindRelease},
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
