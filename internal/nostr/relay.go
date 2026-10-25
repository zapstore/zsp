package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/sanitize"
)

const (
	// DefaultRelay is the default relay URL.
	DefaultRelay = "wss://relay.zapstore.dev"
	// DefaultIndexerPubkey identifies Zapstore's default catalog for display
	// only. It is never used as a publication authorization decision.
	DefaultIndexerPubkey = "78ce6faa72264387284e647ba6938995735ec8c7d5c5a65737e55130f026307d"

	// RelayTimeout is the timeout for relay operations.
	RelayTimeout = 30 * time.Second
)

// Publisher handles publishing events to relays.
type Publisher struct {
	relayURLs []string
}

// AppEventLocations records the relays that list an application.
type AppEventLocations struct {
	ApplicationRelays []string
	CheckedRelays     []string
	ApplicationCount  int
}

// RelayClassification is advisory relay metadata used by the wizard.
type RelayClassification struct {
	RelayURL         string
	PublisherPubkey  string
	IsDefaultIndexer bool
}

// NewPublisher creates a new publisher.
func NewPublisher(relayURLs []string) *Publisher {
	if len(relayURLs) == 0 {
		relayURLs = []string{DefaultRelay}
	}
	return &Publisher{relayURLs: relayURLs}
}

// EnsureReachable verifies that at least one configured relay accepts a
// connection. Relays are probed concurrently. It returns the URLs that could
// not be reached.
func (p *Publisher) EnsureReachable(ctx context.Context) ([]string, error) {
	errs := make([]error, len(p.relayURLs))
	var wg sync.WaitGroup
	for i, relayURL := range p.relayURLs {
		wg.Go(func() { errs[i] = reachRelay(ctx, relayURL) })
	}
	wg.Wait()

	unreachable := make([]string, 0)
	var failures []error
	for i, err := range errs {
		if err != nil {
			unreachable = append(unreachable, p.relayURLs[i])
			failures = append(failures, relayFailure(p.relayURLs[i], err))
		}
	}
	if len(failures) < len(p.relayURLs) {
		return unreachable, nil
	}
	return unreachable, relayQueryError("reachability", failures)
}

func reachRelay(ctx context.Context, relayURL string) error {
	ctx, cancel := context.WithTimeout(ctx, RelayTimeout)
	defer cancel()
	if path, ok := socketPath(relayURL); ok {
		conn, err := dialSocket(ctx, path)
		if err != nil {
			return newOpError("connect", err)
		}
		_ = conn.Close() // the probe is over; a close failure does not change the answer
		return nil
	}
	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		return newOpError("connect", err)
	}
	_ = relay.Close() // the probe is over; a close failure does not change the answer
	return nil
}

// relayFailure renders a relay failure without credentials: the relay URL is
// reduced to its public form and the cause is sanitized when printed.
func relayFailure(relayURL string, err error) error {
	public := sanitize.URL(relayURL)
	if public == "" {
		public = "relay"
	}
	return newOpError(public, err)
}

// PublishResult contains the result of publishing to a single relay.
type PublishResult struct {
	RelayURL    string
	Success     bool
	IsDuplicate bool
	Error       error
	// Message is the sanitized reason the relay gave, or the sanitized failure
	// text when no relay answered. It carries no operation prefix.
	Message string
}

// Publish publishes an event to all configured relays concurrently. Results
// are in the order of the configured relays.
func (p *Publisher) Publish(ctx context.Context, event *nostr.Event) []PublishResult {
	results := make([]PublishResult, len(p.relayURLs))
	var wg sync.WaitGroup
	for i, relayURL := range p.relayURLs {
		wg.Go(func() { results[i] = p.publishToRelay(ctx, relayURL, event) })
	}
	wg.Wait()
	return results
}

