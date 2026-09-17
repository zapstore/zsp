package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/identity"
	nostrpkg "github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/ui"
	"gopkg.in/yaml.v3"
)

const c1RenewalWindow = 90 * 24 * time.Hour

// runWizard is the interactive entry point for `zsp`.
func runWizard(ctx context.Context) int {
	if !isTTY() {
		ui.WritePanel(os.Stderr, "error", "Interactive terminal required", nil, []string{"Run a command such as zsp publish <input> instead"})
		return 1
	}
	fmt.Println(ui.InfoStyle.Italic(true).Render("This wizard will help you configure zapstore.yaml for publishing your app to catalog relays and prove ownership."))
	relayURLs := relaysFromEnv()
	publisher := nostrpkg.NewPublisher(relayURLs)
	unreachableRelays, err := publisher.EnsureReachable(ctx)
	if err != nil {
		ui.WritePanel(os.Stderr, "error", "Couldn't reach any configured relay", wizardRelayTargets(relayURLs), []string{"No changes were made"})
		return 1
	}
	if len(unreachableRelays) > 0 {
		ui.WritePanel(os.Stderr, "warning", "Some configured relays couldn't be reached", wizardRelayTargets(unreachableRelays), []string{"Continuing with reachable relays"})
	}
	root, err := wizardProjectRoot()
	if err != nil {
		return wizardError("Configuration setup stopped", err)
	}
	path := filepath.Join(root, "zapstore.yaml")
	existing, err := loadWizardConfig(path)
	if err != nil {
		return wizardError("Configuration setup stopped", err)
	}
	steps := ui.NewStepTracker(4)
	steps.StartStep("📱 Add your app")
	source, err := ui.PromptFieldDefault("Where is the source code published?", "For example: github.com/zapstore/zapstore. If app is closed-source, leave it blank.", existing.Repository, false, false)
	if err != nil {
		return wizardError("App discovery stopped", err)
	}
	publish, err := wizardConfigFromSourceCode(source)
	if err != nil {
		return wizardError("App discovery stopped", err)
	}
	if source == "" {
		publish.config = existing
	} else {
		publish.config.ReleaseSource = existing.ReleaseSource
		publish.config.ReleaseFilter = existing.ReleaseFilter
		publish.config.Match = existing.Match
		publish.config.PrereleaseChannel = existing.PrereleaseChannel
	}

	var candidates []*zsp.APK
	if isRepositorySuggestion(publish.config.Repository) {
		spinner := ui.NewSpinner("Finding and inspecting APK releases...")
		spinner.Start()
		candidates, err = zsp.Fetch(ctx, wizardFetchConfig(publish.config.FetchConfig), zsp.FetchOptions{})
		spinner.Stop()
		if err != nil {
			if err == ui.ErrInterrupted || errors.Is(err, context.Canceled) || errors.Is(err, huh.ErrUserAborted) {
				return wizardError("No verified APK release found", err)
			}
			ui.PrintInfo("No verified APK release was found at " + source)
		}
	}

	if candidates == nil {
		releaseSource, err := ui.PromptPathDefault("Where are releases of this app published?", "For example: github.com/zapstore/zapstore. For a local build, choose the directory containing APKs.", wizardReleaseSourceDefault(publish.config), true)
		if err != nil {
			return wizardError("App discovery stopped", err)
		}
		publish, err = wizardConfigFromReleaseSource(source, releaseSource)
		if err != nil {
			return wizardError("App discovery stopped", err)
		}
		spinner := ui.NewSpinner("Finding and inspecting APK releases...")
		spinner.Start()
		candidates, err = zsp.Fetch(ctx, wizardFetchConfig(publish.config.FetchConfig), zsp.FetchOptions{})
		spinner.Stop()
		if err != nil {
			return wizardError("No verified APK release found", err)
		}
	}

	defer closeCandidates(candidates)
	selected, err := selectAPKForAppSetup(candidates)
	if err != nil {
		return wizardError("APK selection stopped", err)
	}
	appName := selected.Name
	if appName == "" {
		appName = selected.AppID
	}
	version := selected.VersionName
	if version == "" {
		version = fmt.Sprintf("version code %d", selected.VersionCode)
	} else {
		version = "v" + version
	}
	ui.PrintSuccess(fmt.Sprintf("Found %s (%s) at %s", appName, version, wizardReleaseLocation(publish.config)))
	alreadyPublished := false
	spinner := ui.NewSpinner("Checking whether this app ID is already listed...")
	spinner.Start()
	locations, _, lookupErr := publisher.FindAppEvents(ctx, selected.AppID)
	spinner.Stop()
	if lookupErr == nil {
		alreadyPublished = len(locations.ApplicationRelays) > 0
		wizardRelayStatus(appName, locations, publisher.RelayClassifications(ctx))
	} else {
		return wizardError("Relay reachability check stopped", lookupErr)
	}

	suggestedTo := wizardDiscover(ctx, publish.config, selected, alreadyPublished)

	steps.StartStep("🔑 Claim your app")
	spinner = ui.NewSpinner("Checking app ownership proof...")
	spinner.Start()
	proofs, _, err := publisher.FetchIdentityProofsByCertificate(ctx, selected.CertificateHash)
	spinner.Stop()
	if err != nil {
		return wizardError("Identity setup stopped", fmt.Errorf("look up C1 proof: %w", err))
	}
	state := wizardExistingIdentity(proofs, selected.CertificateHash)
	if state == nil {
		owns, err := ui.Confirm(fmt.Sprintf("Are you the author of %s?", appName), false)
		if err != nil {
			return wizardError("Ownership confirmation stopped", err)
		}
		if !owns {
			if lookupErr == nil && !alreadyPublished && suggestedTo != "" {
				ui.PrintInfo(appName + " was suggested for listing on " + suggestedTo + ". Ask its author to claim it.")
			} else {
				ui.PrintInfo("Nothing to do")
			}
			return 0
		}
		state, err = wizardIdentity(ctx, selected.CertificateHash, appName, proofs)
		if err != nil {
			return wizardError("Identity setup stopped", err)
		}
	}
	defer state.close()
	if state.certificateHash != selected.CertificateHash {
		return wizardError("Identity setup stopped", errCertificateHashMismatch(selected.CertificateHash, state.certificateHash))
	}
	ui.WritePanel(os.Stdout, "success", state.summary, state.details, nil)

	steps.StartStep("🏷️  Configure app metadata")
	return wizardControlMetadata(ctx, publish, selected, state, root, path, steps)
}

