package nostr

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

//go:embed templates/nip07.html
var nip07HTML string

// DefaultNIP07Port is the default port for the NIP-07 browser signer.
const DefaultNIP07Port = 17007

// NIP07Signer signs events via browser NIP-07 extension.
type NIP07Signer struct {
	publicKey string
	port      int
	server    *http.Server
	listener  net.Listener

	mu            sync.Mutex
	mode          string // "idle", "publicKey", "sign"
	eventsToSign  []map[string]any
	pubkeyResult  chan string
	signingResult chan []map[string]any
	shouldClose   bool
	browserOpened bool

	// Security: Session nonce to prevent replay attacks and CSRF
	sessionNonce string
}

// NIP07SignerOptions contains options for creating a NIP-07 signer.
type NIP07SignerOptions struct {
	Port int // Custom port (0 = use default)
}

// NewNIP07Signer creates and initializes a NIP-07 browser signer.
// If port is 0, the default port (17007) is used.
func NewNIP07Signer(ctx context.Context, port int) (*NIP07Signer, error) {
	if port == 0 {
		port = DefaultNIP07Port
	}

	// Security: Generate a random session nonce to prevent CSRF and replay attacks
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("failed to generate session nonce: %w", err)
	}
	sessionNonce := hex.EncodeToString(nonceBytes)

	s := &NIP07Signer{
		port:          port,
		mode:          "idle",
		pubkeyResult:  make(chan string, 1),
		signingResult: make(chan []map[string]any, 1),
		sessionNonce:  sessionNonce,
	}

	// Start server
	if err := s.startServer(); err != nil {
		return nil, fmt.Errorf("failed to start NIP-07 server: %w", err)
	}

	// Get public key to verify extension is available
	pubkey, err := s.getPublicKey(ctx)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("failed to get public key from NIP-07 extension: %w", err)
	}
	s.publicKey = pubkey

	return s, nil
}

func (s *NIP07Signer) Type() SignerType {
	return SignerNIP07
}

func (s *NIP07Signer) PublicKey() string {
	return s.publicKey
}

func (s *NIP07Signer) Sign(ctx context.Context, event *nostr.Event) error {
	// For single event signing, use batch signing with one event
	return s.SignBatch(ctx, []*nostr.Event{event})
}

// SignBatch signs multiple events in a single browser interaction.
func (s *NIP07Signer) SignBatch(ctx context.Context, events []*nostr.Event) error {
	if len(events) == 0 {
		return nil
	}

	// Prepare events for signing (strip signature fields)
	eventMaps := make([]map[string]any, len(events))
	for i, event := range events {
		eventMaps[i] = map[string]any{
			"kind":       event.Kind,
			"content":    event.Content,
			"tags":       event.Tags,
			"created_at": int64(event.CreatedAt),
		}
	}

	s.mu.Lock()
	s.mode = "sign"
	s.eventsToSign = eventMaps
	s.mu.Unlock()

	// Wait for result
	select {
	case signedEvents := <-s.signingResult:
		if len(signedEvents) != len(events) {
			return fmt.Errorf("expected %d signed events, got %d", len(events), len(signedEvents))
		}

		// Update events with signed values
		for i, signed := range signedEvents {
			events[i].ID = signed["id"].(string)
			events[i].PubKey = signed["pubkey"].(string)
			events[i].Sig = signed["sig"].(string)
		}

		s.mu.Lock()
		s.mode = "idle"
		s.eventsToSign = nil
		s.mu.Unlock()

		return nil

	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *NIP07Signer) Close() error {
	s.mu.Lock()
	s.shouldClose = true
	s.mu.Unlock()

	// Give browser time to detect shutdown
	time.Sleep(500 * time.Millisecond)

	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.server.Shutdown(ctx)
	}
	return nil
}

func (s *NIP07Signer) getPublicKey(ctx context.Context) (string, error) {
	s.mu.Lock()
	s.mode = "publicKey"
	s.mu.Unlock()

	if err := s.openBrowser(); err != nil {
		return "", err
	}

	select {
	case pubkey := <-s.pubkeyResult:
		s.mu.Lock()
		s.mode = "idle"
		s.mu.Unlock()
		return pubkey, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(120 * time.Second):
		return "", fmt.Errorf("timeout waiting for public key from NIP-07 extension")
	}
}