// publishToRelay publishes an event to a single relay.
func (p *Publisher) publishToRelay(ctx context.Context, relayURL string, event *nostr.Event) PublishResult {
	result := PublishResult{RelayURL: relayURL}
	fail := func(err error) PublishResult {
		result.Error = err
		result.Message = errorReason(err)
		return result
	}
	// A relay's free-form duplicate wording is not authoritative. The exact
	// signed event must be present before it counts as accepted.
	duplicate := func(err error) PublishResult {
		result.Success = true
		result.IsDuplicate = true
		result.Error = err // kept for diagnostics only
		result.Message = errorReason(err)
		return result
	}

	ctx, cancel := context.WithTimeout(ctx, RelayTimeout)
	defer cancel()

	if path, ok := socketPath(relayURL); ok {
		err := publishSocket(ctx, path, event)
		if err == nil {
			result.Success = true
			return result
		}
		if isDuplicateError(err) && socketEventExists(ctx, path, event.ID) {
			return duplicate(err)
		}
		return fail(err)
	}

	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		return fail(newOpError("connect", err))
	}
	defer relay.Close()

	err = relay.Publish(ctx, *event)
	if err != nil {
		err = newOpError("publish", relayRejection(err))
		if isDuplicateError(err) && eventExists(ctx, relay, event.ID) {
			return duplicate(err)
		}
		return fail(err)
	}
	// go-nostr returns nil when the connection drops after the write but before
	// the relay's OK, so a nil error alone does not prove acceptance.
	if !relay.IsConnected() && !storedOnRelay(ctx, relayURL, event.ID) {
		return fail(newOpError("publish", errors.New("connection closed before the relay confirmed the event")))
	}

	result.Success = true
	return result
}

// Served reports whether the relay returns event to a read for its id and
// author. A relay that accepted the event but holds it back, as Zapstore's
// buffer zone does until it has read the APK, returns false. So does a
// failed read.
func (p *Publisher) Served(ctx context.Context, relayURL string, event *nostr.Event) bool {
	ctx, cancel := context.WithTimeout(ctx, RelayTimeout)
	defer cancel()
	filter := nostr.Filter{IDs: []string{event.ID}, Authors: []string{event.PubKey}, Limit: 1}
	var events []*nostr.Event
	if path, ok := socketPath(relayURL); ok {
		found, err := querySocket(ctx, path, filter)
		if err != nil {
			return false
		}
		events = found
	} else {
		relay, err := nostr.RelayConnect(ctx, relayURL)
		if err != nil {
			return false
		}
		defer relay.Close()
		found, err := relay.QuerySync(ctx, filter)
		if err != nil {
			return false
		}
		events = found
	}
	for _, found := range events {
		if found != nil && found.ID == event.ID {
			return true
		}
	}
	return false
}

// storedOnRelay opens a fresh connection and reports whether the relay has
// the event.
func storedOnRelay(ctx context.Context, relayURL, eventID string) bool {
	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		return false
	}
	defer relay.Close()
	return eventExists(ctx, relay, eventID)
}

func eventExists(ctx context.Context, relay *nostr.Relay, eventID string) bool {
	events, err := relay.QuerySync(ctx, nostr.Filter{IDs: []string{eventID}, Limit: 1})
	if err != nil {
		return false
	}
	for _, event := range events {
		if event != nil && event.ID == eventID {
			return true
		}
	}
	return false
}

func socketEventExists(ctx context.Context, path, eventID string) bool {
	events, err := querySocket(ctx, path, nostr.Filter{IDs: []string{eventID}, Limit: 1})
	if err != nil {
		return false
	}
	for _, event := range events {
		if event != nil && event.ID == eventID {
			return true
		}
	}
	return false
}

// identityProofLimit is the page size requested per relay for identity proofs.
const identityProofLimit = 500

// FetchIdentityProofsByCertificate returns current matching proofs reported by
// each configured relay. ZSP applies its certificate-global selection rule
// after strict local validation. Each relay is asked for at most
// identityProofLimit events; a relay that returns a full page adds a warning
// wrapping ErrIncompleteResults, because the proof sought may be beyond it. The
// events it did return are still included.
func (p *Publisher) FetchIdentityProofsByCertificate(ctx context.Context, certHash string) ([]*nostr.Event, []error, error) {
	return p.FetchIdentityProofs(ctx, []string{certHash})
}

