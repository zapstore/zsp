package nostr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

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
	RelayURL string
	Success  bool
	Error    error
}

// Publish publishes an event to all configured relays.
func (p *Publisher) Publish(ctx context.Context, event *nostr.Event) []PublishResult {
	results := make([]PublishResult, len(p.relayURLs))

	for i, url := range p.relayURLs {
		results[i] = p.publishToRelay(ctx, url, event)
	}

	return results
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
		result.Error = fmt.Errorf("failed to publish: %w", err)
		return result
	}

	result.Success = true
	return result
}

// PublishEventSet publishes all events in an event set.
func (p *Publisher) PublishEventSet(ctx context.Context, events *EventSet) (map[string][]PublishResult, error) {
	results := make(map[string][]PublishResult)

	// Publish in order: Software Application, Software Release, Software Asset
	eventList := []struct {
		name  string
		event *nostr.Event
	}{
		{"software_application", events.AppMetadata},
		{"software_release", events.Release},
		{"software_asset", events.SoftwareAsset},
	}

	for _, item := range eventList {
		results[item.name] = p.Publish(ctx, item.event)
	}

	return results, nil
}

// RelayURLs returns the configured relay URLs.
func (p *Publisher) RelayURLs() []string {
	return p.relayURLs
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