func loadWizardConfig(path string) (zsp.Config, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return zsp.Config{}, nil
	} else if err != nil {
		return zsp.Config{}, err
	}
	config, err := zsp.LoadConfig(path)
	if err != nil {
		return zsp.Config{}, err
	}
	return config, nil
}

func wizardReleaseSourceDefault(config zsp.Config) string {
	if config.ReleaseSource == nil {
		return ""
	}
	if config.ReleaseSource.LocalPath != "" {
		return config.ReleaseSource.LocalPath
	}
	return config.ReleaseSource.URL
}

func wizardRelayTargets(relays []string) []ui.KeyValue {
	targets := make([]ui.KeyValue, 0, len(relays))
	for _, relay := range relays {
		parsed, err := url.Parse(relay)
		if err != nil {
			targets = append(targets, ui.KeyValue{Key: "Relay", Value: "invalid relay URL"})
			continue
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		targets = append(targets, ui.KeyValue{Key: "Relay", Value: parsed.String()})
	}
	return targets
}

func wizardError(title string, err error) int {
	if err == ui.ErrInterrupted || errors.Is(err, context.Canceled) {
		ui.PrintInfo(wizardCancellationSummary(title))
		return 130
	}
	if errors.Is(err, huh.ErrUserAborted) {
		ui.PrintInfo("No changes were made")
		return 0
	}
	kind, summary, details := wizardFailurePresentation(title, err)
	if kind == "error" {
		ui.WritePanel(os.Stderr, kind, summary, details, nil)
		return 1
	}
	ui.PrintInfo(summary)
	return 1
}

func wizardFailurePresentation(title string, err error) (kind, summary string, details []ui.KeyValue) {
	if details = wizardFailureDetails(err); len(details) > 0 {
		return "error", "Selected certificate does not match this APK", details
	}
	return "info", wizardFailureSummary(title), nil
}

type certificateHashMismatchError struct {
	apkHash      string
	selectedHash string
}

func (e certificateHashMismatchError) Error() string {
	return "this signing material does not match the APK certificate"
}

func errCertificateHashMismatch(apkHash, selectedHash string) error {
	return certificateHashMismatchError{apkHash: apkHash, selectedHash: selectedHash}
}

func wizardFailureDetails(err error) []ui.KeyValue {
	var mismatch certificateHashMismatchError
	if !errors.As(err, &mismatch) {
		return nil
	}
	return []ui.KeyValue{
		{Key: "APK certificate hash", Value: mismatch.apkHash},
		{Key: "Selected certificate hash", Value: mismatch.selectedHash},
	}
}

func wizardCancellationSummary(title string) string {
	switch title {
	case "App discovery stopped", "No verified APK release found", "APK selection stopped":
		return "App discovery was cancelled. No changes were made"
	case "Configuration update stopped":
		return "Configuration update was cancelled. The previous zapstore.yaml was left unchanged"
	case "Publishing choice stopped", "Signing setup stopped", "Publication stopped", "CI/CD setup stopped":
		return "Publishing setup was cancelled. Your saved configuration was left unchanged"
	default:
		return "This step was cancelled. No changes were made"
	}
}

func wizardFailureSummary(title string) string {
	switch title {
	case "App discovery stopped":
		return "Couldn't find an app from that source. Try another location"
	case "No verified APK release found":
		return "No verified APK release was found. Try another release or source"
	case "APK selection stopped":
		return "No APK was selected. No changes were made"
	case "Relay reachability check stopped":
		return "Couldn't reach any configured relay. No changes were made"
	case "Ownership confirmation stopped", "Identity setup stopped":
		return "Ownership was not changed"
	case "Configuration setup stopped":
		return "Couldn't use zapstore.yaml. Fix its configuration and try again; it was left unchanged"
	case "Configuration update stopped":
		return "Couldn't save zapstore.yaml. The previous file was left unchanged"
	case "Publishing choice stopped":
		return "No publishing method was selected. Your configuration was saved"
	case "Signing setup stopped":
		return "Signing was not set up. Your configuration was saved"
	case "Publication stopped":
		return "This release was not published. Your configuration was saved"
	case "CI/CD setup stopped":
		return "CI/CD publishing was not enabled. Your configuration was saved"
	default:
		return "That step could not be completed. You can try again"
	}
}

func wizardRelayStatus(appName string, locations nostrpkg.AppEventLocations, classifications []nostrpkg.RelayClassification) {
	if len(locations.ApplicationRelays) > 0 {
		ui.PrintInfo(fmt.Sprintf("%s found on %s", appName, strings.Join(locations.ApplicationRelays, ", ")))
	}
	if len(locations.ApplicationRelays) == 0 {
		ui.PrintInfo(appName + " is not yet listed on " + strings.Join(locations.CheckedRelays, ", "))
	}
	for _, classification := range classifications {
		if classification.IsDefaultIndexer {
			ui.PrintInfo(classification.RelayURL + " is the default Zapstore indexer.")
		}
	}
}

func wizardDiscover(ctx context.Context, cfg zsp.Config, candidate *zsp.APK, appIDExists bool) string {
	source := wizardSuggestionSource(cfg)
	if source == "" || candidate == nil || appIDExists {
		return ""
	}
	relayHTTPURL := config.GetRelayHTTPURL()
	if err := submitPublicSuggestion(ctx, source, candidate.CertificateHash, relayHTTPURL); err != nil {
		return ""
	}
	return relayHTTPURL
}

func submitPublicSuggestion(ctx context.Context, source, certificateHash, indexerURL string) error {
	parsed, err := url.Parse(indexerURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname()))) {
		return fmt.Errorf("indexer must use HTTPS outside loopback")
	}
	body := struct {
		Repository      string `json:"repository,omitempty"`
		ReleaseSource   string `json:"release_source,omitempty"`
		CertificateHash string `json:"certificate_hash"`
	}{CertificateHash: certificateHash}
	if isRepositorySuggestion(source) {
		body.Repository = source
	} else {
		body.ReleaseSource = source
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(indexerURL, "/")+"/suggest", strings.NewReader(string(encoded)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return fmt.Errorf("indexer suggestion was not accepted")
	}
	return nil
}

