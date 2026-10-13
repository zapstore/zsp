package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/identity"
	"github.com/zapstore/zsp/internal/ui"
)

func TestWizardContinuingNotesNameReachableRelays(t *testing.T) {
	relays := []string{"wss://one.example?token=secret", "wss://two.example", "wss://three.example"}
	got := wizardContinuingNotes(relays, []string{"wss://two.example"})
	want := "Continuing with wss://one.example, wss://three.example"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("wizardContinuingNotes() = %v, want %q", got, want)
	}
}

func TestWizardErrorNsecSetupExplainsEnvAndExitsCleanly(t *testing.T) {
	if code := wizardError("Identity setup stopped", fmt.Errorf("signer: %w", errNsecSetup)); code != 0 {
		t.Fatalf("wizardError(errNsecSetup) = %d, want 0", code)
	}
	notes := strings.Join(wizardNsecNotes(), "\n")
	for _, want := range []string{"SIGN_WITH=nsec1", "chmod 600 .env", ".gitignore", "bunker is safer"} {
		if !strings.Contains(notes, want) {
			t.Errorf("nsec notes do not mention %q:\n%s", want, notes)
		}
	}
}

func TestWizardStateUsesHotboxBeforeKeystore(t *testing.T) {
	t.Setenv("HOTBOX_SOCKET", filepath.Join(t.TempDir(), "hotbox.sock"))
	t.Setenv("HOTBOX_PIN", strings.Repeat("p", 32))
	state := &wizardIdentityState{certificateHash: strings.Repeat("c", 64)}
	if err := wizardCertificateKey(state, "unused", false); err != nil {
		t.Fatalf("wizardCertificateKey() error = %v", err)
	}
	if state.hotbox == nil || state.privateKey != nil || !state.hasCertificateKey() {
		t.Fatalf("state = %#v, want hotbox and no keystore", state)
	}
	if _, ok := state.certificateSigner().(hotboxSigner); !ok {
		t.Fatalf("certificateSigner() = %T, want hotboxSigner", state.certificateSigner())
	}
}

func TestWizardConfigFromSource(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		repository    string
		releaseSource string
		wantError     bool
	}{
		{name: "repository without scheme", source: "github.com/example/app", repository: "https://github.com/example/app"},
		{name: "GitHub releases page", source: "github.com/GreenArt7c3/Amber/releases", repository: "https://github.com/greenart7c3/amber"},
		{name: "F-Droid package", source: "https://f-droid.org/packages/com.example.app", releaseSource: "https://f-droid.org/packages/com.example.app"},
		{name: "direct APK URL", source: "https://downloads.example.com/app.apk", releaseSource: "https://downloads.example.com/app.apk"},
		{name: "empty", source: " ", wantError: true},
		{name: "insecure URL", source: "http://example.com/releases", wantError: true},
		{name: "missing local file", source: "./missing.apk", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := wizardConfigFromSource(test.source)
			if test.wantError {
				if err == nil {
					t.Fatal("wizardConfigFromSource() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("wizardConfigFromSource() error = %v", err)
			}
			if got.Repository != test.repository {
				t.Errorf("Repository = %q, want %q", got.Repository, test.repository)
			}
			gotReleaseSource := ""
			if got.ReleaseSource != nil {
				gotReleaseSource = got.ReleaseSource.URL
			}
			if gotReleaseSource != test.releaseSource {
				t.Errorf("ReleaseSource = %q, want %q", gotReleaseSource, test.releaseSource)
			}
		})
	}
}

