package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip46"
)

// TestNsec is a deterministic test key (private key = 1) for dry-run mode.
// DO NOT use this for production signing.
// Public key: 79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798
const TestNsec = "nsec1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqsmhltgl"

// SignerType represents the type of signer.
type SignerType int

const (
	SignerNsec SignerType = iota
	SignerNpub
	SignerBunker
	SignerNIP07
)

// Signer handles event signing.
type Signer interface {
	// Type returns the signer type.
	Type() SignerType

	// PublicKey returns the public key (hex).
	PublicKey() string

	// Sign signs an event in place.
	Sign(ctx context.Context, event *nostr.Event) error

	// Close releases any resources.
	Close() error
}

// NewSigner creates a signer from a SIGN_WITH value.
func NewSigner(ctx context.Context, signWith string) (Signer, error) {
	signWith = strings.TrimSpace(signWith)

	if strings.HasPrefix(signWith, "nsec1") {
		return NewNsecSigner(signWith)
	}

	if strings.HasPrefix(signWith, "npub1") {
		return NewNpubSigner(signWith)
	}

	if strings.HasPrefix(signWith, "bunker://") {
		return NewBunkerSigner(ctx, signWith)
	}

	if signWith == "browser" {
		return NewNIP07Signer(ctx)
	}

	return nil, fmt.Errorf("invalid SIGN_WITH format: must be nsec1..., npub1..., bunker://..., or browser")
}

// NsecSigner signs events with a private key.
type NsecSigner struct {
	privateKey string // hex
	publicKey  string // hex
}

// NewNsecSigner creates a signer from an nsec.
func NewNsecSigner(nsec string) (*NsecSigner, error) {
	prefix, data, err := nip19.Decode(nsec)
	if err != nil {
		return nil, fmt.Errorf("invalid nsec: %w", err)
	}
	if prefix != "nsec" {
		return nil, fmt.Errorf("expected nsec, got %s", prefix)
	}

	privateKey := data.(string)
	publicKey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive public key: %w", err)
	}

	return &NsecSigner{
		privateKey: privateKey,
		publicKey:  publicKey,
	}, nil
}

func (s *NsecSigner) Type() SignerType {
	return SignerNsec
}

func (s *NsecSigner) PublicKey() string {
	return s.publicKey
}

func (s *NsecSigner) Sign(ctx context.Context, event *nostr.Event) error {
	event.PubKey = s.publicKey
	return event.Sign(s.privateKey)
}

func (s *NsecSigner) Close() error {
	return nil
}

// NpubSigner is a "signer" that outputs unsigned events.
// Used for external signing workflows.
type NpubSigner struct {
	publicKey string // hex
}

// NewNpubSigner creates a signer from an npub.
func NewNpubSigner(npub string) (*NpubSigner, error) {
	prefix, data, err := nip19.Decode(npub)
	if err != nil {
		return nil, fmt.Errorf("invalid npub: %w", err)
	}
	if prefix != "npub" {
		return nil, fmt.Errorf("expected npub, got %s", prefix)
	}

	return &NpubSigner{
		publicKey: data.(string),
	}, nil
}

func (s *NpubSigner) Type() SignerType {
	return SignerNpub
}

func (s *NpubSigner) PublicKey() string {
	return s.publicKey
}

func (s *NpubSigner) Sign(ctx context.Context, event *nostr.Event) error {
	// Don't actually sign - just set the pubkey and ID
	event.PubKey = s.publicKey
	event.ID = event.GetID()
	// Signature remains empty - event will be output for external signing
	return nil
}

func (s *NpubSigner) Close() error {
	return nil
}

// BunkerSigner signs events via NIP-46 remote signer.
type BunkerSigner struct {
	bunker    *nip46.BunkerClient
	publicKey string
}