func wizardControlMetadata(ctx context.Context, publish wizardPublishConfig, selected *zsp.APK, state *wizardIdentityState, root, path string, steps *ui.StepTracker) int {
	config, document, err := loadWizardYAML(path, selected)
	if err != nil {
		return wizardError("Configuration setup stopped", err)
	}
	if err := promptWizardMetadata(&config, publish.config, root); err != nil {
		return wizardError("Configuration setup stopped", err)
	}
	if err := saveWizardYAML(path, document, config, publish.config, root); err != nil {
		return wizardError("Configuration update stopped", err)
	}
	// Reload the saved canonical configuration. This retains unedited fields
	// and resolves new relative media paths exactly as later CLI publication
	// will, so preview and signed events match zapstore.yaml.
	canonical, err := zsp.LoadConfig(path)
	if err != nil {
		return wizardError("Configuration update stopped", err)
	}
	publish.config = canonical
	ui.PrintInfo("Updated zapstore.yaml")
	fmt.Println()
	steps.StartStep("🚀 Publish")
	return wizardPublishingChoice(ctx, publish, selected, state)
}

type wizardIdentityState struct {
	certificateHash string
	owner           string
	delegate        string
	authorized      map[string]struct{}
	signer          nostrpkg.Signer
	summary         string
	details         []ui.KeyValue
}

func (s *wizardIdentityState) close() {
	if s.signer != nil {
		_ = s.signer.Close()
	}
}

