package nostr

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/zapstore/zsp/v2/internal/sanitize"
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

func TestNewSignerRejectsHex(t *testing.T) {
	secret := gonostr.GeneratePrivateKey()
	pubkey, err := gonostr.GetPublicKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"hex private key": secret,
		"hex public key":  pubkey,
		"short hex":       "abcd",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewSigner(t.Context(), value)
			if err == nil || !strings.Contains(err.Error(), "invalid SIGN_WITH format") {
				t.Fatalf("NewSigner(%s) error = %v, want invalid SIGN_WITH format", name, err)
			}
		})
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

func TestNewBunkerSignerReportsDeadLoopback(t *testing.T) {
	secretKey := gonostr.GeneratePrivateKey()
	pubkey, err := gonostr.GetPublicKey(secretKey)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "sekrit-bunker-value"
	raw := "bunker://" + pubkey + "?relay=ws%3A%2F%2F127.0.0.1%3A1&secret=" + secret
	start := time.Now()
	_, err = NewBunkerSigner(t.Context(), raw)
	if err == nil {
		t.Fatal("NewBunkerSigner error = nil")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("dead bunker took %s", time.Since(start))
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the bunker secret: %q", err.Error())
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
	nsec, err := nip19.EncodePrivateKey(gonostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(t.Context(), nsec)
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

func TestRelayDialAddress(t *testing.T) {
	tests := []struct {
		relay   string
		want    string
		wantErr bool
	}{
		{relay: "wss://relay.nsec.app", want: "relay.nsec.app:443"},
		{relay: "wss://relay.nsec.app/path", want: "relay.nsec.app:443"},
		{relay: "ws://relay.example", want: "relay.example:80"},
		{relay: "ws://127.0.0.1:3334", want: "127.0.0.1:3334"},
		{relay: "wss://relay.example:8443", want: "relay.example:8443"},
		{relay: "ws://[::1]", want: "[::1]:80"},
		{relay: "ws://[::1]:3334", want: "[::1]:3334"},
		{relay: "relay.example", wantErr: true},
		{relay: "tcp://relay.example", wantErr: true},
		{relay: "wss://", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.relay, func(t *testing.T) {
			got, err := relayDialAddress(tt.relay)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("relayDialAddress(%q) = %q, want error", tt.relay, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("relayDialAddress(%q): %v", tt.relay, err)
			}
			if got != tt.want {
				t.Errorf("relayDialAddress(%q) = %q, want %q", tt.relay, got, tt.want)
			}
		})
	}
}

func TestBunkerSignerCloseDropsConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	closedConn := make(chan struct{})
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			close(accepted)
			defer conn.Close(websocket.StatusNormalClosure, "")
			for {
				if _, _, err := conn.Read(t.Context()); err != nil {
					close(closedConn)
					return
				}
			}
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	relayURL := "ws://" + ln.Addr().String()
	conn, err := gonostr.NewConnection(t.Context(), relayURL, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(t.Context())
	pool := gonostr.NewSimplePool(runCtx)
	relay := gonostr.NewRelay(t.Context(), relayURL)
	relay.Connection = conn
	pool.Relays.Store(relayURL, relay)
	signer := &BunkerSigner{pool: pool, cancel: cancel}

	wait, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	select {
	case <-accepted:
	case <-wait.Done():
		t.Fatal("bunker did not accept the connection")
	}
	if err := signer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closedConn:
	case <-wait.Done():
		t.Fatal("bunker socket still open after Close")
	}
	if err := signer.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestBunkerSignerCloseIsIdempotentAndEndsPool(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	signer := &BunkerSigner{pool: gonostr.NewSimplePool(runCtx), cancel: cancel}
	if err := signer.Close(); err != nil {
		t.Fatal(err)
	}
	if runCtx.Err() == nil {
		t.Fatal("Close did not cancel the signer context")
	}
	if err := signer.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	var missing *BunkerSigner
	if err := missing.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

func TestPreferContextErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newOpError("sign event", preferContextErr(ctx, errors.New("context canceled")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	other := errors.New("response error: no permission")
	if got := preferContextErr(context.Background(), other); got != other {
		t.Fatalf("preferContextErr(live ctx) = %v, want the original error", got)
	}
}
