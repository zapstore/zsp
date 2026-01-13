package nostr

import (
	"context"
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
	s := &NIP07Signer{
		port:          port,
		mode:          "idle",
		pubkeyResult:  make(chan string, 1),
		signingResult: make(chan []map[string]any, 1),
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
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/shutdown", s.handleShutdown)
	mux.HandleFunc("/public-key", s.handlePublicKey)
	mux.HandleFunc("/signed-events", s.handleSignedEvents)

	s.server = &http.Server{Handler: mux}

	go s.server.Serve(listener)
	return nil
}

func (s *NIP07Signer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(nip07HTML))
}

func (s *NIP07Signer) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	state := map[string]any{
		"mode": s.mode,
		"data": s.eventsToSign,
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

	var data struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
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

	var signedEvents []map[string]any
	if err := json.NewDecoder(r.Body).Decode(&signedEvents); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
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

// HTML page for NIP-07 interaction
const nip07HTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>zsp - NIP-07 Signer</title>
  <style>
    * { box-sizing: border-box; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
      max-width: 700px;
      margin: 0 auto;
      padding: 24px;
      background: linear-gradient(135deg, #1a0a1f 0%, #12071a 50%, #0d0510 100%);
      color: #e8e0f0;
      line-height: 1.5;
      min-height: 100vh;
    }
    h1 { color: #e879f9; margin-bottom: 8px; }
    .subtitle { color: #a78baf; margin-bottom: 24px; }
    .status {
      padding: 16px;
      border-radius: 8px;
      margin-bottom: 16px;
      border: 1px solid #3d1f47;
    }
    .status.waiting { background: rgba(26, 10, 31, 0.8); }
    .status.success { background: linear-gradient(135deg, rgba(147, 51, 234, 0.3), rgba(219, 39, 119, 0.3)); border-color: #9333ea; color: #e879f9; }
    .status.error { background: linear-gradient(135deg, #3d1f1f, #5c1f1f); border-color: #991b1b; color: #fca5a5; }
    pre {
      background: #0d0510;
      padding: 16px;
      border-radius: 8px;
      overflow-x: auto;
      font-size: 13px;
      border: 1px solid #2d1535;
    }
    button {
      background: linear-gradient(135deg, #9333ea, #db2777);
      color: white;
      border: none;
      padding: 12px 24px;
      border-radius: 6px;
      font-size: 16px;
      cursor: pointer;
      font-weight: 500;
      box-shadow: 0 4px 12px rgba(147, 51, 234, 0.4);
      transition: all 0.2s ease;
    }
    button:hover { transform: translateY(-2px); box-shadow: 0 6px 20px rgba(147, 51, 234, 0.5); }
    button:disabled { background: #1a0a1f; color: #6b5577; cursor: not-allowed; box-shadow: none; transform: none; }
    .event {
      background: rgba(26, 10, 31, 0.8);
      border: 1px solid #3d1f47;
      border-radius: 8px;
      padding: 16px;
      margin-bottom: 12px;
    }
    .event.signed { border-color: #9333ea; box-shadow: 0 0 12px rgba(147, 51, 234, 0.3); }
    .json-key { color: #c084fc; }
    .json-string { color: #f0abfc; }
    .json-number { color: #f472b6; }
    #idle-section, #publicKey-section, #sign-section { display: none; }
    .spinner {
      display: inline-block;
      width: 16px;
      height: 16px;
      border: 2px solid #3d1f47;
      border-top-color: #e879f9;
      border-radius: 50%;
      animation: spin 1s linear infinite;
      margin-right: 8px;
      vertical-align: middle;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
  </style>
</head>
<body>
  <h1>⚡ zsp</h1>
  <p class="subtitle">Sign Nostr events with your browser extension</p>

  <div id="idle-section">
    <div class="status waiting"><span class="spinner"></span>Waiting for operation from terminal...</div>
  </div>

  <div id="publicKey-section">
    <div id="pk-status" class="status waiting"><span class="spinner"></span>Requesting public key from extension...</div>
  </div>

  <div id="sign-section">
    <div id="sign-status" class="status waiting">Ready to sign events</div>
    <button id="sign-all">Sign All Events</button>
    <p style="margin-top: 12px; color: #a78baf;">After signing, this window will show a completion message and you can close it.</p>
    <div id="events-container" style="margin-top: 16px;"></div>
  </div>

  <script type="module">
    function highlightJSON(json) {
      return json.replace(/("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?)/g, function (match) {
        let cls = 'json-number';
        if (/^"/.test(match)) {
          if (/:$/.test(match)) {
            cls = 'json-key';
          } else {
            cls = 'json-string';
          }
        }
        return '<span class="' + cls + '">' + match + '</span>';
      });
    }

    const idleSection = document.getElementById('idle-section');
    const publicKeySection = document.getElementById('publicKey-section');
    const signSection = document.getElementById('sign-section');

    // Wait for extension injection
    await new Promise(r => setTimeout(r, 100));

    async function waitForNostr(timeout = 4000) {
      const start = Date.now();
      while (Date.now() - start < timeout) {
        if (window.nostr) return true;
        await new Promise(r => setTimeout(r, 100));
      }
      return !!window.nostr;
    }

    const hasNostr = await waitForNostr();
    if (!hasNostr) {
      document.body.innerHTML = '<div class="status error"><h2>No Nostr extension detected</h2><p>Please install a NIP-07 compatible browser extension (e.g., Alby, nos2x, Flamingo).</p></div>';
    } else {
      let displayedSignature = null;

      async function checkState() {
        try {
          const resp = await fetch('/api/state');
          const state = await resp.json();

          idleSection.style.display = state.mode === 'idle' ? 'block' : 'none';
          publicKeySection.style.display = state.mode === 'publicKey' ? 'block' : 'none';
          signSection.style.display = state.mode === 'sign' ? 'block' : 'none';

          if (state.mode === 'publicKey') {
            handlePublicKey();
          }
          if (state.mode === 'sign') {
            handleSigning(state.data);
          }
        } catch (e) {
          console.error('State check error:', e);
        }
      }

      async function handlePublicKey() {
        const status = document.getElementById('pk-status');
        try {
          const pubkey = await window.nostr.getPublicKey();
          status.className = 'status success';
          status.innerHTML = '✓ Public key retrieved: ' + pubkey.slice(0, 16) + '...';

          await fetch('/public-key', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ publicKey: pubkey })
          });
        } catch (e) {
          status.className = 'status error';
          status.textContent = 'Error: ' + e.message;
        }
      }

      async function handleSigning(events) {
        const sig = JSON.stringify(events || []);
        if (sig === displayedSignature) return;
        displayedSignature = sig;

        const status = document.getElementById('sign-status');
        const container = document.getElementById('events-container');
        const btn = document.getElementById('sign-all');

        container.innerHTML = '';
        if (!events || events.length === 0) {
          status.textContent = 'No events to sign.';
          btn.style.display = 'none';
          return;
        }

        btn.style.display = 'inline-block';
        btn.disabled = false;

        const kindCounts = events.reduce((acc, e) => {
          acc[e.kind] = (acc[e.kind] || 0) + 1;
          return acc;
        }, {});
        const breakdown = Object.entries(kindCounts).map(([k, c]) => 'kind ' + k + ' (' + c + ')').join(', ');
        status.textContent = 'Ready to sign: ' + breakdown;

        events.forEach((event, i) => {
          const div = document.createElement('div');
          div.className = 'event';
          div.id = 'event-' + i;
          const pre = document.createElement('pre');
          pre.innerHTML = highlightJSON(JSON.stringify(event, null, 2));
          div.appendChild(pre);
          container.appendChild(div);
        });

        btn.onclick = async () => {
          btn.disabled = true;
          status.innerHTML = '<span class="spinner"></span>Signing events...';

          try {
            const signed = [];
            for (let i = 0; i < events.length; i++) {
              status.innerHTML = '<span class="spinner"></span>Signing event ' + (i + 1) + ' of ' + events.length + '...';
              const ev = { ...events[i] };
              delete ev.id;
              delete ev.pubkey;
              delete ev.sig;
              const signedEv = await window.nostr.signEvent(ev);
              signed.push(signedEv);

              const div = document.getElementById('event-' + i);
              div.className = 'event signed';
              div.querySelector('pre').innerHTML = highlightJSON(JSON.stringify(signedEv, null, 2));
            }

            status.className = 'status success';
            status.innerHTML = '✓ All events signed! Sending to terminal...';

            const resp = await fetch('/signed-events', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify(signed)
            });

            if (resp.ok) {
              status.innerHTML = '✓ Success! Events signed and sent. You can close this tab.';
            }
          } catch (e) {
            status.className = 'status error';
            status.textContent = 'Error: ' + e.message;
            btn.disabled = false;
          }
        };
      }

      async function checkShutdown() {
        try {
          const resp = await fetch('/api/shutdown');
          const data = await resp.json();
          if (data.shouldClose) {
            document.body.innerHTML = '<div class="status success"><h2>✓ Done</h2><p>All events signed. You can close this tab.</p></div>';
          }
        } catch (e) {}
      }

      checkState();
      setInterval(checkState, 1000);
      setInterval(checkShutdown, 1000);
    }
  </script>
</body>
</html>`