func wizardIdentity(ctx context.Context, expectedCertificateHash, appName string, proofs []*gonostr.Event) (*wizardIdentityState, error) {

	keystore, err := ui.PromptPath("Keystore or certificate", "Select the file used to sign "+appName+".", false)
	if err != nil {
		return nil, err
	}
	var privateKey crypto.PrivateKey
	certificate, err := loadCertificateMaterial(keystore)
	if err != nil {
		// Encrypted keystores need their private-key password to expose their
		// certificate. PEM certificates take the no-secret lookup path above.
		privateKey, certificate, err = loadIdentityMaterial(proofOptions{Keystore: keystore, Expiry: "2y"})
		if err != nil {
			return nil, err
		}
	}
	certificateHash := identity.ComputeCertHash(certificate)
	if certificateHash != expectedCertificateHash {
		return nil, errCertificateHashMismatch(expectedCertificateHash, certificateHash)
	}
	if owner, expiry := chooseCurrentProof(proofs, certificate, ""); owner != "" && time.Until(expiry) >= c1RenewalWindow {
		ok, err := confirmCurrentProof(certificateHash, owner, expiry)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("choose the certificate you want linked to this Nostr profile, then restart the wizard")
		}
		authorized := map[string]struct{}{owner: {}}
		delegate := currentProofDelegate(proofs, certificate, owner)
		if delegate != "" {
			authorized[delegate] = struct{}{}
		}
		return &wizardIdentityState{
			certificateHash: certificateHash,
			owner:           owner,
			delegate:        delegate,
			authorized:      authorized,
			summary:         "Ownership proof confirmed",
		}, nil
	}
	signer, err := wizardSigner(ctx)
	if err != nil {
		return nil, err
	}
	delegate := currentProofDelegate(proofs, certificate, chooseProofOwner(proofs, certificate))
	signingCertificate := certificate
	if privateKey == nil {
		privateKey, signingCertificate, err = loadIdentityMaterial(proofOptions{Keystore: keystore, Expiry: "2y"})
		if err != nil {
			signer.Close()
			return nil, err
		}
	}
	if err := identity.ValidateKeyCertPair(privateKey, signingCertificate); err != nil {
		signer.Close()
		return nil, err
	}
	if signingHash := identity.ComputeCertHash(signingCertificate); signingHash != certificateHash {
		signer.Close()
		return nil, errCertificateHashMismatch(certificateHash, signingHash)
	}
	published, err := publishC1Proof(ctx, privateKey, signingCertificate, signer, identity.DefaultExpiry, delegate)
	if err != nil {
		signer.Close()
		return nil, err
	}
	return &wizardIdentityState{
		certificateHash: published.Proof.CertHash,
		owner:           signer.PublicKey(),
		delegate:        delegate,
		authorized:      authorizedPublisherSet(signer.PublicKey(), delegate),
		signer:          signer,
		summary:         "Certificate ownership proof published",
	}, nil
}

func wizardExistingIdentity(proofs []*gonostr.Event, certificateHash string) *wizardIdentityState {
	event, proof := activeProofForCertificateHash(proofs, certificateHash)
	if event == nil || time.Until(proof.ExpiryTime()) < c1RenewalWindow {
		return nil
	}
	delegate := proofDelegate(event)
	return &wizardIdentityState{
		certificateHash: certificateHash,
		owner:           event.PubKey,
		delegate:        delegate,
		authorized:      authorizedPublisherSet(event.PubKey, delegate),
		summary:         "Existing ownership proof found",
		details:         existingOwnershipProofDetails(certificateHash, proof.ExpiryTime()),
	}
}

func chooseProofOwner(events []*gonostr.Event, certificate *x509.Certificate) string {
	owner, _ := chooseCurrentProof(events, certificate, "")
	return owner
}

func authorizedPublisherSet(owner, delegate string) map[string]struct{} {
	authorized := map[string]struct{}{owner: {}}
	if delegate != "" {
		authorized[delegate] = struct{}{}
	}
	return authorized
}

func confirmCurrentProof(certificateHash, owner string, expiry time.Time) (bool, error) {
	ui.WritePanel(os.Stderr, "info", "Existing certificate ownership proof found", currentProofDetails(certificateHash, owner, expiry), []string{
		"This proof confirms that this Nostr profile owns the certificate used to sign your APKs.",
		"Using it lets you publish releases without creating a new proof.",
	})
	return ui.Confirm("Is this the certificate you want linked to this Nostr profile?", true)
}

func currentProofDetails(certificateHash, owner string, expiry time.Time) []ui.KeyValue {
	details := []ui.KeyValue{
		{Key: "Nostr npub", Value: npubFromHex(owner)},
		{Key: "Signing certificate hash", Value: certificateHash},
	}
	if !expiry.IsZero() {
		details = append(details, ui.KeyValue{Key: "Expires", Value: expiry.Local().Format("2006-01-02 15:04")})
	}
	return details
}

func existingOwnershipProofDetails(certificateHash string, expiry time.Time) []ui.KeyValue {
	details := make([]ui.KeyValue, 0, 2)
	if certificateHash != "" {
		details = append(details, ui.KeyValue{Key: "Hash", Value: abbreviateHash(certificateHash)})
	}
	if formatted := formatProofExpiry(expiry); formatted != "" {
		details = append(details, ui.KeyValue{Key: "Expires", Value: formatted})
	}
	return details
}

