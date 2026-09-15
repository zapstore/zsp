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
	publiczsp "github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/cli"
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
		ui.WritePanel(os.Stderr, "error", "Interactive terminal required", nil, []string{"Run a command such as zsp publish <input> instead."})
		return 1
	}
	steps := ui.NewStepTracker(3)
	steps.StartStep("🔎 Add your app")
	source, err := ui.PromptPath("App source", "Paste a GitHub, GitLab, Gitea, Forgejo, Codeberg, or F-Droid URL. For a local build, enter its APK path.", true)
	if err != nil {
		return wizardError("App discovery stopped", err)
	}
	publish, err := wizardConfigFromSource(source)
	if err != nil {
		return wizardError("App discovery stopped", err)
	}
	spinner := ui.NewSpinner("Finding and downloading APK releases...")
	spinner.Start()
	candidates, err := publiczsp.Fetch(ctx, publish.config.FetchConfig, publiczsp.FetchOptions{})
	if err != nil {
		spinner.StopWithError("Could not find a verified APK release")
		return wizardError("No verified APK release found", err)
	}
	spinner.Stop()
	defer closeCandidates(candidates)
	selected, err := selectPublicAPK(&cli.Options{}, candidates)
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
	ui.PrintSuccess(fmt.Sprintf("Found %s (%s)", appName, version))
	alreadyPublished := false
	publisher := nostrpkg.NewPublisher(relaysFromEnv())
	locations, _, lookupErr := publisher.FindAppEvents(ctx, selected.AppID)
	if lookupErr == nil {
		alreadyPublished = len(locations.ApplicationRelays) > 0 || len(locations.ReleaseRelays) > 0 || len(locations.AssetRelays) > 0
		wizardRelayStatus(appName, locations, publisher.RelayClassifications(ctx))
	} else {
		ui.PrintInfo("Couldn't check whether this app is already listed. No changes were made.")
	}

	suggestedTo := wizardDiscover(ctx, publish.config, selected)

	steps.StartStep("🔐 Claim your app")
	owns, err := ui.Confirm(fmt.Sprintf("Are you the author of %s?", appName), false)
	if err != nil {
		return wizardError("Ownership confirmation stopped", err)
	}
	if !owns {
		if lookupErr == nil && !alreadyPublished && suggestedTo != "" {
			ui.PrintInfo(appName + " was suggested for listing on " + suggestedTo + ". Ask its author to claim it.")
		}
		ui.WritePanel(os.Stdout, "success", "All done", nil, nil)
		return 0
	}
	state, err := wizardIdentity(ctx, selected.CertificateHash)
	if err != nil {
		return wizardError("Identity setup stopped", err)
	}
	defer state.close()
	if state.certificateHash != selected.CertificateHash {
		return wizardError("Identity setup stopped", fmt.Errorf("the selected APK is signed by a different certificate"))
	}
	ui.PrintSuccess(state.summary)

	steps.StartStep("📝 Configure app metadata")
	return wizardControlMetadata(ctx, publish, selected, state)
}

func wizardError(title string, err error) int {
	if err == ui.ErrInterrupted || errors.Is(err, context.Canceled) {
		ui.PrintInfo(wizardCancellationSummary(title))
		return 130
	}
	if errors.Is(err, huh.ErrUserAborted) {
		ui.PrintInfo("No changes were made.")
		return 0
	}
	ui.PrintInfo(wizardFailureSummary(title))
	return 1
}

func wizardCancellationSummary(title string) string {
	switch title {
	case "App discovery stopped", "No verified APK release found", "APK selection stopped":
		return "App discovery was cancelled. No changes were made."
	case "Configuration update stopped":
		return "Configuration update was cancelled. The previous zapstore.yaml was left unchanged."
	case "Publishing choice stopped", "Signing setup stopped", "Publication stopped", "CI/CD setup stopped":
		return "Publishing setup was cancelled. Your saved configuration was left unchanged."
	default:
		return "This step was cancelled. No changes were made."
	}
}