// FetchIdentityProofs returns proofs whose d tag is any certificate in certHashes.
func (p *Publisher) FetchIdentityProofs(ctx context.Context, certHashes []string) ([]*nostr.Event, []error, error) {
	if len(certHashes) == 0 {
		return nil, nil, nil
	}
	filter := nostr.Filter{
		Kinds: []int{KindIdentityProof},
		Tags:  nostr.TagMap{"d": certHashes},
		// Kind 30509 is parameterized replaceable by author and d tag. Fetch
		// enough current author variants to select the latest valid proof
		// locally; limiting this query to one lets one malformed event hide a
		// valid proof from another author.
		Limit: identityProofLimit,
	}
	proofsByID := make(map[string]*nostr.Event)
	var warnings []error
	completed := 0
	for _, relayURL := range p.relayURLs {
		events, err := p.queryRelayMultiple(ctx, relayURL, filter)
		if err != nil {
			warnings = append(warnings, relayFailure(relayURL, err))
			continue
		}
		completed++
		if len(events) >= identityProofLimit {
			warnings = append(warnings, relayFailure(relayURL, ErrIncompleteResults))
		}
		for _, event := range events {
			if event != nil {
				proofsByID[event.ID] = event
			}
		}
	}
	if completed == 0 && len(p.relayURLs) > 0 {
		return nil, warnings, relayQueryError("C1", warnings)
	}
	current := make([]*nostr.Event, 0, len(proofsByID))
	for _, event := range proofsByID {
		current = append(current, event)
	}
	sort.Slice(current, func(i, j int) bool {
		if current[i].CreatedAt == current[j].CreatedAt {
			return current[i].ID < current[j].ID
		}
		return current[i].CreatedAt > current[j].CreatedAt
	})
	return current, warnings, nil
}

// FetchApplicationEvents returns deduplicated application events for appID on
// every completed relay query. It names no author: a catalog relay serves only
// the selected listing, so the caller filters by signer.
func (p *Publisher) FetchApplicationEvents(ctx context.Context, appID string) ([]*nostr.Event, []error, error) {
	byID := make(map[string]*nostr.Event)
	var warnings []error
	completed := 0
	filter := nostr.Filter{
		Kinds: []int{KindAppMetadata}, Tags: nostr.TagMap{"d": []string{appID}}, Limit: 100,
	}
	for _, relayURL := range p.relayURLs {
		events, err := p.queryRelayMultiple(ctx, relayURL, filter)
		if err != nil {
			warnings = append(warnings, relayFailure(relayURL, err))
			continue
		}
		completed++
		for _, event := range events {
			byID[event.ID] = event
		}
	}
	if completed == 0 && len(p.relayURLs) > 0 {
		return nil, warnings, relayQueryError("application", warnings)
	}
	events := make([]*nostr.Event, 0, len(byID))
	for _, event := range byID {
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt == events[j].CreatedAt {
			return events[i].ID < events[j].ID
		}
		return events[i].CreatedAt > events[j].CreatedAt
	})
	return events, warnings, nil
}

// HasAppEvents reports whether a kind 32267 application event for appID exists
// on a configured relay. A successful empty query is definitive; individual
// relay failures are returned as warnings.
func (p *Publisher) HasAppEvents(ctx context.Context, appID string) (bool, []error, error) {
	locations, warnings, err := p.FindAppEvents(ctx, appID)
	return len(locations.ApplicationRelays) > 0, warnings, err
}

// FindAppEvents returns configured relay URLs that contain a kind 32267
// application event for appID.
func (p *Publisher) FindAppEvents(ctx context.Context, appID string) (AppEventLocations, []error, error) {
	filter := nostr.Filter{
		Kinds: []int{KindAppMetadata}, Tags: nostr.TagMap{"d": []string{appID}}, Limit: 1,
	}
	var locations AppEventLocations
	var warnings []error
	completed := 0
	for _, relayURL := range p.relayURLs {
		events, err := p.queryRelayMultiple(ctx, relayURL, filter)
		if err != nil {
			warnings = append(warnings, relayFailure(relayURL, err))
			continue
		}
		completed++
		locations.CheckedRelays = append(locations.CheckedRelays, relayURL)
		for range events {
			locations.ApplicationRelays = append(locations.ApplicationRelays, relayURL)
			locations.ApplicationCount++
		}
	}
	if completed == 0 && len(p.relayURLs) > 0 {
		return AppEventLocations{}, warnings, relayQueryError("app", warnings)
	}
	locations.ApplicationRelays = uniqueRelayURLs(locations.ApplicationRelays)
	return locations, warnings, nil
}