func abbreviateHash(hash string) string {
	const side = 6
	if len(hash) <= side*2 {
		return hash
	}
	return hash[:side] + "…" + hash[len(hash)-side:]
}

func formatProofExpiry(expiry time.Time) string {
	if expiry.IsZero() {
		return ""
	}
	return expiry.Local().Format("2 Jan 2006")
}

func npubFromHex(owner string) string {
	npub, err := nip19.EncodePublicKey(owner)
	if err != nil {
		return owner
	}
	return npub
}

func chooseCurrentProof(events []*gonostr.Event, certificate *x509.Certificate, owner string) (string, time.Time) {
	for _, event := range events {
		if owner != "" && event.PubKey != owner {
			continue
		}
		if certificate != nil {
			proof, err := identity.ValidateActiveProofEvent(event, certificate, time.Now())
			if err != nil {
				continue
			}
			return event.PubKey, proof.ExpiryTime()
		}
		proof, err := identity.ParseIdentityProofFromEvent(event)
		if err != nil || proof.IsExpired() {
			continue
		}
		return event.PubKey, proof.ExpiryTime()
	}
	return "", time.Time{}
}

// activeProofForCertificateHash finds a current, signed C1 proof whose d tag
// matches the APK certificate hash. The certificate signature is checked when
// a certificate is supplied to renew or replace the proof.
func activeProofForCertificateHash(events []*gonostr.Event, certificateHash string) (*gonostr.Event, *identity.IdentityProof) {
	now := time.Now()
	for _, event := range events {
		if event == nil || event.Content != "" {
			continue
		}
		signed, err := event.CheckSignature()
		if err != nil || !signed {
			continue
		}
		proof, err := identity.ParseIdentityProofFromEvent(event)
		if err != nil || proof.CertHash != certificateHash || proof.Expiry <= int64(event.CreatedAt) || !proof.ExpiryTime().After(now) {
			continue
		}
		if revoked, _ := identity.IsRevoked(event); revoked {
			continue
		}
		return event, proof
	}
	return nil, nil
}

func proofDelegate(event *gonostr.Event) string {
	if event == nil {
		return ""
	}
	delegation := event.Tags.GetFirst([]string{"delegation"})
	if delegation == nil || len(*delegation) != 2 {
		return ""
	}
	return (*delegation)[1]
}

func currentProofDelegate(events []*gonostr.Event, certificate *x509.Certificate, owner string) string {
	for _, event := range events {
		if event.PubKey != owner {
			continue
		}
		if _, err := identity.ValidateActiveProofEvent(event, certificate, time.Now()); err != nil {
			continue
		}
		if delegation := event.Tags.GetFirst([]string{"delegation"}); delegation != nil && len(*delegation) == 2 {
			return (*delegation)[1]
		}
		return ""
	}
	return ""
}

func wizardSigner(ctx context.Context) (nostrpkg.Signer, error) {
	signWith := config.GetSignWith()
	if signWith == "" {
		kind, err := ui.SelectField("Signing method", "Needed only to create or renew C1. A remote signer keeps the private key outside ZSP.", []string{"bunker:// URL", "Advanced: one-time nsec"})
		if err != nil {
			return nil, err
		}
		switch kind {
		case "bunker:// URL":
			signWith, err = ui.PromptField("Bunker URL", "Remote signing connection.", false, true)
		default:
			signWith, err = ui.PromptSecret("Nostr nsec")
		}
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(signWith, "nsec1") {
			ui.PrintInfo("Set SIGN_WITH in .env yourself to skip this prompt next time.")
		} else if strings.HasPrefix(signWith, "bunker://") {
			save, err := ui.Confirm("Save bunker URL to .env?", true)
			if err != nil {
				return nil, err
			}
			if save {
				if err := ensureEnvIgnored(); err != nil {
					return nil, err
				}
				if err := saveSignWith(signWith); err != nil {
					return nil, err
				}
			}
		}
		_ = os.Setenv("SIGN_WITH", signWith)
	}
	signer, err := nostrpkg.NewSigner(ctx, signWith)
	if err != nil {
		return nil, err
	}
	return signer, nil
}

type wizardPublishConfig struct {
	config zsp.Config
}

func wizardFetchConfig(config zsp.FetchConfig) zsp.FetchConfig {
	config.SkipHTTPCache = true
	return config
}

func wizardConfigFromSourceCode(source string) (wizardPublishConfig, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return wizardPublishConfig{}, nil
	}
	source, err := validateSuggestionSource(source)
	if err != nil {
		return wizardPublishConfig{}, err
	}
	if err := config.ValidateForgeRepositoryURL(source, ""); err != nil {
		return wizardPublishConfig{}, err
	}
	source, _ = config.CanonicalForgeRepositoryURL(source, "")
	return wizardPublishConfig{config: zsp.Config{
		FetchConfig: zsp.FetchConfig{Repository: source},
	}}, nil
}