func wizardFailureSummary(title string) string {
	switch title {
	case "App discovery stopped":
		return "Couldn't find an app from that source. Try another location."
	case "No verified APK release found":
		return "No verified APK release was found. Try another release or source."
	case "APK selection stopped":
		return "No APK was selected. No changes were made."
	case "Ownership confirmation stopped", "Identity setup stopped":
		return "Ownership was not changed."
	case "Configuration setup stopped":
		return "Couldn't use zapstore.yaml. Fix its configuration and try again; it was left unchanged."
	case "Configuration update stopped":
		return "Couldn't save zapstore.yaml. The previous file was left unchanged."
	case "Publishing choice stopped":
		return "No publishing method was selected. Your configuration was saved."
	case "Signing setup stopped":
		return "Signing was not set up. Your configuration was saved."
	case "Publication stopped":
		return "This release was not published. Your configuration was saved."
	case "CI/CD setup stopped":
		return "CI/CD publishing was not enabled. Your configuration was saved."
	default:
		return "That step could not be completed. You can try again."
	}
}

func wizardRelayStatus(appName string, locations nostrpkg.AppEventLocations, classifications []nostrpkg.RelayClassification) {
	if len(locations.ApplicationRelays) > 0 {
		ui.PrintSuccess(fmt.Sprintf("Found %d application event(s) on %s", locations.ApplicationCount, strings.Join(locations.ApplicationRelays, ", ")))
	}
	if len(locations.ReleaseRelays) > 0 {
		ui.PrintSuccess(fmt.Sprintf("Found %d release event(s) on %s", locations.ReleaseCount, strings.Join(locations.ReleaseRelays, ", ")))
	}
	if len(locations.AssetRelays) > 0 {
		ui.PrintSuccess(fmt.Sprintf("Found %d APK asset event(s) on %s", locations.AssetCount, strings.Join(locations.AssetRelays, ", ")))
	}
	if len(locations.ApplicationRelays) == 0 && len(locations.ReleaseRelays) == 0 && len(locations.AssetRelays) == 0 {
		ui.PrintInfo(appName + " is not yet listed on " + strings.Join(locations.CheckedRelays, ", "))
	}
	for _, classification := range classifications {
		if classification.IsDefaultIndexer {
			ui.PrintInfo(classification.RelayURL + " is the default Zapstore indexer.")
		}
	}
}

