package nostr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip46"
	"github.com/zapstore/zsp/internal/sanitize"
)

// SignerType represents the type of signer.
type SignerType int

const (
	SignerNsec SignerType = iota
	SignerBunker
)

// localBunkerClientKey matches hotbox/internal/bunker.localClientKey.
// Loopback bunkers expect this key. Remote bunkers keep a per-install key.
const localBunkerClientKey = "0000000000000000000000000000000000000000000000000000000000000002"

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

// NewSigner creates a signer from a SIGN_WITH value: an nsec1... private key
// or a bunker://... URL.
func NewSigner(ctx context.Context, signWith string) (Signer, error) {
	signWith = strings.TrimSpace(signWith)

	switch {
	case strings.HasPrefix(signWith, "nsec1"):
		return NewNsecSigner(signWith)
	case strings.HasPrefix(signWith, "npub1"):
		return nil, errors.New("npub cannot sign publication")
	case strings.HasPrefix(signWith, "bunker://"):
		return NewBunkerSigner(ctx, signWith)
	}
	return nil, errors.New("invalid SIGN_WITH format: must be nsec1... or bunker://...")
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
	sanitize.Register(nsec)
	prefix, data, err := nip19.Decode(nsec)
	if err != nil {
		return nil, newOpError("invalid nsec", err)
	}
	if prefix != "nsec" {
		return nil, fmt.Errorf("expected nsec, got %s", prefix)
	}

	privateKey := data.(string)
	sanitize.Register(privateKey)
	publicKey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		return nil, newOpError("derive public key", err)
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
	if err := event.Sign(s.privateKey); err != nil {
		// go-nostr quotes the secret key in this error ("Sign called with
		// invalid secret key '%s'"); opError sanitizes it when printed.
		return newOpError("sign event", err)
	}
	return nil
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

// bunkerTimeout bounds the NIP-46 connect and get_public_key handshake.
const bunkerTimeout = 30 * time.Second

// ErrBunkerSecretUsed means the bunker refused the client because its URL
// secret was already used by another client key.
var ErrBunkerSecretUsed = errors.New("this bunker URL's secret appears to have been used with a different application; generate a new bunker connection URL from your signer (e.g. nsec.app)")

// BunkerSigner signs events via NIP-46 remote signer. It owns the relay pool
// and subscription behind the bunker connection; Close releases them.
type BunkerSigner struct {
	bunker    *nip46.BunkerClient
	pool      *nostr.SimplePool
	cancel    context.CancelFunc
	publicKey string
}

// NewBunkerSigner creates a signer from a bunker:// URL. The connection
// outlives ctx: ctx bounds and cancels the handshake only, and Close ends the
// connection.
func NewBunkerSigner(ctx context.Context, bunkerURL string) (*BunkerSigner, error) {
	// The bunker URL carries a NIP-46 secret; record it and the URL itself so
	// neither can appear in an error message.
	sanitize.Register(bunkerURL, bunkerURLSecret(bunkerURL))

	// Extract the target pubkey from the bunker URL.
	// The URL format is: bunker://<remote-signer-pubkey>?relay=...&secret=...
	// A remote bunker keeps a client key for that pubkey. A loopback bunker uses
	// the shared local client key.
	targetPubkey, err := extractBunkerTargetPubkey(bunkerURL)
	if err != nil {
		return nil, newOpError("invalid bunker URL", err)
	}

	clientSecretKey, err := bunkerClientKey(bunkerURL, targetPubkey)
	if err != nil {
		return nil, newOpError("bunker client key", err)
	}
	// nip46.RPC waits until the context ends when every relay dial fails.
	// The CLI context ends on SIGINT, so a stopped hotbox would hang publish.
	if err := probeBunkerRelays(ctx, bunkerURL); err != nil {
		return nil, newOpError("connect to bunker", err)
	}

	// nip46.NewBunker ties its subscription to the context it is given, so it
	// gets the signer's own context, not the handshake's.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	pool := nostr.NewSimplePool(runCtx)
	signer := &BunkerSigner{pool: pool, cancel: cancel}
	signer.bunker = nip46.NewBunker(runCtx, clientSecretKey, targetPubkey, bunkerRelays(bunkerURL), pool, func(string) {})

	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, bunkerTimeout)
	defer cancelHandshake()

	_, err = signer.bunker.RPC(handshakeCtx, "connect", []string{targetPubkey, bunkerURLSecret(bunkerURL)})
	// An "already connected" reply means the secret was already used, which is
	// fine when this client key made the original connection.
	if err != nil && !replyContains(err, "already connected") {
		signer.Close()
		return nil, newOpError("connect to bunker", preferContextErr(handshakeCtx, err))
	}

	pubkey, err := signer.bunker.GetPublicKey(handshakeCtx)
	if err != nil {
		signer.Close()
		err = preferContextErr(handshakeCtx, err)
		// "no permission" means the URL's secret was used with a different
		// client key, so the user needs a new bunker URL.
		if replyContains(err, "no permission") {
			err = fmt.Errorf("%w: %w", ErrBunkerSecretUsed, err)
		}
		return nil, newOpError("get public key from bunker", err)
	}
	signer.publicKey = pubkey
	return signer, nil
}

// replyContains reports whether the signer's error reply mentions text.
// nip46.BunkerClient.RPC flattens the reply into a "response error: ..."
// string with no typed error, so the reply text is the only thing to match.
func replyContains(err error, text string) bool {
	return strings.Contains(err.Error(), text)
}