func wizardConfigFromReleaseSource(sourceCode, source string) (wizardPublishConfig, error) {
	publish, err := wizardConfigFromSourceCode(sourceCode)
	if err != nil {
		return wizardPublishConfig{}, err
	}
	source = strings.TrimSpace(source)
	if !strings.Contains(source, "://") {
		absolutePath, err := filepath.Abs(source)
		if err != nil {
			return wizardPublishConfig{}, err
		}
		if info, err := os.Stat(absolutePath); err == nil && info.IsDir() {
			return wizardPublishConfig{
				config: zsp.Config{
					FetchConfig: zsp.FetchConfig{
						Repository:    publish.config.Repository,
						ReleaseSource: &zsp.ReleaseSource{LocalPath: absolutePath},
					},
				},
			}, nil
		}
	}
	source, err = validateSuggestionSource(source)
	if err != nil {
		return wizardPublishConfig{}, err
	}
	if repository, ok := config.CanonicalForgeRepositoryURL(source, ""); ok {
		if publish.config.Repository == "" {
			publish.config.Repository = repository
			return publish, nil
		}
		source = repository
	}
	publish.config.ReleaseSource = &zsp.ReleaseSource{URL: source}
	return publish, nil
}

type wizardMetadata struct {
	name        string
	summary     string
	description string
	tags        []string
	license     string
	website     string
	icon        string
	images      []string
	sources     []string
	guidance    bool
}

func wizardProjectRoot() (string, error) {
	currentDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if isGitRepositoryRoot(currentDirectory) {
		return filepath.Abs(currentDirectory)
	}
	value, err := ui.PromptPathDefault("Project directory", "ZSP writes zapstore.yaml at the repository root so Zapstore can find your app metadata.", currentDirectory, true)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", root)
	}
	if !isGitRepositoryRoot(root) {
		return "", fmt.Errorf("%s is not a Git repository root", root)
	}
	return root, nil
}