func TestWizardConfigFromSourceAcceptsLocalAPK(t *testing.T) {
	directory := t.TempDir()
	apkPath := filepath.Join(directory, "app-release.apk")
	if err := os.WriteFile(apkPath, []byte("apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	notAPK := filepath.Join(directory, "notes.txt")
	if err := os.WriteFile(notAPK, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directory, apkPath} {
		got, err := wizardConfigFromSource(path)
		if err != nil {
			t.Fatalf("wizardConfigFromSource(%q) error = %v", path, err)
		}
		if got.ReleaseSource == nil || got.ReleaseSource.LocalPath != path || got.Repository != "" {
			t.Fatalf("wizardConfigFromSource(%q) = %#v", path, got)
		}
	}
	if _, err := wizardConfigFromSource(notAPK); err == nil {
		t.Fatal("wizardConfigFromSource() accepted a file that is not an APK")
	}
}

func TestLoadWizardConfigWithoutFile(t *testing.T) {
	cfg, path, err := loadWizardConfig(filepath.Join(t.TempDir(), "zapstore.yaml"))
	if err != nil || path != "" || cfg.Fetch.Repository != "" {
		t.Fatalf("loadWizardConfig() = (%#v, %q, %v), want empty", cfg, path, err)
	}
}

func TestParseWizardRepoConfigResolvesFromRepositoryRoot(t *testing.T) {
	const repository = "https://github.com/example/app"
	data := []byte(`repository: https://github.com/example/app
summary: Notes
icon: ./assets/icon.png
images:
  - assets/one.png
  - https://cdn.example.com/two.png
release_notes: CHANGELOG.md
`)
	cfg, ok := parseWizardRepoConfig(data, repository)
	if !ok {
		t.Fatal("parseWizardRepoConfig() rejected a valid file")
	}
	const raw = "https://raw.githubusercontent.com/example/app/HEAD/"
	if cfg.Publish.Icon != raw+"assets/icon.png" {
		t.Errorf("Icon = %q", cfg.Publish.Icon)
	}
	if strings.Join(cfg.Publish.Images, ",") != raw+"assets/one.png,https://cdn.example.com/two.png" {
		t.Errorf("Images = %v", cfg.Publish.Images)
	}
	if cfg.Publish.ReleaseNotes != raw+"CHANGELOG.md" {
		t.Errorf("ReleaseNotes = %q", cfg.Publish.ReleaseNotes)
	}
	if cfg.Publish.Summary != "Notes" || cfg.Fetch.Repository != repository {
		t.Errorf("config = %#v", cfg)
	}
}

func TestParseWizardRepoConfigRejectsInvalidFiles(t *testing.T) {
	for name, data := range map[string]string{
		"local release source": "repository: https://github.com/example/app\nrelease_source: ./app-release.apk\n",
		"invalid YAML":         "repository: [\n",
		"unknown metadata":     "repository: https://github.com/example/app\nmetadata_sources: [nowhere]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := parseWizardRepoConfig([]byte(data), "https://github.com/example/app"); ok {
				t.Fatal("parseWizardRepoConfig() accepted an invalid file")
			}
		})
	}
}

func TestWizardHandoffNotesPointAtSkill(t *testing.T) {
	title, notes := wizardHandoffNotes("Notes", false, true)
	if title != "Zapstore will keep Notes up to date" {
		t.Fatalf("title = %q", title)
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "shown as developed by you") || !strings.Contains(notes[1], `"run zsp skill and write a zapstore.yaml"`) {
		t.Fatalf("notes = %v", notes)
	}
	_, notes = wizardHandoffNotes("Notes", true, true)
	if !strings.Contains(notes[1], "improve the zapstore.yaml") {
		t.Fatalf("with config notes = %v", notes)
	}
	title, notes = wizardHandoffNotes("Notes", false, false)
	if title != "You'll publish new releases of Notes" || !strings.Contains(notes[1], "run zsp publish after each release") {
		t.Fatalf("without Zapstore relay = (%q, %v)", title, notes)
	}
}

func TestWizardNextReleaseChoicesDependOnZapstoreRelay(t *testing.T) {
	choices := wizardNextReleaseChoices(true)
	if len(choices) != 2 || choices[0] != "Let Zapstore publish them for you (recommended)" || choices[1] != wizardChoiceCICD {
		t.Fatalf("with Zapstore relay = %v", choices)
	}
	choices = wizardNextReleaseChoices(false)
	if len(choices) != 2 || choices[0] != "Publish them yourself with zsp publish" || choices[1] != wizardChoiceCICD {
		t.Fatalf("without Zapstore relay = %v", choices)
	}
}

func TestHasZapstoreRelay(t *testing.T) {
	for relays, want := range map[string]bool{
		"wss://relay.zapstore.dev":                      true,
		"wss://RELAY.zapstore.dev/,wss://other.example": true,
		"wss://other.example":                           false,
		"/tmp/relay.sock":                               false,
		"wss://relay.zapstore.dev.evil.example":         false,
	} {
		if got := hasZapstoreRelay(strings.Split(relays, ",")); got != want {
			t.Errorf("hasZapstoreRelay(%q) = %v, want %v", relays, got, want)
		}
	}
}

