package nostr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

	// Check if it's a hex private key (pad to 64 hex characters = 32 bytes if shorter)
	if isValidHex(signWith) && len(signWith) <= 64 {
		// Pad with leading zeros to 64 characters (32 bytes)
		hexKey := fmt.Sprintf("%064s", signWith)
		hexKey = strings.ReplaceAll(hexKey, " ", "0")
		nsec, err := nip19.EncodePrivateKey(hexKey)
		if err != nil {
			return nil, fmt.Errorf("invalid hex private key: %w", err)
		}
		return NewNsecSigner(nsec)
	}

	return nil, fmt.Errorf("invalid SIGN_WITH format: must be nsec1..., npub1..., hex private key, bunker://..., or browser")
}

// isValidHex checks if a string is valid hexadecimal.
func isValidHex(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
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

// Close clears sensitive key material from memory.
// Note: Go strings are immutable, so we cannot truly zero them.
// Setting to empty string allows the original to be garbage collected sooner
// and reduces the window of exposure.
func (s *NsecSigner) Close() error {
	s.privateKey = ""
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
	// Extract the target pubkey from the bunker URL.
	// The URL format is: bunker://<remote-signer-pubkey>?relay=...&secret=...
	// We key the client secret by the target pubkey, NOT the secret token,
	// because the secret is single-use and disposable while the pubkey identifies
	// the actual bunker we're connecting to.
	targetPubkey, err := extractBunkerTargetPubkey(bunkerURL)
	if err != nil {
		return nil, fmt.Errorf("invalid bunker URL: %w", err)
	}

	// Get or generate a truly random client secret key for this bunker.
	// This is persisted to ensure we use the same client key across sessions,
	// which is necessary because NIP-46 permissions are tied to the client pubkey.
	clientSecretKey, err := getOrCreateBunkerClientKey(targetPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to get client key: %w", err)
	}

	// Connect to bunker
	bunker, err := nip46.ConnectBunker(ctx, clientSecretKey, bunkerURL, nil, func(s string) {
		// This is called when user needs to approve the connection
		fmt.Printf("Bunker connection request: %s\n", s)
	})
	if err != nil {
		if !strings.Contains(err.Error(), "already connected") {
			return nil, fmt.Errorf("failed to connect to bunker: %w", err)
		}
		// "already connected" means the secret was already used.
		// This is okay if we're using the same client key that originally connected.
	}

	// Get public key
	pubkey, err := bunker.GetPublicKey(ctx)
	if err != nil {
		// If we get "no permission", it likely means the bunker URL's secret
		// was already used with a different client key (e.g., from another app).
		// The user needs to generate a new bunker URL.
		if strings.Contains(err.Error(), "no permission") {
			return nil, fmt.Errorf("failed to get public key from bunker: %w\n\nThis bunker URL's secret appears to have been used with a different application.\nPlease generate a new bunker connection URL from your signer (e.g., nsec.app)", err)
		}
		return nil, fmt.Errorf("failed to get public key from bunker: %w", err)
	}

	return &BunkerSigner{
		bunker:    bunker,
		publicKey: pubkey,
	}, nil
}

// extractBunkerTargetPubkey extracts the target pubkey from a bunker URL.
// The URL format is: bunker://<remote-signer-pubkey>?relay=...&secret=...
func extractBunkerTargetPubkey(bunkerURL string) (string, error) {
	parsed, err := url.Parse(bunkerURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL: %w", err)
	}
	if parsed.Scheme != "bunker" {
		return "", fmt.Errorf("expected bunker:// scheme, got %s://", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("missing target pubkey in bunker URL")
	}
	if !nostr.IsValidPublicKey(parsed.Host) {
		return "", fmt.Errorf("invalid target pubkey: %s", parsed.Host)
	}
	return parsed.Host, nil
}

// getOrCreateBunkerClientKey retrieves an existing client key for a bunker,
// or generates and persists a new truly random one.
// Keys are stored in the user's config directory under zsp/bunker-keys/.
func getOrCreateBunkerClientKey(targetPubkey string) (string, error) {
	keyPath, err := bunkerKeyPath(targetPubkey)
	if err != nil {
		return "", err
	}

	// Try to read existing key
	data, err := os.ReadFile(keyPath)
	if err == nil {
		key := strings.TrimSpace(string(data))
		if len(key) == 64 && isValidHex(key) {
			return key, nil
		}
		// Invalid key file, regenerate
	}

	// Generate a truly random 32-byte private key
	var keyBytes [32]byte
	if _, err := rand.Read(keyBytes[:]); err != nil {
		return "", fmt.Errorf("failed to generate random key: %w", err)
	}
	clientKey := hex.EncodeToString(keyBytes[:])

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return "", fmt.Errorf("failed to create key directory: %w", err)
	}

	// Write key with restrictive permissions (owner read/write only)
	if err := os.WriteFile(keyPath, []byte(clientKey+"\n"), 0600); err != nil {
		return "", fmt.Errorf("failed to save client key: %w", err)
	}

	return clientKey, nil
}

// bunkerKeyPath returns the file path for storing a bunker client key.
// Keys are stored in $XDG_CONFIG_HOME/zsp/bunker-keys/<pubkey>.key
// or ~/.config/zsp/bunker-keys/<pubkey>.key on Unix systems.
func bunkerKeyPath(targetPubkey string) (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config directory: %w", err)
	}
	return filepath.Join(configDir, "zsp", "bunker-keys", targetPubkey+".key"), nil
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