func isGitRepositoryRoot(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func loadWizardYAML(path string, apk *zsp.APK) (wizardMetadata, *yaml.Node, error) {
	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	metadata := wizardMetadata{name: apk.Name}
	if metadata.name == "" {
		metadata.name = apk.AppID
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return metadata, document, nil
	}
	if err != nil {
		return metadata, nil, err
	}
	if err := yaml.Unmarshal(data, document); err != nil {
		return metadata, nil, fmt.Errorf("parse existing zapstore.yaml: %w", err)
	}
	existing, err := zsp.LoadConfig(path)
	if err != nil {
		return metadata, nil, err
	}
	metadata = wizardMetadata{
		name: existing.Name, summary: existing.Summary, description: existing.Description,
		tags: existing.Tags, license: existing.License, website: existing.Website,
		icon: existing.Icon, images: existing.Images, sources: existing.MetadataSources,
	}
	if metadata.name == "" {
		metadata.name = apk.Name
	}
	if metadata.name == "" {
		metadata.name = apk.AppID
	}
	return metadata, document, nil
}

func wizardPublishingChoice(ctx context.Context, publish wizardPublishConfig, selected *zsp.APK, state *wizardIdentityState) int {
	choice, err := ui.SelectField("Choose how to publish", "Let Zapstore discover the committed configuration for convenience, or publish yourself for more control.", []string{
		"Let Zapstore pick up the committed configuration (convenient)",
		"Publish manually or through CI/CD (more control)",
	})
	if err != nil {
		return wizardError("Publishing choice stopped", err)
	}
	if choice == "Let Zapstore pick up the committed configuration (convenient)" {
		ui.PrintInfo("Commit and push zapstore.yaml so catalog indexers can pick it up.")
		return 0
	}
	return wizardDirectPublishing(ctx, publish, selected, state)
}

func wizardDirectPublishing(ctx context.Context, publish wizardPublishConfig, selected *zsp.APK, state *wizardIdentityState) int {
	choice, err := ui.SelectField("Manual or automated publishing?", "Manual publishing gives you a review now. CI/CD can publish future releases automatically.", []string{
		"Publish this verified release now",
		"Set up CI/CD publishing",
	})
	if err != nil {
		return wizardError("Publishing choice stopped", err)
	}
	if choice == "Set up CI/CD publishing" {
		return wizardConfigureDelegate(ctx, state)
	}
	if state.signer == nil {
		signer, signerErr := wizardSigner(ctx)
		if signerErr != nil {
			return wizardError("Signing setup stopped", signerErr)
		}
		state.signer = signer
	}
	if _, allowed := state.authorized[state.signer.PublicKey()]; !allowed {
		return wizardError("Signing setup stopped", fmt.Errorf("SIGN_WITH is not the active C1 owner or delegate"))
	}
	result, err := zsp.Publish(ctx, publish.config.PublishConfig, selected, zsp.PublishOptions{Preview: true})
	if err != nil {
		return wizardError("Publication stopped", err)
	}
	ui.WritePanel(os.Stdout, "success", "Release published", []ui.KeyValue{{Key: "Release", Value: result.ID}}, nil)
	return 0
}

func wizardConfigureDelegate(ctx context.Context, state *wizardIdentityState) int {
	delegate, err := ui.PromptField("CI/CD public key", "The npub or lowercase hex public key your automation will use to publish.", false, true)
	if err != nil {
		return wizardError("CI/CD setup stopped", err)
	}
	delegate, err = parseDelegate(delegate)
	if err != nil {
		return wizardError("CI/CD setup stopped", err)
	}
	material, err := ui.PromptPath("Keystore or certificate", "Choose the signing material for the verified APK certificate.", true)
	if err != nil {
		return wizardError("CI/CD setup stopped", err)
	}
	privateKey, certificate, err := loadIdentityMaterial(proofOptions{Keystore: material, Expiry: "2y"})
	if err != nil {
		return wizardError("CI/CD setup stopped", err)
	}
	if selectedHash := identity.ComputeCertHash(certificate); selectedHash != state.certificateHash {
		return wizardError("CI/CD setup stopped", errCertificateHashMismatch(state.certificateHash, selectedHash))
	}
	if state.signer == nil {
		signer, signerErr := wizardSigner(ctx)
		if signerErr != nil {
			return wizardError("CI/CD setup stopped", signerErr)
		}
		state.signer = signer
	}
	if state.signer.PublicKey() != state.owner {
		return wizardError("CI/CD setup stopped", fmt.Errorf("SIGN_WITH is not the active C1 owner"))
	}
	if _, err := publishC1Proof(ctx, privateKey, certificate, state.signer, identity.DefaultExpiry, delegate); err != nil {
		return wizardError("CI/CD setup stopped", err)
	}
	ui.WritePanel(os.Stdout, "success", "CI/CD publishing enabled", nil, []string{
		"The delegated key can now publish releases signed by this certificate.",
	})
	return 0
}

func promptWizardMetadata(metadata *wizardMetadata, source zsp.Config, root string) error {
	var err error
	metadata.sources, err = promptWizardMetadataSources(metadata.sources, source, root)
	if err != nil {
		return err
	}
	if len(metadata.sources) > 0 {
		return nil
	}
	metadata.guidance, err = ui.Confirm("Add commented guidance for optional metadata fields?", true)
	if err != nil {
		return err
	}
	return nil
}

func promptWizardMetadataSources(existing []string, source zsp.Config, root string) ([]string, error) {
	values, labels := wizardMetadataSourceChoices(source, root)
	selected := make([]int, 0, len(existing))
	for index, value := range values {
		for _, configured := range existing {
			if value == configured {
				selected = append(selected, index)
				break
			}
		}
	}
	indexes, err := ui.SelectMultipleWithDefaultsDescription(
		"Metadata sources",
		"Select every source that applies. Press Space to toggle options, then Enter to continue.",
		labels,
		selected,
	)
	if err != nil {
		return nil, err
	}
	sources := make([]string, 0, len(indexes))
	for _, index := range indexes {
		sources = append(sources, values[index])
	}
	return sources, nil
}

func wizardMetadataSourceChoices(source zsp.Config, root string) ([]string, []string) {
	if source.ReleaseSource != nil && source.ReleaseSource.LocalPath != "" {
		return []string{"fdroid", "playstore"}, []string{"F-Droid", "Google Play Store"}
	}

	values := make([]string, 0, 5)
	labels := make([]string, 0, 5)
	add := func(value, label string) {
		for _, configured := range values {
			if configured == value {
				return
			}
		}
		values = append(values, value)
		labels = append(labels, label)
	}
	if hasLocalFastlaneMetadata(root) {
		add("fastlane", "Fastlane (local metadata)")
	}
	for _, sourceType := range wizardMetadataForgeTypes(source) {
		switch sourceType {
		case config.SourceGitHub:
			add("github", "GitHub")
		case config.SourceGitLab:
			add("gitlab", "GitLab")
		case config.SourceGitea:
			add("gitea", "Gitea")
		}
	}
	add("fdroid", "F-Droid")
	add("playstore", "Google Play Store")
	return values, labels
}

func wizardMetadataForgeTypes(source zsp.Config) []config.SourceType {
	types := []config.SourceType{config.DetectSourceType(source.Repository)}
	if source.ReleaseSource != nil {
		releaseType := config.DetectSourceType(source.ReleaseSource.URL)
		if source.ReleaseSource.Type != "" {
			releaseType = config.ParseSourceType(source.ReleaseSource.Type)
		}
		types = append(types, releaseType)
	}
	return types
}

func hasLocalFastlaneMetadata(root string) bool {
	info, err := os.Stat(filepath.Join(root, "fastlane", "metadata", "android"))
	return err == nil && info.IsDir()
}

func splitWizardList(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}

func applyWizardMetadata(config *zsp.Config, metadata wizardMetadata) {
	config.Name = metadata.name
	config.Summary = metadata.summary
	config.Description = metadata.description
	config.Tags = append([]string(nil), metadata.tags...)
	config.License = metadata.license
	config.Website = metadata.website
	config.Icon = metadata.icon
	config.Images = append([]string(nil), metadata.images...)
}

func saveWizardYAML(path string, document *yaml.Node, metadata wizardMetadata, source zsp.Config, root string) error {
	if document == nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("existing zapstore.yaml must contain a YAML mapping")
	}
	values := map[string][]string{
		"summary":     {metadata.summary},
		"description": {metadata.description},
		"tags":        metadata.tags,
		"license":     {metadata.license},
		"website":     {metadata.website},
		"images":      metadata.images,
	}
	for key, value := range values {
		setWizardYAMLValue(document.Content[0], key, value)
	}
	// Name and icon come from the APK or metadata sources, never zapstore.yaml.
	setWizardYAMLValue(document.Content[0], "name", nil)
	setWizardYAMLValue(document.Content[0], "icon", nil)
	setWizardYAMLSequence(document.Content[0], "metadata_sources", metadata.sources)
	if source.Repository != "" {
		setWizardYAMLValue(document.Content[0], "repository", []string{source.Repository})
	}
	if source.ReleaseSource != nil {
		value := source.ReleaseSource.URL
		if source.ReleaseSource.LocalPath != "" {
			relative, err := filepath.Rel(root, source.ReleaseSource.LocalPath)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(relative, ".") {
				relative = "./" + relative
			}
			value = relative
		}
		setWizardYAMLValue(document.Content[0], "release_source", []string{value})
	}
	encoded, err := yaml.Marshal(document)
	if err != nil {
		return err
	}
	if metadata.guidance {
		encoded = appendWizardMetadataGuidance(encoded)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".zapstore.yaml-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func appendWizardMetadataGuidance(data []byte) []byte {
	const guidance = `# zsp metadata guidance
# Add any of these optional fields when they are available:
# summary: A short description for app listings
# description: A longer app description
# tags: [privacy, music]
# license: GPL-3.0-or-later
# website: https://example.com
# images: [https://example.com/screenshot.png]
`
	if strings.Contains(string(data), "# zsp metadata guidance") {
		return data
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return append(data, guidance...)
}

func setWizardYAMLValue(mapping *yaml.Node, key string, values []string) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != key {
			continue
		}
		if len(values) == 0 || values[0] == "" {
			mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
			return
		}
		mapping.Content[index+1] = wizardYAMLValue(values)
		return
	}
	if len(values) == 0 || values[0] == "" {
		return
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		wizardYAMLValue(values),
	)
}

