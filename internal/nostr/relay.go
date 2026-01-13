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

// NewPublisherFromEnv creates a publisher from the RELAYS environment variable.
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

	// Publish in order: app metadata, release, asset
	eventList := []struct {
		name  string
		event *nostr.Event
	}{
		{"app_metadata", events.AppMetadata},
		{"release", events.Release},
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