// NewBunkerSigner creates a signer from a bunker:// URL.
func NewBunkerSigner(ctx context.Context, bunkerURL string) (*BunkerSigner, error) {
	// Parse bunker URL
	bunker, err := nip46.ConnectBunker(ctx, "", bunkerURL, nil, func(s string) {
		// This is called when user needs to approve the connection
		fmt.Printf("Bunker connection request: %s\n", s)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to bunker: %w", err)
	}

	// Get public key
	pubkey, err := bunker.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get public key from bunker: %w", err)
	}

	return &BunkerSigner{
		bunker:    bunker,
		publicKey: pubkey,
	}, nil
}

func (s *BunkerSigner) Type() SignerType {
	return SignerBunker
}

func (s *BunkerSigner) PublicKey() string {
	return s.publicKey
}

func (s *BunkerSigner) Sign(ctx context.Context, event *nostr.Event) error {
	event.PubKey = s.publicKey
	return s.bunker.SignEvent(ctx, event)
}

func (s *BunkerSigner) Close() error {
	// BunkerClient doesn't have a Close method, connections are managed internally
	return nil
}

// BatchSigner is an optional interface for signers that support batch signing.
type BatchSigner interface {
	SignBatch(ctx context.Context, events []*nostr.Event) error
}

// SignEventSet signs all events in an event set.
// It signs the SoftwareAsset first to get its ID, adds the reference to Release,
// then signs Release and AppMetadata.
func SignEventSet(ctx context.Context, signer Signer, events *EventSet, relayHint string) error {
	// Use batch signing if available (e.g., NIP-07 browser signer)
	// For batch signing, we need to pre-compute the asset ID before signing
	if batchSigner, ok := signer.(BatchSigner); ok {
		return signEventSetBatch(ctx, batchSigner, events, relayHint)
	}

	// Sequential signing: sign asset first, add reference to release, then sign rest
	// 1. Sign the software asset first to get its event ID
	if err := signer.Sign(ctx, events.SoftwareAsset); err != nil {
		return fmt.Errorf("failed to sign software asset event: %w", err)
	}

	// 2. Add the asset event ID reference to the release event
	events.AddAssetReference(events.SoftwareAsset.ID, relayHint)

	// 3. Sign the release event (now with asset reference)
	if err := signer.Sign(ctx, events.Release); err != nil {
		return fmt.Errorf("failed to sign release event: %w", err)
	}

	// 4. Sign the app metadata event
	if err := signer.Sign(ctx, events.AppMetadata); err != nil {
		return fmt.Errorf("failed to sign app metadata event: %w", err)
	}

	return nil
}

// signEventSetBatch handles batch signing for signers like NIP-07.
// For batch signing, we need a different approach since all events are signed at once.
func signEventSetBatch(ctx context.Context, batchSigner BatchSigner, events *EventSet, relayHint string) error {
	// For batch signing, we can't sign asset first and then update release.
	// Instead, we pre-compute what the asset event ID will be.
	// The ID is SHA256 of the serialized event, so we can compute it before signing.

	// Compute what the asset event ID will be (based on unsigned content)
	events.SoftwareAsset.PubKey = events.Release.PubKey // Ensure pubkey is set
	assetID := events.SoftwareAsset.GetID()

	// Add the asset reference to release before batch signing
	events.AddAssetReference(assetID, relayHint)

	// Now batch sign all events
	allEvents := []*nostr.Event{events.AppMetadata, events.Release, events.SoftwareAsset}
	if err := batchSigner.SignBatch(ctx, allEvents); err != nil {
		return fmt.Errorf("failed to batch sign events: %w", err)
	}

	return nil
}

// EventsToJSON converts events to JSON Lines format.
func EventsToJSON(events *EventSet) ([]byte, error) {
	var result []byte

	for _, event := range []*nostr.Event{events.AppMetadata, events.Release, events.SoftwareAsset} {
		data, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		result = append(result, data...)
		result = append(result, '\n')
	}

	return result, nil
}
