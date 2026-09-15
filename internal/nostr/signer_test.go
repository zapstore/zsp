package nostr

import (
	"bytes"
	"strings"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

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