// RelayClassifications obtains the optional NIP-11 administrative pubkey for
// each configured relay. The result is display metadata, not authorization.
func (p *Publisher) RelayClassifications(ctx context.Context) []RelayClassification {
	result := make([]RelayClassification, 0, len(p.relayURLs))
	client := &http.Client{Timeout: RelayTimeout}
	for _, relayURL := range p.relayURLs {
		classification := RelayClassification{RelayURL: relayURL}
		httpURL, err := relayInfoURL(relayURL)
		if err == nil {
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, httpURL, nil)
			if requestErr == nil {
				request.Header.Set("Accept", "application/nostr+json")
				response, responseErr := client.Do(request)
				if responseErr == nil {
					var document struct {
						Pubkey string `json:"pubkey"`
					}
					if response.StatusCode == http.StatusOK {
						_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&document)
					}
					_ = response.Body.Close()
					classification.PublisherPubkey = document.Pubkey
					classification.IsDefaultIndexer = document.Pubkey == DefaultIndexerPubkey
				}
			}
		}
		result = append(result, classification)
	}
	return result
}

func relayInfoURL(relayURL string) (string, error) {
	parsed, err := url.Parse(relayURL)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("invalid relay URL")
	}
	switch parsed.Scheme {
	case "wss":
		parsed.Scheme = "https"
	case "ws":
		parsed.Scheme = "http"
	default:
		return "", fmt.Errorf("invalid relay scheme")
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// ProfileName returns the display_name or name from a signed kind 0 profile.
func (p *Publisher) ProfileName(ctx context.Context, pubkey string) string {
	if pubkey == "" {
		return ""
	}
	for _, relayURL := range p.relayURLs {
		events, err := p.queryRelayMultiple(ctx, relayURL, nostr.Filter{Kinds: []int{0}, Authors: []string{pubkey}, Limit: 1})
		if err != nil {
			continue
		}
		for _, event := range events {
			if event == nil || event.PubKey != pubkey {
				continue
			}
			valid, signatureErr := event.CheckSignature()
			if signatureErr != nil || !valid {
				continue
			}
			var profile struct {
				DisplayName string `json:"display_name"`
				Name        string `json:"name"`
			}
			if json.Unmarshal([]byte(event.Content), &profile) == nil {
				if profile.DisplayName != "" {
					return profile.DisplayName
				}
				if profile.Name != "" {
					return profile.Name
				}
			}
		}
	}
	return ""
}

func uniqueRelayURLs(values []string) []string {
	if len(values) < 2 {
		return values
	}
	sort.Strings(values)
	return append(values[:0:0], compactStrings(values)...)
}

func compactStrings(values []string) []string {
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

// PublishedReleases is the greatest linked version_code relays serve for an
// app in one channel.
type PublishedReleases struct {
	// VersionCode and ReleasedAt come from the authorized publishers.
	VersionCode int64
	ReleasedAt  time.Time
	// OtherVersionCode comes from any other signer, such as the catalog key
	// when its listing is the one a catalog relay selects.
	OtherVersionCode int64
}

// FetchPublishedReleases reads the releases and assets for appID by app id
// alone: a catalog relay serves only the selected listing, whoever signed it.
// A release links assets from any authorized publisher, so a delegate's
// release can link an owner's asset.
func (p *Publisher) FetchPublishedReleases(ctx context.Context, publishers map[string]struct{}, appID string, certificateHashes []string, channel string) (PublishedReleases, []error, error) {
	if channel == "" {
		channel = "main"
	}
	own := make(map[string]*nostr.Event)
	other := make(map[string]*nostr.Event)
	var warnings []error
	completed := 0
	for _, relayURL := range p.relayURLs {
		relayCompleted := false
		for _, kind := range []int{KindRelease, KindSoftwareAsset} {
			tags := nostr.TagMap{"i": []string{appID}}
			if kind == KindRelease {
				tags["c"] = []string{channel}
			}
			events, err := p.queryRelayMultiple(ctx, relayURL, nostr.Filter{Kinds: []int{kind}, Tags: tags})
			if err != nil {
				warnings = append(warnings, relayFailure(relayURL, err))
				continue
			}
			relayCompleted = true
			for _, event := range events {
				if event == nil {
					continue
				}
				identifier, validIdentifier := singleTagValue(event.Tags, "i")
				valid, signatureErr := event.CheckSignature()
				if signatureErr != nil || !valid || !validIdentifier || identifier != appID {
					continue
				}
				if _, ok := publishers[event.PubKey]; ok {
					own[event.ID] = event
				} else {
					other[event.ID] = event
				}
			}
		}
		if relayCompleted {
			completed++
		}
	}
	if completed == 0 && len(p.relayURLs) > 0 {
		return PublishedReleases{}, warnings, relayQueryError("release", warnings)
	}
	var result PublishedReleases
	result.VersionCode, result.ReleasedAt = highestLinkedReleaseVersion(own, certificateHashes, channel)
	result.OtherVersionCode, _ = highestLinkedReleaseVersion(other, certificateHashes, channel)
	return result, warnings, nil
}

func highestLinkedReleaseVersion(eventsByID map[string]*nostr.Event, certificateHashes []string, channel string) (int64, time.Time) {
	allowed := make(map[string]struct{}, len(certificateHashes))
	for _, hash := range certificateHashes {
		allowed[strings.ToLower(hash)] = struct{}{}
	}
	if channel == "" {
		channel = "main"
	}
	var maximum int64
	var latestTimestamp time.Time
	for _, release := range eventsByID {
		releaseChannel, validChannel := singleTagValue(release.Tags, "c")
		if release.Kind != KindRelease || !validChannel || releaseChannel != channel {
			continue
		}
		for _, tag := range release.Tags {
			if len(tag) < 2 || tag[0] != "e" {
				continue
			}
			asset := eventsByID[tag[1]]
			if asset == nil || asset.Kind != KindSoftwareAsset {
				continue
			}
			assetCertificate, validCertificate := singleTagValue(asset.Tags, "apk_certificate_hash")
			if !validCertificate {
				continue
			}
			if _, ok := allowed[strings.ToLower(assetCertificate)]; !ok {
				continue
			}
			versionValue, validVersion := singleTagValue(asset.Tags, "version_code")
			if !validVersion {
				continue
			}
			versionCode, err := strconv.ParseInt(versionValue, 10, 64)
			releasedAt := time.Unix(int64(release.CreatedAt), 0)
			if err == nil && strconv.FormatInt(versionCode, 10) == versionValue && (versionCode > maximum ||
				(versionCode == maximum && releasedAt.After(latestTimestamp))) {
				maximum = versionCode
				latestTimestamp = releasedAt
			}
		}
	}
	return maximum, latestTimestamp
}

func singleTagValue(tags nostr.Tags, name string) (string, bool) {
	var value string
	found := false
	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != name {
			continue
		}
		if found || len(tag) != 2 || tag[1] == "" {
			return "", false
		}
		found = true
		value = tag[1]
	}
	return value, found
}

func relayQueryError(operation string, warnings []error) error {
	if err := errors.Join(warnings...); err != nil {
		return fmt.Errorf("no relay completed %s query: %w", operation, err)
	}
	return fmt.Errorf("no relay completed %s query", operation)
}

// queryRelayMultiple queries a single relay and returns all matching events.
func (p *Publisher) queryRelayMultiple(ctx context.Context, relayURL string, filter nostr.Filter) ([]*nostr.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, RelayTimeout)
	defer cancel()

	if path, ok := socketPath(relayURL); ok {
		return querySocket(ctx, path, filter)
	}

	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		return nil, newOpError("connect", err)
	}
	defer relay.Close()

	events, err := relay.QuerySync(ctx, filter)
	if err != nil {
		return nil, newOpError("query", err)
	}
	return events, nil
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
