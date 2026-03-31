package nostr

import (
	"context"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
)

// KindCommunity is the Communikeys community definition event (NIP kind:10222).
// It is authored by the community's own pubkey and carries the relay + Blossom
// configuration for that community.
const KindCommunity = 10222

// DefaultBootstrapRelays are queried to resolve kind:10222 events when RELAY_URLS
// is not explicitly set by the operator.
var DefaultBootstrapRelays = []string{
	"wss://relay.primal.net",
	"wss://relay.damus.io",
	"wss://nos.lol",
}

// CommunityConfig holds the relay and Blossom configuration extracted from a
// kind:10222 event.
type CommunityConfig struct {
	// RelayURLs are the community's designated publish relays (`r` tags).
	RelayURLs []string
	// BlossomURL is the first Blossom server listed in `blossom` tags, or empty.
	BlossomURL string
}

// ResolveCommunityConfigs fetches kind:10222 events for each community pubkey
// that is not the Zapstore default community and merges them into a single
// CommunityConfig.
//
// Resolution rules:
//   - RelayURLs: union of all `r` tags across all resolved community events
//   - BlossomURL: first `blossom` tag found across community events (in order)
//
// bootstrapRelays are used only for the lookup queries. If nil or empty the
// DefaultBootstrapRelays are used. This allows operators to point RELAY_URLS at
// their own infra to locate their community event before resolution redirects
// subsequent publishes.
//
// Returns (nil, nil) when every community in the list is the default Zapstore
// community — meaning no resolution is needed and callers should keep their
// existing defaults.
func ResolveCommunityConfigs(ctx context.Context, communities []string, bootstrapRelays []string) (*CommunityConfig, error) {
	// Filter out the default Zapstore community — no resolution needed for it.
	var nonDefault []string
	for _, c := range communities {
		if c != DefaultCommunity {
			nonDefault = append(nonDefault, c)
		}
	}
	if len(nonDefault) == 0 {
		return nil, nil
	}

	if len(bootstrapRelays) == 0 {
		bootstrapRelays = DefaultBootstrapRelays
	}

	bootstrap := NewPublisher(bootstrapRelays)

	merged := &CommunityConfig{}
	seen := make(map[string]bool)

	for _, pubkey := range nonDefault {
		event, err := fetchCommunityEvent(ctx, bootstrap, pubkey)
		if err != nil {
			return nil, fmt.Errorf("fetching community event for %s: %w", pubkey[:8], err)
		}
		if event == nil {
			continue
		}
		cfg := parseCommunityConfig(event)

		for _, r := range cfg.RelayURLs {
			if !seen[r] {
				seen[r] = true
				merged.RelayURLs = append(merged.RelayURLs, r)
			}
		}
		if merged.BlossomURL == "" {
			merged.BlossomURL = cfg.BlossomURL
		}
	}

	return merged, nil
}

// fetchCommunityEvent queries bootstrap relays for the most recent kind:10222
// event authored by communityPubkey. Returns nil if not found on any relay.
func fetchCommunityEvent(ctx context.Context, bootstrap *Publisher, communityPubkey string) (*nostr.Event, error) {
	filter := nostr.Filter{
		Kinds:   []int{KindCommunity},
		Authors: []string{communityPubkey},
		Limit:   1,
	}

	for _, url := range bootstrap.relayURLs {
		event, err := bootstrap.queryRelay(ctx, url, filter)
		if err != nil {
			// Non-fatal: try next relay.
			continue
		}
		if event != nil {
			return event, nil
		}
	}

	return nil, nil
}

// parseCommunityConfig extracts relay and Blossom configuration from a kind:10222
// event's tags.
func parseCommunityConfig(event *nostr.Event) *CommunityConfig {
	cfg := &CommunityConfig{}
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "r":
			cfg.RelayURLs = append(cfg.RelayURLs, tag[1])
		case "blossom":
			if cfg.BlossomURL == "" {
				cfg.BlossomURL = tag[1]
			}
		}
	}
	return cfg
}
