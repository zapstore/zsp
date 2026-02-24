package nostr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// KindProfile is the kind for profile metadata events (NIP-01).
const KindProfile = 0

const (
	// DefaultRelay is the default relay URL.
	DefaultRelay = "wss://relay.zapstore.dev"

	// RelayTimeout is the timeout for relay operations.
	RelayTimeout = 30 * time.Second
)

// Publisher handles publishing events to relays.
type Publisher struct {
	relayURLs []string
}

// NewPublisher creates a new publisher.
func NewPublisher(relayURLs []string) *Publisher {
	if len(relayURLs) == 0 {
		relayURLs = []string{DefaultRelay}
	}
	return &Publisher{relayURLs: relayURLs}
}

// NewPublisherFromEnv creates a publisher from the RELAY_URLS environment variable.
func NewPublisherFromEnv(relaysEnv string) *Publisher {
	if relaysEnv == "" {
		return NewPublisher(nil)
	}

	urls := strings.Split(relaysEnv, ",")
	var cleaned []string
	for _, url := range urls {
		url = strings.TrimSpace(url)
		if url != "" {
			cleaned = append(cleaned, url)
		}
	}

	return NewPublisher(cleaned)
}

// PublishResult contains the result of publishing to a single relay.
type PublishResult struct {
	RelayURL    string
	Success     bool
	IsDuplicate bool
	Error       error
}

// Publish publishes an event to all configured relays.
func (p *Publisher) Publish(ctx context.Context, event *nostr.Event) []PublishResult {
	results := make([]PublishResult, len(p.relayURLs))

	for i, url := range p.relayURLs {
		results[i] = p.publishToRelay(ctx, url, event)
	}

	return results
}

// isDuplicateError checks if an error indicates the event already exists.
func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "duplicate") || strings.Contains(errStr, "already exists")
}

// publishToRelay publishes an event to a single relay.
func (p *Publisher) publishToRelay(ctx context.Context, url string, event *nostr.Event) PublishResult {
	result := PublishResult{RelayURL: url}

	ctx, cancel := context.WithTimeout(ctx, RelayTimeout)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, url)
	if err != nil {
		result.Error = fmt.Errorf("failed to connect: %w", err)
		return result
	}
	defer relay.Close()

	err = relay.Publish(ctx, *event)
	if err != nil {
		// Check if this is a duplicate error (event already exists)
		if isDuplicateError(err) {
			result.Success = true
			result.IsDuplicate = true
			result.Error = err // Keep error for informational purposes
			return result
		}
		result.Error = fmt.Errorf("failed to publish: %w", err)
		return result
	}

	result.Success = true
	return result
}

// PublishEventSet publishes all events in an event set.
func (p *Publisher) PublishEventSet(ctx context.Context, events *EventSet) (map[string][]PublishResult, error) {
	results := make(map[string][]PublishResult)

	// Publish Software Application
	results["software_application"] = p.Publish(ctx, events.AppMetadata)

	// Publish Software Release
	results["software_release"] = p.Publish(ctx, events.Release)

	// Publish all Software Assets
	for i, asset := range events.SoftwareAssets {
		key := "software_asset"
		if len(events.SoftwareAssets) > 1 {
			key = fmt.Sprintf("software_asset_%d", i+1)
		}
		results[key] = p.Publish(ctx, asset)
	}

	return results, nil
}

// RelayURLs returns the configured relay URLs.
func (p *Publisher) RelayURLs() []string {
	return p.relayURLs
}

// CheckExistingRelease queries all relays for the latest Software Release event (kind 30063).
// It searches by pubkey and d tag (identifier@version).
// Returns the CreatedAt of the most recent existing release, or zero time if none exists.
func (p *Publisher) CheckExistingRelease(ctx context.Context, pubkey, identifier, version string) (time.Time, error) {
	dTag := identifier + "@" + version
	filter := nostr.Filter{
		Kinds:   []int{KindRelease},
		Authors: []string{pubkey},
		Tags: nostr.TagMap{
			"d": []string{dTag},
		},
		Limit: 1,
	}

	var latest nostr.Timestamp
	for _, url := range p.relayURLs {
		event, err := p.queryRelay(ctx, url, filter)
		if err != nil {
			continue
		}
		if event != nil && event.CreatedAt > latest {
			latest = event.CreatedAt
		}
	}

	if latest == 0 {
		return time.Time{}, nil
	}
	return latest.Time(), nil
}

// ExistingAsset contains information about an existing software asset on relays.
type ExistingAsset struct {
	Event    *nostr.Event
	RelayURL string
	Version  string
}

