package nostr

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestParseCommunityConfig(t *testing.T) {
	tests := []struct {
		name          string
		tags          nostr.Tags
		wantRelays    []string
		wantBlossom   string
	}{
		{
			name: "relay and blossom tags",
			tags: nostr.Tags{
				{"r", "wss://relay.example.com"},
				{"r", "wss://relay2.example.com"},
				{"blossom", "https://blossom.example.com"},
			},
			wantRelays:  []string{"wss://relay.example.com", "wss://relay2.example.com"},
			wantBlossom: "https://blossom.example.com",
		},
		{
			name: "first blossom wins",
			tags: nostr.Tags{
				{"r", "wss://relay.example.com"},
				{"blossom", "https://first.blossom.com"},
				{"blossom", "https://second.blossom.com"},
			},
			wantRelays:  []string{"wss://relay.example.com"},
			wantBlossom: "https://first.blossom.com",
		},
		{
			name: "relays only, no blossom",
			tags: nostr.Tags{
				{"r", "wss://relay.example.com"},
				{"k", "32267"},
			},
			wantRelays:  []string{"wss://relay.example.com"},
			wantBlossom: "",
		},
		{
			name:        "empty tags",
			tags:        nostr.Tags{},
			wantRelays:  nil,
			wantBlossom: "",
		},
		{
			name: "unrelated tags are ignored",
			tags: nostr.Tags{
				{"d", "some-id"},
				{"name", "Test Community"},
				{"content", "Apps"},
				{"k", "32267"},
				{"r", "wss://relay.example.com"},
				{"mint", "https://mint.example.com", "cashu"},
			},
			wantRelays:  []string{"wss://relay.example.com"},
			wantBlossom: "",
		},
		{
			name: "short tags are skipped",
			tags: nostr.Tags{
				{"r"},
				{"blossom"},
				{"r", "wss://relay.example.com"},
			},
			wantRelays:  []string{"wss://relay.example.com"},
			wantBlossom: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := &nostr.Event{
				Kind: KindCommunity,
				Tags: tc.tags,
			}
			cfg := parseCommunityConfig(event)

			if len(cfg.RelayURLs) != len(tc.wantRelays) {
				t.Errorf("relay count: got %d, want %d", len(cfg.RelayURLs), len(tc.wantRelays))
			} else {
				for i, r := range cfg.RelayURLs {
					if r != tc.wantRelays[i] {
						t.Errorf("relay[%d]: got %q, want %q", i, r, tc.wantRelays[i])
					}
				}
			}

			if cfg.BlossomURL != tc.wantBlossom {
				t.Errorf("blossom: got %q, want %q", cfg.BlossomURL, tc.wantBlossom)
			}
		})
	}
}

func TestResolveCommunityConfigs_DefaultCommunityReturnsNil(t *testing.T) {
	// Passing only the default Zapstore community should skip resolution
	// and return (nil, nil) — no network call is made.
	cfg, err := ResolveCommunityConfigs(nil, []string{DefaultCommunity}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil CommunityConfig for default community, got %+v", cfg)
	}
}

func TestResolveCommunityConfigs_EmptyCommunitiesReturnsNil(t *testing.T) {
	cfg, err := ResolveCommunityConfigs(nil, []string{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil CommunityConfig for empty communities, got %+v", cfg)
	}
}
