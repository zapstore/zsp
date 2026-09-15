package nostr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip46"
)

// SignerType represents the type of signer.
type SignerType int

const (
	SignerNsec SignerType = iota
	SignerBunker
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
		return nil, fmt.Errorf("npub cannot sign publication")
	}

	if strings.HasPrefix(signWith, "bunker://") {
		return NewBunkerSigner(ctx, signWith)
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

	return nil, fmt.Errorf("invalid SIGN_WITH format: must be nsec1..., npub1..., hex private key, or bunker://...")
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
	if s == nil {
		return nil
	}
	s.privateKey = ""
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
	bunker, err := nip46.ConnectBunker(ctx, clientSecretKey, bunkerURL, nil, func(string) {})
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

	// Try to read an existing owner-only key.
	data, err := os.ReadFile(keyPath)
	if err == nil {
		key := strings.TrimSpace(string(data))
		if len(key) == 64 && isValidHex(key) {
			if err := os.Chmod(keyPath, 0o600); err != nil {
				return "", fmt.Errorf("secure bunker client key: %w", err)
			}
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

	if err := WriteSecretFile(keyPath, []byte(clientKey+"\n")); err != nil {
		return "", fmt.Errorf("failed to save client key: %w", err)
	}

	return clientKey, nil
}

// WriteSecretFile atomically replaces a secret file with owner-only mode.
func WriteSecretFile(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
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

// SignEventSet signs a finalized event set without changing any event body.
func SignEventSet(ctx context.Context, signer Signer, events *EventSet) error {
	for i, asset := range events.SoftwareAssets {
		if err := signFinalizedEvent(ctx, signer, asset); err != nil {
			return fmt.Errorf("failed to sign Software Asset event %d: %w", i+1, err)
		}
	}

	if err := signFinalizedEvent(ctx, signer, events.Release); err != nil {
		return fmt.Errorf("failed to sign Software Release event: %w", err)
	}

	if events.AppMetadata != nil {
		if err := signFinalizedEvent(ctx, signer, events.AppMetadata); err != nil {
			return fmt.Errorf("failed to sign Software Application event: %w", err)
		}
	}

	if events.IdentityProof != nil {
		if err := signFinalizedEvent(ctx, signer, events.IdentityProof); err != nil {
			return fmt.Errorf("failed to sign IdentityProof event: %w", err)
		}
	}

	return nil
}

func signFinalizedEvent(ctx context.Context, signer Signer, event *nostr.Event) error {
	if event == nil || event.ID == "" || event.ID != event.GetID() {
		return fmt.Errorf("event must be finalized before signing")
	}
	if err := signer.Sign(ctx, event); err != nil {
		return err
	}
	if event.ID != event.GetID() {
		return fmt.Errorf("signer changed event body")
	}
	valid, err := event.CheckSignature()
	if err != nil || !valid {
		return fmt.Errorf("signer produced an invalid event signature")
	}
	return nil
}