// CheckExistingAsset queries all relays to check if a Software Asset already exists.
// It searches for kind 3063 events with a matching `i` tag (identifier) and `version` tag.
// Returns the first existing Software Asset found, or nil if none exists.
func (p *Publisher) CheckExistingAsset(ctx context.Context, identifier, version string) (*ExistingAsset, error) {
	filter := nostr.Filter{
		Kinds: []int{KindSoftwareAsset},
		Tags: nostr.TagMap{
			"i":       []string{identifier},
			"version": []string{version},
		},
		Limit: 1,
	}

	// Query each relay until we find an existing asset
	for _, url := range p.relayURLs {
		event, err := p.queryRelay(ctx, url, filter)
		if err != nil {
			// Log error but continue to other relays
			continue
		}
		if event != nil {
			// Extract version from the event for confirmation
			existingVersion := ""
			for _, tag := range event.Tags {
				if len(tag) >= 2 && tag[0] == "version" {
					existingVersion = tag[1]
					break
				}
			}
			return &ExistingAsset{
				Event:    event,
				RelayURL: url,
				Version:  existingVersion,
			}, nil
		}
	}

	return nil, nil
}

// queryRelay queries a single relay for events matching the filter.
func (p *Publisher) queryRelay(ctx context.Context, url string, filter nostr.Filter) (*nostr.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, RelayTimeout)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer relay.Close()

	events, err := relay.QuerySync(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query: %w", err)
	}

	if len(events) > 0 {
		return events[0], nil
	}

	return nil, nil
}

// ExistingApp contains information about an existing app on relays.
type ExistingApp struct {
	Event    *nostr.Event
	RelayURL string
}

// CheckExistingApp queries all relays to check if an App Metadata event already exists.
// It searches for kind 32267 events with a matching `d` tag (identifier).
// Returns the first existing App found, or nil if none exists.
func (p *Publisher) CheckExistingApp(ctx context.Context, identifier string) (*ExistingApp, error) {
	filter := nostr.Filter{
		Kinds: []int{KindAppMetadata},
		Tags: nostr.TagMap{
			"d": []string{identifier},
		},
		Limit: 1,
	}

	// Query each relay until we find an existing app
	for _, url := range p.relayURLs {
		event, err := p.queryRelay(ctx, url, filter)
		if err != nil {
			// Log error but continue to other relays
			continue
		}
		if event != nil {
			return &ExistingApp{
				Event:    event,
				RelayURL: url,
			}, nil
		}
	}

	return nil, nil
}

// FetchIdentityProof queries relays for a kind 30509 identity proof event.
// If spkifp is provided, looks for that specific identity; otherwise returns any identity proof.
// Returns nil if no matching event is found.
func (p *Publisher) FetchIdentityProof(ctx context.Context, pubkey, spkifp string) (*nostr.Event, error) {
	filter := nostr.Filter{
		Kinds:   []int{KindIdentityProof},
		Authors: []string{pubkey},
		Limit:   1,
	}

	// If specific SPKIFP provided, filter by d tag
	if spkifp != "" {
		filter.Tags = nostr.TagMap{
			"d": []string{spkifp},
		}
	}

	// Query each relay until we find an identity proof
	for _, url := range p.relayURLs {
		event, err := p.queryRelay(ctx, url, filter)
		if err != nil {
			// Log error but continue to other relays
			continue
		}
		if event != nil {
			return event, nil
		}
	}

	return nil, nil
}

// FetchAllIdentityProofs queries relays for all kind 30509 identity proof events from a pubkey.
func (p *Publisher) FetchAllIdentityProofs(ctx context.Context, pubkey string) ([]*nostr.Event, error) {
	filter := nostr.Filter{
		Kinds:   []int{KindIdentityProof},
		Authors: []string{pubkey},
		Limit:   100,
	}

	var allEvents []*nostr.Event
	seen := make(map[string]bool)

	// Query each relay
	for _, url := range p.relayURLs {
		events, err := p.queryRelayMultiple(ctx, url, filter)
		if err != nil {
			continue
		}
		for _, event := range events {
			if !seen[event.ID] {
				seen[event.ID] = true
				allEvents = append(allEvents, event)
			}
		}
	}

	return allEvents, nil
}

// queryRelayMultiple queries a single relay and returns all matching events.
func (p *Publisher) queryRelayMultiple(ctx context.Context, url string, filter nostr.Filter) ([]*nostr.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, RelayTimeout)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer relay.Close()

	return relay.QuerySync(ctx, filter)
}

// BuildIdentityProofEvent creates a kind 30509 identity proof event per NIP-C1.
// The createdAt timestamp must match the one used when signing the proof message.
func BuildIdentityProofEvent(tags nostr.Tags, pubkey string, createdAt int64) *nostr.Event {
	return &nostr.Event{
		Kind:      KindIdentityProof,
		PubKey:    pubkey,
		CreatedAt: nostr.Timestamp(createdAt),
		Tags:      tags,
		Content:   "",
	}
}