func TestKeystoreProofSignerSignsVerifiableProof(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "wizard"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := strings.Repeat("a", 64)
	createdAt := time.Now().Unix()
	expiry := createdAt + 3600
	signed, err := keystoreProofSigner{privateKey: key, certificate: certificate, publicKey: publicKey}.SignProof(t.Context(), createdAt, expiry)
	if err != nil {
		t.Fatal(err)
	}
	if signed.PublicKey != publicKey || signed.CertificateHash != identity.ComputeCertHash(certificate) {
		t.Fatalf("signed = %+v", signed)
	}
	proof := &identity.IdentityProof{CertHash: signed.CertificateHash, Signature: base64.StdEncoding.EncodeToString(signed.Signature), CreatedAt: createdAt, Expiry: expiry}
	if result := identity.VerifyIdentityProofWithCert(proof, nil, publicKey, certificate); !result.Valid {
		t.Fatalf("proof does not verify: %v", result.Error)
	}
}

func TestWizardReleaseLocation(t *testing.T) {
	tests := []struct {
		name string
		cfg  zsp.Config
		want string
	}{
		{
			name: "repository",
			cfg:  zsp.Config{Fetch: zsp.FetchConfig{Repository: "https://github.com/example/app"}},
			want: "https://github.com/example/app",
		},
		{
			name: "release source",
			cfg: zsp.Config{Fetch: zsp.FetchConfig{
				Repository:    "https://github.com/example/not-the-release",
				ReleaseSource: &zsp.ReleaseSource{URL: "https://github.com/example/app"},
			}},
			want: "https://github.com/example/app",
		},
		{
			name: "local APK",
			cfg: zsp.Config{Fetch: zsp.FetchConfig{
				ReleaseSource: &zsp.ReleaseSource{LocalPath: "/tmp/app.apk"},
			}},
			want: "/tmp/app.apk",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := wizardReleaseLocation(test.cfg); got != test.want {
				t.Errorf("wizardReleaseLocation() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWizardFailureSummaryDoesNotExposeInternalErrors(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"Configuration setup stopped", "Couldn't use zapstore.yaml. Fix it and try again"},
		{"Relay reachability check stopped", "Couldn't reach any configured relay. No changes were made"},
		{"Publication stopped", "This release was not published"},
	}
	for _, test := range tests {
		t.Run(test.title, func(t *testing.T) {
			if got := wizardFailureSummary(test.title); got != test.want {
				t.Errorf("wizardFailureSummary() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWizardFailurePresentationReportsCertificateMismatch(t *testing.T) {
	const apkHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const selectedHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	kind, summary, details := wizardFailurePresentation("Identity setup stopped", errCertificateHashMismatch(apkHash, selectedHash))
	if kind != "error" {
		t.Fatalf("kind = %q, want error", kind)
	}
	if summary != "This keystore does not match the APK. No proof was changed" {
		t.Fatalf("summary = %q", summary)
	}
	want := []ui.KeyValue{
		{Key: "APK certificate hash", Value: apkHash},
		{Key: "Selected certificate hash", Value: selectedHash},
	}
	if len(details) != len(want) {
		t.Fatalf("details = %#v, want %#v", details, want)
	}
	for index := range want {
		if details[index] != want[index] {
			t.Fatalf("details[%d] = %+v, want %+v", index, details[index], want[index])
		}
	}

	kind, summary, details = wizardFailurePresentation("Identity setup stopped", errors.New("look up C1 proof: timeout"))
	if kind != "info" || summary != "No proof was changed" || details != nil {
		t.Fatalf("unrelated failure = (%q, %q, %#v)", kind, summary, details)
	}
}

func TestWizardRelayTargetsRedactsCredentialsAndQueries(t *testing.T) {
	got := wizardRelayTargets([]string{
		"wss://relay.example/path?token=secret",
		"wss://user:password@private.example#fragment",
		"://invalid",
	})
	want := []ui.KeyValue{
		{Key: "Relay", Value: "wss://relay.example/path"},
		{Key: "Relay", Value: "wss://private.example"},
		{Key: "Relay", Value: "invalid relay URL"},
	}
	if len(got) != len(want) {
		t.Fatalf("wizardRelayTargets() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("wizardRelayTargets()[%d] = %v, want %v", index, got[index], want[index])
		}
	}
}

func TestWizardFailureReasonIsSanitized(t *testing.T) {
	reason := wizardFailureReason(errors.New("failed to connect to bunker: dial tcp 127.0.0.1:1: connection refused"))
	if reason != "failed to connect to bunker: dial tcp 127.0.0.1:1: connection refused" {
		t.Fatalf("reason = %q", reason)
	}
	secret := "nsec1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpj0f0h"
	reason = wizardFailureReason(errors.New("sign failed for " + secret))
	if strings.Contains(reason, secret) || strings.Contains(reason, "nsec1") {
		t.Fatalf("reason leaked key material: %q", reason)
	}
	if got := wizardFailureReason(nil); got != "" {
		t.Fatalf("reason = %q, want empty", got)
	}
}

func TestWizardCancellationSummaryStatesWhatChanged(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"App discovery stopped", "App discovery was cancelled. No changes were made"},
		{"Identity setup stopped", "No proof was changed"},
		{"Publication stopped", "Publishing was cancelled. Nothing was published"},
	}
	for _, test := range tests {
		t.Run(test.title, func(t *testing.T) {
			if got := wizardCancellationSummary(test.title); got != test.want {
				t.Fatalf("wizardCancellationSummary(%q) = %q, want %q", test.title, got, test.want)
			}
		})
	}
}

func TestExistingOwnershipProofDetailsShowsOwnerAndExpiry(t *testing.T) {
	const owner = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	npub, err := nip19.EncodePublicKey(owner)
	if err != nil {
		t.Fatal(err)
	}
	got := existingOwnershipProofDetails(owner, time.Date(2028, 9, 14, 11, 57, 0, 0, time.Local))
	want := []ui.KeyValue{
		{Key: "Owner", Value: npub},
		{Key: "Expires", Value: "14 Sep 2028"},
	}
	if len(got) != len(want) {
		t.Fatalf("existingOwnershipProofDetails() = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("existingOwnershipProofDetails()[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
	if got := existingOwnershipProofDetails("", time.Time{}); len(got) != 0 {
		t.Fatalf("empty details = %#v", got)
	}
	if got := abbreviateHash("abcd"); got != "abcd" {
		t.Fatalf("abbreviateHash(short) = %q", got)
	}
}

func TestActiveProofForCertificateHash(t *testing.T) {
	secret := gonostr.GeneratePrivateKey()
	owner, err := gonostr.GetPublicKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	const certificateHash = "d3bd3d00681ef7da9d157961477304ff86086ba6002354dbf6a3dc5ea3f7266a"
	event := &gonostr.Event{
		Kind:      30509,
		PubKey:    owner,
		CreatedAt: gonostr.Now(),
		Tags: gonostr.Tags{
			{"d", certificateHash},
			{"signature", "c2ln"},
			{"expiry", fmt.Sprintf("%d", time.Now().Add(31*24*time.Hour).Unix())},
			{"delegation", strings.Repeat("a", 64)},
		},
	}
	if err := event.Sign(secret); err != nil {
		t.Fatal(err)
	}

	got, proof := activeProofForCertificateHash([]*gonostr.Event{event}, certificateHash)
	if got != event || proof == nil {
		t.Fatalf("activeProofForCertificateHash() = (%#v, %#v), want event and proof", got, proof)
	}
	if delegate := proofDelegate(got); delegate != strings.Repeat("a", 64) {
		t.Fatalf("proofDelegate() = %q", delegate)
	}
	state := wizardExistingIdentity([]*gonostr.Event{event}, certificateHash, "Notes")
	if state == nil || state.owner != owner || state.delegate != strings.Repeat("a", 64) || !strings.HasPrefix(state.summary, "Notes is claimed by npub1") {
		t.Fatalf("wizardExistingIdentity() = %#v, want current proof owner and delegate", state)
	}
	event.Tags[2][1] = fmt.Sprintf("%d", time.Now().Add(29*24*time.Hour).Unix())
	if err := event.Sign(secret); err != nil {
		t.Fatal(err)
	}
	if state := wizardExistingIdentity([]*gonostr.Event{event}, certificateHash, "Notes"); state != nil {
		t.Fatalf("wizardExistingIdentity() = %#v, want nil for proof within renewal window", state)
	}
	if got, proof := activeProofForCertificateHash([]*gonostr.Event{event}, strings.Repeat("b", 64)); got != nil || proof != nil {
		t.Fatalf("mismatched certificate hash = (%#v, %#v), want nil", got, proof)
	}
}
