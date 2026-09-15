package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/zapstore/zsp/internal/identity"
	nostrpkg "github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/ui"
)

// forceTTY overrides the isTTY seam for the duration of the test.
func forceTTY(t *testing.T, value bool) {
	t.Helper()
	old := isTTY
	isTTY = func() bool { return value }
	t.Cleanup(func() { isTTY = old })
}

func TestParseDelegate(t *testing.T) {
	pubkeyHex := gonostr.GeneratePrivateKey() // any 64-char lowercase hex value works as a test pubkey
	npub, err := nip19.EncodePublicKey(pubkeyHex)
	if err != nil {
		t.Fatalf("encode npub: %v", err)
	}

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "lowercase hex", input: pubkeyHex, want: pubkeyHex},
		{name: "npub", input: npub, want: pubkeyHex},
		{name: "uppercase hex rejected", input: strings.ToUpper(pubkeyHex), wantErr: true},
		{name: "too short", input: "abcd", wantErr: true},
		{name: "invalid npub", input: "npub1invalid", wantErr: true},
		{name: "non-hex characters", input: strings.Repeat("g", 64), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDelegate(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseDelegate(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDelegate(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("parseDelegate(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateSuggestionSource(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "https URL kept as-is", input: "https://github.com/example/app", want: "https://github.com/example/app"},
		{name: "bare domain gets https prefix", input: "github.com/example/app", want: "https://github.com/example/app"},
		{name: "http rejected", input: "http://github.com/example/app", wantErr: true},
		{name: "empty rejected", input: "", wantErr: true},
		{name: "whitespace trimmed", input: "  github.com/example/app  ", want: "https://github.com/example/app"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateSuggestionSource(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateSuggestionSource(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateSuggestionSource(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("validateSuggestionSource(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRelaysFromEnv_Default(t *testing.T) {
	t.Setenv("RELAYS", "")
	withNoEnvFile(t)

	got := relaysFromEnv()
	if len(got) != 1 || got[0] != nostrpkg.DefaultRelay {
		t.Fatalf("relaysFromEnv() = %v, want [%s]", got, nostrpkg.DefaultRelay)
	}
}

func TestRelaysFromEnv_CommaSeparated(t *testing.T) {
	t.Setenv("RELAYS", "wss://a.example, wss://b.example ,, wss://c.example")
	withNoEnvFile(t)

	got := relaysFromEnv()
	want := []string{"wss://a.example", "wss://b.example", "wss://c.example"}
	if len(got) != len(want) {
		t.Fatalf("relaysFromEnv() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("relaysFromEnv()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAnyRelayAccepted(t *testing.T) {
	tests := []struct {
		name    string
		results []nostrpkg.PublishResult
		want    bool
	}{
		{name: "none", results: nil, want: false},
		{name: "all failed", results: []nostrpkg.PublishResult{{Success: false}, {Success: false}}, want: false},
		{name: "one succeeded", results: []nostrpkg.PublishResult{{Success: false}, {Success: true}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anyRelayAccepted(tt.results); got != tt.want {
				t.Fatalf("anyRelayAccepted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestC1RelayPublishErrorIncludesSafeRelayDiagnostics(t *testing.T) {
	failure := &c1RelayPublishError{
		CertificateHash: "certificate-hash",
		EventID:         "event-id",
		Results: []nostrpkg.PublishResult{
			{
				RelayURL: "wss://relay.example/?token=secret#fragment",
				Error:    errors.New("failed to publish: relay rejected event"),
			},
			{RelayURL: "wss://offline.example", Error: errors.New("connection refused")},
		},
	}

	diagnostics := failure.Diagnostics()
	if len(diagnostics) != 2 {
		t.Fatalf("len(diagnostics) = %d, want 2", len(diagnostics))
	}
	if got := diagnostics[0]["relay_url"]; got != "wss://relay.example/" {
		t.Fatalf("relay_url = %q, want query and fragment removed", got)
	}
	if strings.Contains(fmt.Sprint(diagnostics), "secret") {
		t.Fatalf("diagnostics leaked relay credentials: %v", diagnostics)
	}
	if got := diagnostics[1]["message"]; got != "connection refused" {
		t.Fatalf("message = %q, want connection detail", got)
	}
}

func TestCreateSignedC1ProofProducesValidNostrEvent(t *testing.T) {
	privateKey, certificate := mustGenerateRSAIdentity(t)
	signer, err := nostrpkg.NewSigner(context.Background(), testNsec(t))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	t.Cleanup(func() { _ = signer.Close() })

	published, err := createSignedC1Proof(context.Background(), privateKey, certificate, signer, identity.DefaultExpiry, "")
	if err != nil {
		t.Fatalf("createSignedC1Proof: %v", err)
	}
	if published.Event.ID == "" {
		t.Fatal("C1 event ID is empty")
	}
	valid, err := published.Event.CheckSignature()
	if err != nil {
		t.Fatalf("CheckSignature: %v", err)
	}
	if !valid {
		t.Fatal("C1 event signature is invalid")
	}
}

func TestRenderC1RelayFailure(t *testing.T) {
	failure := &c1RelayPublishError{
		CertificateHash: "certificate-hash",
		EventID:         "event-id",
		Results: []nostrpkg.PublishResult{
			{RelayURL: "wss://relay.example", Error: errors.New("connection refused")},
		},
	}

	oldNoColor := ui.NoColor
	ui.SetNoColor(true)
	t.Cleanup(func() { ui.SetNoColor(oldNoColor) })

	output := renderC1RelayFailure(failure)
	for _, border := range []string{"╭", "╮", "╰", "╯", "─"} {
		if strings.Contains(output, border) {
			t.Fatalf("rendered failure must not contain a border %q:\n%s", border, output)
		}
	}
	for _, expected := range []string{
		"[ERROR] C1 proof was not accepted by any relay",
		"C1 proof was not accepted by any relay",
		"wss://relay.example",
		"connection refused",
		"certificate-hash",
		"event-id",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("rendered failure missing %q:\n%s", expected, output)
		}
	}
}

func TestResolveKeystorePath_NonInteractiveRequiresFlag(t *testing.T) {
	forceTTY(t, false)

	_, err := resolveKeystorePath("", true)
	if err == nil || !strings.Contains(err.Error(), "--keystore") {
		t.Fatalf("resolveKeystorePath() error = %v, want --keystore requirement", err)
	}
}

func TestResolveKeystorePath_UsesProvidedValue(t *testing.T) {
	got, err := resolveKeystorePath("  release.jks  ", false)
	if err != nil {
		t.Fatalf("resolveKeystorePath() error: %v", err)
	}
	if got != "release.jks" {
		t.Fatalf("resolveKeystorePath() = %q, want release.jks", got)
	}
}

func TestIdentitySource_NonInteractiveWithoutFlag(t *testing.T) {
	forceTTY(t, false)

	got, err := identitySource("")
	if err != nil {
		t.Fatalf("identitySource() unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("identitySource() = %q, want empty", got)
	}
}

func TestIdentitySource_FlagValidatedRegardlessOfTTY(t *testing.T) {
	forceTTY(t, false)

	got, err := identitySource("github.com/example/app")
	if err != nil {
		t.Fatalf("identitySource() unexpected error: %v", err)
	}
	if got != "https://github.com/example/app" {
		t.Fatalf("identitySource() = %q, want https://github.com/example/app", got)
	}

	if _, err := identitySource("ftp://bad"); err == nil {
		t.Fatal("identitySource() expected error for invalid scheme")
	}
}

func TestLoadIdentityMaterial_PEM(t *testing.T) {
	key, cert := mustGenerateRSAIdentity(t)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	writeTestPEMCert(t, certPath, cert)
	writeTestPEMKey(t, keyPath, key)

	gotKey, gotCert, err := loadIdentityMaterial(proofOptions{Keystore: certPath, PrivateKey: keyPath})
	if err != nil {
		t.Fatalf("loadIdentityMaterial() error: %v", err)
	}
	if identity.ComputeCertHash(gotCert) != identity.ComputeCertHash(cert) {
		t.Fatal("loaded certificate does not match")
	}
	if err := identity.ValidateKeyCertPair(gotKey, gotCert); err != nil {
		t.Fatalf("loaded pair invalid: %v", err)
	}
}

func TestLoadIdentityMaterial_PEMMissingKeyNonInteractive(t *testing.T) {
	forceTTY(t, false)

	_, cert := mustGenerateRSAIdentity(t)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	writeTestPEMCert(t, certPath, cert)

	_, _, err := loadIdentityMaterial(proofOptions{Keystore: certPath})
	if err == nil {
		t.Fatal("loadIdentityMaterial() expected error for missing --private-key")
	}
	if !strings.Contains(err.Error(), "--private-key") {
		t.Fatalf("error = %v, want mention of --private-key", err)
	}
}

func TestLoadIdentityMaterial_JKS(t *testing.T) {
	path := generateTestKeystore(t, "JKS", "release")

	t.Setenv("KEYSTORE_PASSWORD", "storepass")
	withNoEnvFile(t)

	gotKey, gotCert, err := loadIdentityMaterial(proofOptions{Keystore: path})
	if err != nil {
		t.Fatalf("loadIdentityMaterial() error: %v", err)
	}
	if gotCert.Subject.CommonName != "release" {
		t.Fatalf("certificate common name = %q, want release", gotCert.Subject.CommonName)
	}
	if err := identity.ValidateKeyCertPair(gotKey, gotCert); err != nil {
		t.Fatalf("loaded pair invalid: %v", err)
	}
}

func TestLoadIdentityMaterial_JKSMissingPasswordNonInteractive(t *testing.T) {
	forceTTY(t, false)

	path := generateTestKeystore(t, "JKS", "release")

	t.Setenv("KEYSTORE_PASSWORD", "")
	withNoEnvFile(t)

	_, _, err := loadIdentityMaterial(proofOptions{Keystore: path})
	if err == nil {
		t.Fatal("loadIdentityMaterial() expected error for missing KEYSTORE_PASSWORD")
	}
	if !strings.Contains(err.Error(), "KEYSTORE_PASSWORD") {
		t.Fatalf("error = %v, want mention of KEYSTORE_PASSWORD", err)
	}
}

func TestLoadIdentityMaterial_JSONNeverPromptsForPassword(t *testing.T) {
	forceTTY(t, true)
	path := generateTestKeystore(t, "JKS", "release")

	t.Setenv("KEYSTORE_PASSWORD", "")
	withNoEnvFile(t)

	_, _, err := loadIdentityMaterial(proofOptions{Keystore: path, NonInteractive: true})
	if err == nil || !strings.Contains(err.Error(), "KEYSTORE_PASSWORD") {
		t.Fatalf("error = %v, want non-interactive KEYSTORE_PASSWORD error", err)
	}
}

func TestLoadIdentityMaterial_JKSMultipleAliasesRequireSelection(t *testing.T) {
	forceTTY(t, false)

	path := generateTestKeystore(t, "JKS", "rsa", "ec")

	t.Setenv("KEYSTORE_PASSWORD", "storepass")
	withNoEnvFile(t)

	_, _, err := loadIdentityMaterial(proofOptions{Keystore: path})
	if err == nil {
		t.Fatal("loadIdentityMaterial() expected error for ambiguous alias")
	}
	if !strings.Contains(err.Error(), "--key-alias") {
		t.Fatalf("error = %v, want mention of --key-alias", err)
	}
}

func TestLoadIdentityMaterial_JKSExplicitAlias(t *testing.T) {
	path := generateTestKeystore(t, "JKS", "rsa", "ec")

	t.Setenv("KEYSTORE_PASSWORD", "storepass")
	withNoEnvFile(t)

	_, gotCert, err := loadIdentityMaterial(proofOptions{Keystore: path, KeyAlias: "ec"})
	if err != nil {
		t.Fatalf("loadIdentityMaterial() error: %v", err)
	}
	if gotCert.Subject.CommonName != "ec" {
		t.Fatalf("certificate common name = %q, want ec", gotCert.Subject.CommonName)
	}
}

func TestLoadIdentityMaterial_PKCS12(t *testing.T) {
	path := generateTestKeystore(t, "PKCS12", "release")

	t.Setenv("KEYSTORE_PASSWORD", "storepass")
	withNoEnvFile(t)

	gotKey, gotCert, err := loadIdentityMaterial(proofOptions{Keystore: path})
	if err != nil {
		t.Fatalf("loadIdentityMaterial() error: %v", err)
	}
	if gotCert.Subject.CommonName != "release" {
		t.Fatalf("certificate common name = %q, want release", gotCert.Subject.CommonName)
	}
	if err := identity.ValidateKeyCertPair(gotKey, gotCert); err != nil {
		t.Fatalf("loaded pair invalid: %v", err)
	}
}

func TestLoadIdentityMaterial_PKCS12MissingPasswordNonInteractive(t *testing.T) {
	forceTTY(t, false)

	path := generateTestKeystore(t, "PKCS12", "release")

	t.Setenv("KEYSTORE_PASSWORD", "")
	withNoEnvFile(t)

	_, _, err := loadIdentityMaterial(proofOptions{Keystore: path})
	if err == nil {
		t.Fatal("loadIdentityMaterial() expected error for missing KEYSTORE_PASSWORD")
	}
	if !strings.Contains(err.Error(), "KEYSTORE_PASSWORD") {
		t.Fatalf("error = %v, want mention of KEYSTORE_PASSWORD", err)
	}
}

func TestLoadIdentityMaterial_UnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "release.bin")
	if err := os.WriteFile(path, []byte("not a keystore"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, err := loadIdentityMaterial(proofOptions{Keystore: path})
	if err == nil {
		t.Fatal("loadIdentityMaterial() expected error for unsupported keystore type")
	}
}

// --- test helpers -----------------------------------------------------

// withNoEnvFile changes the working directory to an empty temp directory so
// config.GetEnv's ".env" fallback can't pick up an ambient .env file (e.g.
// local developer secrets) and leak state into a test that only wants to
// control the process environment.
func withNoEnvFile(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

func testNsec(t *testing.T) string {
	t.Helper()
	privkey := gonostr.GeneratePrivateKey()
	nsec, err := nip19.EncodePrivateKey(privkey)
	if err != nil {
		t.Fatalf("encode nsec: %v", err)
	}
	return nsec
}

func mustGenerateRSAIdentity(t *testing.T) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return key, mustSelfSignedIdentity(t, key, &key.PublicKey)
}

func mustSelfSignedIdentity(t *testing.T, key crypto.PrivateKey, pub crypto.PublicKey) *x509.Certificate {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "zsp-identity-create-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func writeTestPEMKey(t *testing.T, path string, key crypto.PrivateKey) {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}
}

func writeTestPEMCert(t *testing.T, path string, cert *x509.Certificate) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}
}

func generateTestKeystore(t *testing.T, storeType string, aliases ...string) string {
	t.Helper()
	extension := ".jks"
	if storeType == "PKCS12" {
		extension = ".p12"
	}
	path := filepath.Join(t.TempDir(), "release"+extension)
	for _, alias := range aliases {
		command := exec.Command(
			"keytool", "-genkeypair",
			"-alias", alias,
			"-keystore", path,
			"-storetype", storeType,
			"-storepass", "storepass",
			"-keypass", "storepass",
			"-dname", "CN="+alias,
			"-keyalg", "RSA",
			"-keysize", "2048",
			"-validity", "1",
			"-noprompt",
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("keytool %s: %v\n%s", alias, err, output)
		}
	}
	return path
}