func setWizardYAMLSequence(mapping *yaml.Node, key string, values []string) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != key {
			continue
		}
		mapping.Content[index+1] = wizardYAMLValue(values)
		return
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		wizardYAMLValue(values),
	)
}

func wizardYAMLValue(values []string) *yaml.Node {
	if len(values) == 0 {
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	}
	if len(values) == 1 {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: values[0]}
	}
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, value := range values {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	}
	return node
}

func saveSignWith(value string) error {
	const key = "SIGN_WITH="
	data, err := os.ReadFile(".env")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	replaced := false
	for index, line := range lines {
		if strings.HasPrefix(line, key) {
			lines[index] = key + value
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, key+value)
	}
	return nostrpkg.WriteSecretFile(".env", []byte(strings.Join(lines, "\n")+"\n"))
}

func ensureEnvIgnored() error {
	data, err := os.ReadFile(".gitignore")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == ".env" || strings.TrimSpace(line) == "/.env" {
			return nil
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return os.WriteFile(".gitignore", append(data, []byte(".env\n")...), 0o644)
}

func wizardSuggestionSource(cfg zsp.Config) string {
	if cfg.Repository != "" {
		return cfg.Repository
	}
	if cfg.ReleaseSource != nil {
		return cfg.ReleaseSource.URL
	}
	return ""
}

func wizardReleaseLocation(cfg zsp.Config) string {
	if cfg.ReleaseSource != nil {
		if cfg.ReleaseSource.LocalPath != "" {
			return cfg.ReleaseSource.LocalPath
		}
		if cfg.ReleaseSource.URL != "" {
			return cfg.ReleaseSource.URL
		}
		if cfg.ReleaseSource.AssetURL != "" {
			return cfg.ReleaseSource.AssetURL
		}
	}
	return cfg.Repository
}