func (s *NIP07Signer) startServer() error {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return err
	}
	s.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/state", s.securityMiddleware(s.handleState))
	mux.HandleFunc("/api/shutdown", s.securityMiddleware(s.handleShutdown))
	mux.HandleFunc("/public-key", s.securityMiddleware(s.handlePublicKey))
	mux.HandleFunc("/signed-events", s.securityMiddleware(s.handleSignedEvents))

	s.server = &http.Server{Handler: mux}

	go s.server.Serve(listener)
	return nil
}

// securityMiddleware adds security headers and validates request origin.
// This protects against CSRF attacks from malicious websites trying to
// interact with the local signing server.
func (s *NIP07Signer) securityMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Add security headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")

		// Validate Origin header for non-GET requests (CSRF protection)
		if r.Method != http.MethodGet {
			origin := r.Header.Get("Origin")
			expectedOrigin := fmt.Sprintf("http://localhost:%d", s.port)
			expectedOrigin127 := fmt.Sprintf("http://127.0.0.1:%d", s.port)

			if origin != "" && origin != expectedOrigin && origin != expectedOrigin127 {
				http.Error(w, "Forbidden: invalid origin", http.StatusForbidden)
				return
			}
		}

		next(w, r)
	}
}

func (s *NIP07Signer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(nip07HTML))
}

func (s *NIP07Signer) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	state := map[string]any{
		"mode":  s.mode,
		"data":  s.eventsToSign,
		"nonce": s.sessionNonce, // Include nonce for client verification
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

func (s *NIP07Signer) handleShutdown(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	shouldClose := s.shouldClose
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"shouldClose": shouldClose})
}

func (s *NIP07Signer) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Security: Verify session nonce from request header
	requestNonce := r.Header.Get("X-Session-Nonce")
	if requestNonce != s.sessionNonce {
		http.Error(w, "Invalid session nonce", http.StatusForbidden)
		return
	}

	var data struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Security: Validate pubkey format
	if !nostr.IsValidPublicKey(data.PublicKey) {
		http.Error(w, "Invalid public key format", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	// Non-blocking send
	select {
	case s.pubkeyResult <- data.PublicKey:
	default:
	}
}

func (s *NIP07Signer) handleSignedEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Security: Verify session nonce from request header
	requestNonce := r.Header.Get("X-Session-Nonce")
	if requestNonce != s.sessionNonce {
		http.Error(w, "Invalid session nonce", http.StatusForbidden)
		return
	}

	var signedEvents []map[string]any
	if err := json.NewDecoder(r.Body).Decode(&signedEvents); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Security: Verify each signed event has a valid signature
	for i, eventMap := range signedEvents {
		// Convert map to nostr.Event for verification
		eventJSON, err := json.Marshal(eventMap)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid event %d: %v", i, err), http.StatusBadRequest)
			return
		}
		var event nostr.Event
		if err := json.Unmarshal(eventJSON, &event); err != nil {
			http.Error(w, fmt.Sprintf("Invalid event %d: %v", i, err), http.StatusBadRequest)
			return
		}

		// Verify the signature
		valid, err := event.CheckSignature()
		if err != nil || !valid {
			http.Error(w, fmt.Sprintf("Invalid signature on event %d", i), http.StatusBadRequest)
			return
		}

		// Verify the pubkey matches our expected pubkey (if we have one)
		if s.publicKey != "" && event.PubKey != s.publicKey {
			http.Error(w, fmt.Sprintf("Event %d pubkey mismatch", i), http.StatusBadRequest)
			return
		}
	}

	w.WriteHeader(http.StatusOK)

	// Non-blocking send
	select {
	case s.signingResult <- signedEvents:
	default:
	}
}

func (s *NIP07Signer) openBrowser() error {
	s.mu.Lock()
	if s.browserOpened {
		s.mu.Unlock()
		return nil
	}
	s.browserOpened = true
	s.mu.Unlock()

	url := fmt.Sprintf("http://localhost:%d/", s.port)

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return cmd.Start()
}


