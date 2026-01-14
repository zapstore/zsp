package nostr

import (
	"context"
	"encoding/hex"
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

// SignerOptions contains options for creating a signer.
type SignerOptions struct {
	Port int // Custom port for browser signer (0 = default)
}

// NewSigner creates a signer from a SIGN_WITH value.
func NewSigner(ctx context.Context, signWith string) (Signer, error) {
	return NewSignerWithOptions(ctx, signWith, SignerOptions{})
}

// NewSignerWithOptions creates a signer from a SIGN_WITH value with options.
func NewSignerWithOptions(ctx context.Context, signWith string, opts SignerOptions) (Signer, error) {
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
		return NewNIP07Signer(ctx, opts.Port)
	}

	// Check if it's a hex private key (64 hex characters = 32 bytes)
	if len(signWith) == 64 && isValidHex(signWith) {
		nsec, err := nip19.EncodePrivateKey(signWith)
		if err != nil {
			return nil, fmt.Errorf("invalid hex private key: %w", err)
		}
		return NewNsecSigner(nsec)
	}

	return nil, fmt.Errorf("invalid SIGN_WITH format: must be nsec1..., npub1..., hex private key, bunker://..., or browser")
}

// isValidHex checks if a string is valid hexadecimal.
func isValidHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
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
	// Generate an ephemeral client secret key for bunker communication
	clientSecretKey := nostr.GeneratePrivateKey()

	// Connect to bunker
	bunker, err := nip46.ConnectBunker(ctx, clientSecretKey, bunkerURL, nil, func(s string) {
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
// It signs the Software Assets first to get their IDs, adds the references to Software Release,
// then signs Software Release and Software Application.
func SignEventSet(ctx context.Context, signer Signer, events *EventSet, relayHint string) error {
	// Use batch signing if available (e.g., NIP-07 browser signer)
	// For batch signing, we need to pre-compute the asset IDs before signing
	if batchSigner, ok := signer.(BatchSigner); ok {
		return signEventSetBatch(ctx, batchSigner, events, relayHint)
	}

	// Sequential signing: sign assets first, add references to release, then sign rest
	// 1. Sign all Software Assets first to get their event IDs
	for i, asset := range events.SoftwareAssets {
		if err := signer.Sign(ctx, asset); err != nil {
			return fmt.Errorf("failed to sign Software Asset event %d: %w", i+1, err)
		}
		// 2. Add the asset event ID reference to the Software Release event
		events.AddAssetReference(asset.ID, relayHint)
	}

	// 3. Sign the Software Release event (now with asset references)
	if err := signer.Sign(ctx, events.Release); err != nil {
		return fmt.Errorf("failed to sign Software Release event: %w", err)
	}

	// 4. Sign the Software Application event
	if err := signer.Sign(ctx, events.AppMetadata); err != nil {
		return fmt.Errorf("failed to sign Software Application event: %w", err)
	}

	return nil
}

// signEventSetBatch handles batch signing for signers like NIP-07.
// For batch signing, we need a different approach since all events are signed at once.
func signEventSetBatch(ctx context.Context, batchSigner BatchSigner, events *EventSet, relayHint string) error {
	// For batch signing, we can't sign Software Assets first and then update Software Release.
	// Instead, we pre-compute what the Software Asset event IDs will be.
	// The ID is SHA256 of the serialized event, so we can compute it before signing.

	// Compute what each Software Asset event ID will be (based on unsigned content)
	for _, asset := range events.SoftwareAssets {
		asset.PubKey = events.Release.PubKey // Ensure pubkey is set
		assetID := asset.GetID()
		// Add the asset reference to Software Release before batch signing
		events.AddAssetReference(assetID, relayHint)
	}

	// Now batch sign all events
	allEvents := []*nostr.Event{events.AppMetadata, events.Release}
	allEvents = append(allEvents, events.SoftwareAssets...)
	if err := batchSigner.SignBatch(ctx, allEvents); err != nil {
		return fmt.Errorf("failed to batch sign events: %w", err)
	}

	return nil
}

// EventsToJSON converts events to JSON Lines format.
func EventsToJSON(events *EventSet) ([]byte, error) {
	var result []byte

	// Add app metadata and release
	for _, event := range []*nostr.Event{events.AppMetadata, events.Release} {
		data, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		result = append(result, data...)
		result = append(result, '\n')
	}

	// Add all software assets
	for _, asset := range events.SoftwareAssets {
		data, err := json.Marshal(asset)
		if err != nil {
			return nil, err
		}
		result = append(result, data...)
		result = append(result, '\n')
	}

	return result, nil
}