// preferContextErr returns the context error when ctx ended the handshake.
// nip46.BunkerClient.RPC reports cancellation as plain text, which would hide
// context.Canceled and context.DeadlineExceeded from callers.
func preferContextErr(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func bunkerRelays(bunkerURL string) []string {
	parsed, err := url.Parse(bunkerURL)
	if err != nil {
		return nil
	}
	return parsed.Query()["relay"]
}

// bunkerURLSecret extracts the NIP-46 secret query parameter from a bunker
// URL for redaction. It returns "" when the URL cannot be parsed.
func bunkerURLSecret(bunkerURL string) string {
	parsed, err := url.Parse(bunkerURL)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("secret")
}

// extractBunkerTargetPubkey extracts the target pubkey from a bunker URL.
// The URL format is: bunker://<remote-signer-pubkey>?relay=...&secret=...
func extractBunkerTargetPubkey(bunkerURL string) (string, error) {
	parsed, err := url.Parse(bunkerURL)
	if err != nil {
		return "", newOpError("parse URL", err)
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

func bunkerClientKey(bunkerURL, targetPubkey string) (string, error) {
	if loopbackBunker(bunkerURL) {
		return localBunkerClientKey, nil
	}
	return getOrCreateBunkerClientKey(targetPubkey)
}

func loopbackBunker(bunkerURL string) bool {
	parsed, err := url.Parse(bunkerURL)
	if err != nil {
		return false
	}
	relays := parsed.Query()["relay"]
	if len(relays) == 0 {
		return false
	}
	for _, relay := range relays {
		parsedRelay, err := url.Parse(relay)
		if err != nil {
			return false
		}
		switch parsedRelay.Hostname() {
		case "127.0.0.1", "localhost", "::1":
		default:
			return false
		}
	}
	return true
}

// probeBunkerRelays dials each bunker relay. One open TCP port is enough.
// A closed hotbox bunker returns immediately instead of waiting in NIP-46.
func probeBunkerRelays(ctx context.Context, bunkerURL string) error {
	relays := bunkerRelays(bunkerURL)
	if len(relays) == 0 {
		return errors.New("bunker URL has no relay")
	}
	var last error
	for _, raw := range relays {
		address, err := relayDialAddress(raw)
		if err != nil {
			last = err
			continue
		}
		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var dialer net.Dialer
		conn, err := dialer.DialContext(dialCtx, "tcp", address)
		cancel()
		if err == nil {
			conn.Close()
			return nil
		}
		last = err
	}
	return last
}

// relayDialAddress returns the host:port a websocket relay URL connects to,
// using the scheme's default port when the URL has none.
func relayDialAddress(raw string) (string, error) {
	relayURL, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	host := relayURL.Hostname()
	if host == "" {
		return "", errors.New("bunker relay has no host")
	}
	port := relayURL.Port()
	if port == "" {
		switch relayURL.Scheme {
		case "wss", "https":
			port = "443"
		case "ws", "http":
			port = "80"
		default:
			return "", fmt.Errorf("bunker relay scheme %q is not supported", relayURL.Scheme)
		}
	}
	return net.JoinHostPort(host, port), nil
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
			sanitize.Register(key)
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
		return "", fmt.Errorf("generate random key: %w", err)
	}
	clientKey := hex.EncodeToString(keyBytes[:])
	sanitize.Register(clientKey)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return "", fmt.Errorf("create key directory: %w", err)
	}

	if err := WriteSecretFile(keyPath, []byte(clientKey+"\n")); err != nil {
		return "", fmt.Errorf("save client key: %w", err)
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
		return "", fmt.Errorf("get config directory: %w", err)
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
	if err := s.bunker.SignEvent(ctx, event); err != nil {
		return newOpError("sign event", preferContextErr(ctx, err))
	}
	return nil
}

// Close ends the bunker websocket and its relay subscription. It is safe to
// call more than once.
func (s *BunkerSigner) Close() error {
	if s == nil {
		return nil
	}
	// pool.Close only cancels the pool. go-nostr ties each relay to
	// context.Background, so the websocket stays open until it is closed.
	// Take the connections first, then cancel, so the subscription loop does
	// not dial a replacement when these sockets drop.
	var conns []*nostr.Connection
	s.pool.Relays.Range(func(_ string, relay *nostr.Relay) bool {
		if relay != nil && relay.Connection != nil {
			conns = append(conns, relay.Connection)
		}
		return true
	})
	s.cancel()
	s.pool.Close("signer closed")
	for _, conn := range conns {
		_ = conn.Close() // a second Close finds the socket already gone
	}
	return nil
}

// SignEventSet signs a finalized event set without changing any event body.
func SignEventSet(ctx context.Context, signer Signer, events *EventSet) error {
	for i, asset := range events.SoftwareAssets {
		if err := signFinalizedEvent(ctx, signer, asset); err != nil {
			return fmt.Errorf("sign Software Asset event %d: %w", i+1, err)
		}
	}

	if err := signFinalizedEvent(ctx, signer, events.Release); err != nil {
		return fmt.Errorf("sign Software Release event: %w", err)
	}

	if events.AppMetadata != nil {
		if err := signFinalizedEvent(ctx, signer, events.AppMetadata); err != nil {
			return fmt.Errorf("sign Software Application event: %w", err)
		}
	}

	if events.IdentityProof != nil {
		if err := signFinalizedEvent(ctx, signer, events.IdentityProof); err != nil {
			return fmt.Errorf("sign IdentityProof event: %w", err)
		}
	}

	return nil
}

func signFinalizedEvent(ctx context.Context, signer Signer, event *nostr.Event) error {
	if event == nil || event.ID == "" || event.ID != event.GetID() {
		return errors.New("event must be finalized before signing")
	}
	if err := signer.Sign(ctx, event); err != nil {
		return err
	}
	if event.ID != event.GetID() {
		return errors.New("signer changed event body")
	}
	valid, err := event.CheckSignature()
	if err != nil || !valid {
		return errors.New("signer produced an invalid event signature")
	}
	return nil
}
