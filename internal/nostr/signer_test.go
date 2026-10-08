package nostr

import (
	"bytes"
	"context"
	"strings"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/zapstore/zsp/internal/sanitize"
)

func TestLoopbackBunkerUsesSharedClientKey(t *testing.T) {
	local := "bunker://ab?relay=ws%3A%2F%2F127.0.0.1%3A1%2Ftoken&secret=s"
	if !loopbackBunker(local) {
		t.Fatal("127.0.0.1 bunker was not local")
	}
	remote := "bunker://ab?relay=wss%3A%2F%2Frelay.example&secret=s"
	if loopbackBunker(remote) {
		t.Fatal("remote bunker was local")
	}
	key, err := bunkerClientKey(local, "ab")
	if err != nil {
		t.Fatal(err)
	}
	if key != localBunkerClientKey {
		t.Fatalf("client key = %s", key)
	}
}

func TestNewSignerRejectsBrowserSigning(t *testing.T) {
	_, err := NewSigner(t.Context(), "browser")
	if err == nil {
		t.Fatal("NewSigner(browser) error = nil")
	}
	if !strings.Contains(err.Error(), "invalid SIGN_WITH format") {
		t.Fatalf("NewSigner(browser) error = %q", err)
	}
}

func TestNewSignerRejectsNpub(t *testing.T) {
	pubkey, err := gonostr.GetPublicKey(gonostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	npub, err := nip19.EncodePublicKey(pubkey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewSigner(t.Context(), npub)
	if err == nil || !strings.Contains(err.Error(), "npub cannot sign") {
		t.Fatalf("NewSigner(npub) error = %v", err)
	}
}

func TestNsecSignerRegistersKeyForRedaction(t *testing.T) {
	nsec, err := nip19.EncodePrivateKey(gonostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewNsecSigner(nsec)
	if err != nil {
		t.Fatal(err)
	}
	// go-nostr quotes the secret key in signing errors; registered key
	// material must be redacted wherever text is shown.
	leak := "Sign called with invalid secret key '" + signer.privateKey + "': encoding/hex: invalid byte"
	redacted := sanitize.Text(leak)
	if strings.Contains(redacted, signer.privateKey) || strings.Contains(redacted, nsec) {
		t.Fatalf("sanitized error leaked key material: %q", redacted)
	}
}

func TestBunkerURLErrorsHideSecret(t *testing.T) {
	const secret = "verysecretbunkervalue123"
	_, err := NewBunkerSigner(context.Background(), "bunker://not-a-valid-pubkey?relay=wss://relay.example&secret="+secret)
	if err == nil {
		t.Fatal("NewBunkerSigner() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("bunker error leaked the NIP-46 secret: %v", err)
	}
}

func TestBunkerURLSecretExtraction(t *testing.T) {
	if got := bunkerURLSecret("bunker://pubkey?relay=wss://relay.example&secret=hidden"); got != "hidden" {
		t.Fatalf("bunkerURLSecret() = %q", got)
	}
	if got := bunkerURLSecret("://not a url"); got != "" {
		t.Fatalf("bunkerURLSecret(invalid) = %q", got)
	}
}

func TestSignEventSetPreservesFinalizedEventBodies(t *testing.T) {
	secret := gonostr.GeneratePrivateKey()
	signer, err := NewSigner(t.Context(), secret)
	if err != nil {
		t.Fatal(err)
	}
	defer signer.Close()

	events := &EventSet{
		AppMetadata: &gonostr.Event{Kind: KindAppMetadata, PubKey: signer.PublicKey(), CreatedAt: 10, Tags: gonostr.Tags{{"d", "com.example.app"}}},
		Release:     &gonostr.Event{Kind: KindRelease, PubKey: signer.PublicKey(), CreatedAt: 10, Tags: gonostr.Tags{{"i", "com.example.app"}, {"c", "main"}}},
		SoftwareAssets: []*gonostr.Event{
			{Kind: KindSoftwareAsset, PubKey: signer.PublicKey(), CreatedAt: 10, Tags: gonostr.Tags{{"i", "com.example.app"}, {"version_code", "1"}}},
		},
	}
	FinalizeEventSet(events, "wss://relay.example")
	before := [][]byte{
		append([]byte(nil), events.AppMetadata.Serialize()...),
		append([]byte(nil), events.Release.Serialize()...),
		append([]byte(nil), events.SoftwareAssets[0].Serialize()...),
	}

	if err := SignEventSet(t.Context(), signer, events); err != nil {
		t.Fatal(err)
	}
	after := [][]byte{
		events.AppMetadata.Serialize(),
		events.Release.Serialize(),
		events.SoftwareAssets[0].Serialize(),
	}
	for i := range before {
		if !bytes.Equal(before[i], after[i]) {
			t.Fatalf("event %d body changed while signing", i)
		}
	}
}