func wizardDiscover(ctx context.Context, cfg publiczsp.Config, candidate *publiczsp.APK) string {
	source := wizardSuggestionSource(cfg)
	if source == "" || candidate.SourceURL == "" {
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

func wizardControlMetadata(ctx context.Context, publish wizardPublishConfig, selected *publiczsp.APK, state *wizardIdentityState) int {
	root, err := wizardProjectRoot()
	if err != nil {
		return wizardError("Configuration setup stopped", err)
	}
	path := filepath.Join(root, "zapstore.yaml")
	config, document, err := loadWizardYAML(path, selected)
	if err != nil {
		return wizardError("Configuration setup stopped", err)
	}
	if err := promptWizardMetadata(&config, selected); err != nil {
		return wizardError("Configuration setup stopped", err)
	}
	if err := saveWizardYAML(path, document, config, publish.config, root); err != nil {
		return wizardError("Configuration update stopped", err)
	}
	// Reload the saved canonical configuration. This retains unedited fields
	// and resolves new relative media paths exactly as later CLI publication
	// will, so preview and signed events match zapstore.yaml.
	canonical, err := publiczsp.LoadConfig(path)
	if err != nil {
		return wizardError("Configuration update stopped", err)
	}
	publish.config = canonical
	ui.WritePanel(os.Stdout, "success", "Updated zapstore.yaml", []ui.KeyValue{{Key: "Location", Value: path}}, []string{
		"It is ready to commit at the repository root.",
	})
	return wizardPublishingChoice(ctx, publish, selected, state)
}

type wizardIdentityState struct {
	certificateHash string
	owner           string
	delegate        string
	authorized      map[string]struct{}
	signer          nostrpkg.Signer
	summary         string
}

func (s *wizardIdentityState) close() {
	if s.signer != nil {
		_ = s.signer.Close()
	}
}

func wizardIdentity(ctx context.Context, expectedCertificateHash string) (*wizardIdentityState, error) {
	keystore, err := ui.PromptPath("Keystore or certificate", "ZSP already verified the APK certificate. Select matching signing material to prove control.", false)
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
		return nil, fmt.Errorf("this signing material does not match the APK certificate")
	}
	publisher := nostrpkg.NewPublisher(relaysFromEnv())
	proofs, _, err := publisher.FetchIdentityProofsByCertificate(ctx, certificateHash)
	if err != nil {
		return nil, fmt.Errorf("look up C1 proof: %w", err)
	}
	if owner, expiry := chooseCurrentProof(proofs, certificate, ""); owner != "" && time.Until(expiry) >= c1RenewalWindow {
		ok, err := confirmCurrentProof(ctx, certificateHash, owner, expiry)
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
	delegate, err := ui.PromptFieldDefault("CI/CD public key", "Optional npub or lowercase hexadecimal public key authorized to publish releases.", currentProofDelegate(proofs, certificate, chooseProofOwner(proofs, certificate)), false, false)
	if err != nil {
		signer.Close()
		return nil, err
	}
	if delegate != "" {
		delegate, err = parseDelegate(delegate)
		if err != nil {
			signer.Close()
			return nil, err
		}
	}
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
	if identity.ComputeCertHash(signingCertificate) != certificateHash {
		signer.Close()
		return nil, fmt.Errorf("this signing material does not match the APK certificate")
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

func confirmCurrentProof(ctx context.Context, certificateHash, owner string, expiry time.Time) (bool, error) {
	ownerDisplay := owner
	if profile := nostrpkg.NewPublisher(relaysFromEnv()).ProfileName(ctx, owner); profile != "" {
		ownerDisplay = profile + " (" + owner + ")"
	}
	details := []ui.KeyValue{
		{Key: "Nostr profile", Value: ownerDisplay},
		{Key: "Signing certificate", Value: certificateHash},
	}
	if !expiry.IsZero() {
		details = append(details, ui.KeyValue{Key: "Expires", Value: expiry.Local().Format(time.RFC822)})
	}
	ui.WritePanel(os.Stderr, "info", "Existing certificate ownership proof found", details, []string{
		"This proof confirms that this Nostr profile owns the certificate used to sign your APKs.",
		"Using it lets you publish releases without creating a new proof.",
	})
	return ui.Confirm("Is this the certificate you want linked to this Nostr profile?", true)
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
	config publiczsp.Config
}

func wizardConfigFromSource(source string) (wizardPublishConfig, error) {
	source = strings.TrimSpace(source)
	if !strings.Contains(source, "://") {
		absolutePath, err := filepath.Abs(source)
		if err != nil {
			return wizardPublishConfig{}, err
		}
		if info, err := os.Stat(absolutePath); err == nil && !info.IsDir() && strings.EqualFold(filepath.Ext(absolutePath), ".apk") {
			return wizardPublishConfig{
				config: publiczsp.Config{
					FetchConfig: publiczsp.FetchConfig{
						ReleaseSource: &publiczsp.ReleaseSource{LocalPath: absolutePath},
					},
				},
			}, nil
		}
	}
	source, err := validateSuggestionSource(source)
	if err != nil {
		return wizardPublishConfig{}, err
	}
	cfg := publiczsp.Config{}
	if isRepositorySuggestion(source) {
		cfg.Repository = source
	} else {
		cfg.ReleaseSource = &publiczsp.ReleaseSource{URL: source}
	}
	return wizardPublishConfig{config: cfg}, nil
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
}

func wizardProjectRoot() (string, error) {
	currentDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	value, err := ui.PromptPathDefault("Project directory", "Choose the local repository root where zapstore.yaml should be committed.", currentDirectory, true)
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
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%s is not a Git repository root", root)
		}
		return "", err
	}
	return root, nil
}

func loadWizardYAML(path string, apk *publiczsp.APK) (wizardMetadata, *yaml.Node, error) {
	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	metadata := wizardMetadata{name: apk.Name}
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
	existing, err := publiczsp.LoadConfig(path)
	if err != nil {
		return metadata, nil, err
	}
	metadata = wizardMetadata{
		name: existing.Name, summary: existing.Summary, description: existing.Description,
		tags: existing.Tags, license: existing.License, website: existing.Website,
		icon: existing.Icon, images: existing.Images,
	}
	if metadata.name == "" {
		metadata.name = apk.Name
	}
	return metadata, document, nil
}

func wizardPublishingChoice(ctx context.Context, publish wizardPublishConfig, selected *publiczsp.APK, state *wizardIdentityState) int {
	choice, err := ui.SelectField("Choose how to publish", "Let Zapstore discover the committed configuration for convenience, or publish yourself for more control.", []string{
		"Let Zapstore pick up the committed configuration (convenient)",
		"Publish manually or through CI/CD (more control)",
	})
	if err != nil {
		return wizardError("Publishing choice stopped", err)
	}
	if choice == "Let Zapstore pick up the committed configuration (convenient)" {
		ui.WritePanel(os.Stdout, "success", "Configuration ready", nil, []string{
			"Commit zapstore.yaml and Zapstore will use it to follow future releases.",
		})
		return 0
	}
	return wizardDirectPublishing(ctx, publish, selected, state)
}

func wizardDirectPublishing(ctx context.Context, publish wizardPublishConfig, selected *publiczsp.APK, state *wizardIdentityState) int {
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
	result, err := publiczsp.Publish(ctx, publish.config.PublishConfig, selected, publiczsp.PublishOptions{Preview: true})
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
	if identity.ComputeCertHash(certificate) != state.certificateHash {
		return wizardError("CI/CD setup stopped", fmt.Errorf("this signing material does not match the APK certificate"))
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

func promptWizardMetadata(metadata *wizardMetadata, apk *publiczsp.APK) error {
	var err error
	metadata.name, err = ui.PromptFieldDefault("App name", "Shown in Zapstore. This defaults to the verified APK label.", metadata.name, false, true)
	if err != nil {
		return err
	}
	metadata.summary, err = ui.PromptFieldDefault("Short summary", "A concise description for app listings. Optional.", metadata.summary, false, false)
	if err != nil {
		return err
	}
	metadata.description, err = ui.PromptFieldDefault("Description", "What does "+metadata.name+" do? Optional.", metadata.description, false, false)
	if err != nil {
		return err
	}
	tags, err := ui.PromptFieldDefault("Tags", "Comma-separated search tags. Optional.", strings.Join(metadata.tags, ", "), false, false)
	if err != nil {
		return err
	}
	metadata.tags = splitWizardList(tags)
	metadata.license, err = ui.PromptFieldDefault("License", "For example: GPL-3.0-or-later or Apache-2.0. Optional.", metadata.license, false, false)
	if err != nil {
		return err
	}
	metadata.website, err = ui.PromptFieldDefault("Website", "Project home page. Optional.", metadata.website, false, false)
	if err != nil {
		return err
	}
	metadata.icon, err = ui.PromptFieldDefault("Icon", "Public URL or path relative to this project. Optional.", metadata.icon, false, false)
	if err != nil {
		return err
	}
	images, err := ui.PromptFieldDefault("Screenshots", "Comma-separated public URLs or project-relative paths. Optional.", strings.Join(metadata.images, ", "), false, false)
	if err != nil {
		return err
	}
	metadata.images = splitWizardList(images)
	return nil
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

func applyWizardMetadata(config *publiczsp.Config, metadata wizardMetadata) {
	config.Name = metadata.name
	config.Summary = metadata.summary
	config.Description = metadata.description
	config.Tags = append([]string(nil), metadata.tags...)
	config.License = metadata.license
	config.Website = metadata.website
	config.Icon = metadata.icon
	config.Images = append([]string(nil), metadata.images...)
}

func saveWizardYAML(path string, document *yaml.Node, metadata wizardMetadata, source publiczsp.Config, root string) error {
	if document == nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("existing zapstore.yaml must contain a YAML mapping")
	}
	values := map[string][]string{
		"name":        {metadata.name},
		"summary":     {metadata.summary},
		"description": {metadata.description},
		"tags":        metadata.tags,
		"license":     {metadata.license},
		"website":     {metadata.website},
		"icon":        {metadata.icon},
		"images":      metadata.images,
	}
	for key, value := range values {
		setWizardYAMLValue(document.Content[0], key, value)
	}
	if source.Repository != "" {
		setWizardYAMLValue(document.Content[0], "repository", []string{source.Repository})
	} else if source.ReleaseSource != nil {
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
	if _, err := publiczsp.LoadConfig(temporaryPath); err != nil {
		return fmt.Errorf("validate zapstore.yaml: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
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

func wizardYAMLValue(values []string) *yaml.Node {
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

func wizardSuggestionSource(cfg publiczsp.Config) string {
	if cfg.Repository != "" {
		return cfg.Repository
	}
	if cfg.ReleaseSource != nil {
		return cfg.ReleaseSource.URL
	}
	return ""
}
