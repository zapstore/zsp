package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/identity"
	nostrpkg "github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/source"
	"github.com/zapstore/zsp/internal/ui"
)

const c1RenewalWindow = 30 * 24 * time.Hour

// errNsecSetup ends the wizard so the developer can put an nsec in .env; the
// wizard never takes a pasted nsec.
var errNsecSetup = errors.New("nsec setup required")

// wizardRun is the configuration the wizard resolved for one run. configLabel
// names the zapstore.yaml in use, empty when the run has none. yamlBytes is
// the file text the run started from. hadYAML is true when a local or
// committed file was already in use. sourceChanged is true when the developer
// replaced the source in a local file.
type wizardRun struct {
	config        zsp.Config
	configLabel   string
	yamlBytes     []byte
	localYAML     bool
	hadYAML       bool
	sourceChanged bool
	wroteYAML     bool
}

// runWizard is the interactive entry point for `zsp`.
func runWizard(ctx context.Context) int {
	if !isTTY() {
		ui.WritePanel(os.Stderr, "error", "Interactive terminal required", nil, []string{"Run zsp publish from a directory that has zapstore.yaml"})
		return 1
	}
	fmt.Println(ui.InfoStyle.Italic(true).Render("Publish your Android app on Zapstore and prove it's yours."))
	relayURLs := relaysFromEnv()
	publisher := nostrpkg.NewPublisher(relayURLs)
	unreachableRelays, err := publisher.EnsureReachable(ctx)
	if err != nil {
		ui.WritePanel(os.Stderr, "error", "Couldn't reach any configured relay", wizardRelayTargets(relayURLs), []string{"No changes were made"})
		return 1
	}
	if len(unreachableRelays) > 0 {
		ui.WritePanel(os.Stderr, "warning", "Some configured relays couldn't be reached", wizardRelayTargets(unreachableRelays), wizardContinuingNotes(relayURLs, unreachableRelays))
	}
	local, localPath, err := loadWizardConfig(wizardConfigName)
	if err != nil {
		return wizardError("Configuration setup stopped", err)
	}

	steps := ui.NewStepTracker(3)
	steps.StartStep("📦 Add your app")
	defaultSource := wizardSourceDefault(local)
	var run wizardRun
	var candidates []*zsp.APK
	var selected *zsp.APK
	defer func() { closeCandidates(candidates) }()
	for {
		answer, err := ui.PromptPathDefault("Where is your APK?", "Paste a releases page (GitHub, GitLab, Codeberg, F-Droid…) or the path to an APK file.", defaultSource, true)
		if err != nil {
			return wizardError("App discovery stopped", err)
		}
		run, err = wizardResolveRun(ctx, strings.TrimSpace(answer), defaultSource, local, localPath)
		if err != nil {
			return wizardError("App discovery stopped", err)
		}

		spinner := ui.NewSpinner("Finding and inspecting APK releases...")
		spinner.Start()
		candidates, err = zsp.Fetch(ctx, run.config.Fetch, zsp.FetchOptions{SkipHTTPCache: true})
		spinner.Stop()
		if err != nil {
			if isWizardCancel(err) {
				return wizardError("No verified APK release found", err)
			}
			ui.PrintInfo("No verified APK release was found at " + wizardReleaseLocation(run.config))
			if reason := wizardFailureReason(err); reason != "" {
				ui.PrintInfo("Reason: " + reason)
			}
			return 1
		}
		selected, err = selectAPKForAppSetup(candidates)
		if err != nil {
			return wizardError("APK selection stopped", err)
		}
		back, err := wizardChooseMetadata(ctx, &run, selected.AppID)
		if back {
			closeCandidates(candidates)
			candidates = nil
			continue
		}
		if err != nil {
			return wizardError("Listing sources stopped", err)
		}
		break
	}
	appName := selected.Name
	if appName == "" {
		appName = selected.AppID
	}
	version := wizardVersionLabel(selected)
	location := wizardReleaseLocation(run.config)
	ui.PrintSuccess(fmt.Sprintf("Found %s (%s) at %s", appName, version, location))

	spinner := ui.NewSpinner("Checking whether " + appName + " is on Zapstore...")
	spinner.Start()
	locations, _, err := publisher.FindAppEvents(ctx, selected.AppID)
	spinner.Stop()
	if err != nil {
		return wizardError("Relay reachability check stopped", err)
	}
	listed := len(locations.ApplicationRelays) > 0
	wizardRelayStatus(appName, locations)

	path, err := selected.Path()
	if err != nil {
		return wizardError("Identity setup stopped", err)
	}
	chain, err := apk.SigningCertificates(ctx, path)
	if err != nil || len(chain) == 0 {
		return wizardError("Identity setup stopped", err)
	}
	certificate := chain[len(chain)-1]

	steps.StartStep("🔐 Prove it's yours")
	spinner = ui.NewSpinner("Checking who has claimed " + appName + "...")
	spinner.Start()
	proofs, _, err := publisher.FetchIdentityProofs(ctx, []string{selected.CertificateHash})
	spinner.Stop()
	if err != nil {
		return wizardError("Identity setup stopped", fmt.Errorf("look up C1 proof: %w", err))
	}
	state := wizardExistingIdentity(proofs, certificate, appName)
	if state != nil {
		ui.WritePanel(os.Stdout, "success", state.summary, state.details, nil)
	} else {
		state, err = wizardClaim(ctx, certificate, appName, proofs, listed)
		if err != nil {
			return wizardError("Identity setup stopped", err)
		}
		if listed {
			ui.WritePanel(os.Stdout, "success", "You're the verified developer of "+appName, nil, []string{
				"That's all Zapstore needs. New releases from " + location + " keep appearing, now linked to your Nostr profile.",
			})
		} else {
			ui.PrintInfo("Your proof goes out with the first release")
		}
	}
	defer state.close()

	steps.StartStep("🚀 Publish")
	if listed {
		ui.PrintInfo("Nothing to publish: Zapstore keeps " + appName + " up to date from " + location + ".")
	} else if code := wizardFirstRelease(ctx, run, selected, state, appName, version); code != 0 || !state.published {
		return code
	}
	return wizardNextReleases(ctx, state, appName, run.wroteYAML, run.hadYAML)
}

func isWizardCancel(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, huh.ErrUserAborted)
}

// wizardContinuingNotes names the relays the wizard keeps using.
func wizardContinuingNotes(relays, unreachable []string) []string {
	skip := make(map[string]struct{}, len(unreachable))
	for _, relay := range unreachable {
		skip[relay] = struct{}{}
	}
	var reachable []string
	for _, relay := range relays {
		if _, ok := skip[relay]; !ok {
			reachable = append(reachable, relay)
		}
	}
	names := make([]string, 0, len(reachable))
	for _, target := range wizardRelayTargets(reachable) {
		names = append(names, target.Value)
	}
	return []string{"Continuing with " + strings.Join(names, ", ")}
}

// loadWizardConfig loads zapstore.yaml from the working directory. It returns
// the absolute path when the file exists, and an empty one when it does not.
func loadWizardConfig(path string) (zsp.Config, string, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return zsp.Config{}, "", nil
	} else if err != nil {
		return zsp.Config{}, "", err
	}
	loaded, err := zsp.LoadConfig(path)
	if err != nil {
		return zsp.Config{}, "", err
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return zsp.Config{}, "", err
	}
	return loaded, absolutePath, nil
}

func wizardSourceDefault(cfg zsp.Config) string {
	if source := cfg.Fetch.ReleaseSource; source != nil {
		if source.LocalPath != "" {
			return source.LocalPath
		}
		if source.URL != "" {
			return source.URL
		}
	}
	return cfg.Fetch.Repository
}

// wizardResolveRun builds the run configuration from the answer. A local
// zapstore.yaml wins; changing its source keeps its filters. Without one, a
// forge repository's committed zapstore.yaml is used when it is valid.
func wizardResolveRun(ctx context.Context, answer, defaultSource string, local zsp.Config, localPath string) (wizardRun, error) {
	if localPath != "" {
		data, err := os.ReadFile(localPath)
		if err != nil {
			return wizardRun{}, err
		}
		run := wizardRun{config: local, configLabel: localPath, yamlBytes: data, localYAML: true, hadYAML: true}
		if answer != defaultSource {
			fetch, err := wizardConfigFromSource(answer)
			if err != nil {
				return wizardRun{}, err
			}
			fetch.ReleaseFilter = local.Fetch.ReleaseFilter
			fetch.Match = local.Fetch.Match
			fetch.PrereleaseChannel = local.Fetch.PrereleaseChannel
			run.config.Fetch = fetch
			run.sourceChanged = true
		}
		ui.PrintInfo("Using the local zapstore.yaml (" + localPath + ")")
		return run, nil
	}
	fetch, err := wizardConfigFromSource(answer)
	if err != nil {
		return wizardRun{}, err
	}
	run := wizardRun{config: zsp.Config{Fetch: fetch}}
	if fetch.Repository == "" || fetch.ReleaseSource != nil {
		return run, nil
	}
	spinner := ui.NewSpinner("Looking for zapstore.yaml in " + fetch.Repository + "...")
	spinner.Start()
	remote, remoteBytes, ok := wizardRepoConfig(ctx, fetch.Repository)
	spinner.Stop()
	if !ok {
		ui.PrintInfo("No zapstore.yaml committed in " + fetch.Repository + " yet")
		return run, nil
	}
	ui.PrintInfo("Using the committed zapstore.yaml in " + fetch.Repository + " (default branch)")
	return wizardRun{config: remote, configLabel: fetch.Repository, yamlBytes: remoteBytes, hadYAML: true}, nil
}

// wizardRepoConfig fetches the repository-root zapstore.yaml on the default
// branch. Relative media and release-note paths resolve from that root. A
// missing or invalid file, including one with a local release source, is
// reported as absent.
func wizardRepoConfig(ctx context.Context, repository string) (zsp.Config, []byte, bool) {
	data, err := source.FetchRepoConfig(ctx, repository)
	if err != nil {
		return zsp.Config{}, nil, false
	}
	cfg, ok := parseWizardRepoConfig(data, repository)
	if !ok {
		return zsp.Config{}, nil, false
	}
	return cfg, data, true
}

func parseWizardRepoConfig(data []byte, repository string) (zsp.Config, bool) {
	cfg, err := zsp.ParseConfig(bytes.NewReader(data))
	if err != nil {
		return zsp.Config{}, false
	}
	if releaseSource := cfg.Fetch.ReleaseSource; releaseSource != nil && (releaseSource.LocalPath != "" || releaseSource.Type == "local") {
		return zsp.Config{}, false
	}
	resolve := func(value string) (string, bool) {
		if value == "" || strings.Contains(value, "://") {
			return value, true
		}
		return source.RepoFileURL(repository, value)
	}
	var ok bool
	if cfg.Publish.Icon, ok = resolve(cfg.Publish.Icon); !ok {
		return zsp.Config{}, false
	}
	if cfg.Publish.ReleaseNotes, ok = resolve(cfg.Publish.ReleaseNotes); !ok {
		return zsp.Config{}, false
	}
	for index, image := range cfg.Publish.Images {
		if cfg.Publish.Images[index], ok = resolve(image); !ok {
			return zsp.Config{}, false
		}
	}
	if cfg.Fetch.Repository == "" {
		cfg.Fetch.Repository = repository
	}
	return cfg, true
}

// wizardConfigFromSource turns the answer into a source: a local APK file or
// directory, a forge repository, or any other release source URL.
func wizardConfigFromSource(answer string) (zsp.FetchConfig, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return zsp.FetchConfig{}, fmt.Errorf("an APK source is required")
	}
	if !strings.Contains(answer, "://") {
		if info, err := os.Stat(answer); err == nil {
			absolutePath, err := filepath.Abs(answer)
			if err != nil {
				return zsp.FetchConfig{}, err
			}
			if !info.IsDir() && !strings.EqualFold(filepath.Ext(absolutePath), ".apk") {
				return zsp.FetchConfig{}, fmt.Errorf("%s is not an APK file", answer)
			}
			return zsp.FetchConfig{ReleaseSource: &zsp.ReleaseSource{LocalPath: absolutePath}}, nil
		}
		if strings.HasPrefix(answer, ".") || strings.HasPrefix(answer, "/") || strings.HasPrefix(answer, "~") {
			return zsp.FetchConfig{}, fmt.Errorf("no file at %s", answer)
		}
	}
	value, err := validateSuggestionSource(answer)
	if err != nil {
		return zsp.FetchConfig{}, err
	}
	if repository, ok := config.CanonicalForgeRepositoryURL(value, ""); ok {
		return zsp.FetchConfig{Repository: repository}, nil
	}
	return zsp.FetchConfig{ReleaseSource: &zsp.ReleaseSource{URL: value}}, nil
}

func wizardVersionLabel(apk *zsp.APK) string {
	if apk.VersionName == "" {
		return fmt.Sprintf("version code %d", apk.VersionCode)
	}
	return "v" + apk.VersionName
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
	if errors.Is(err, errNsecSetup) {
		ui.WritePanel(os.Stdout, "info", "Use your nsec from .env", nil, wizardNsecNotes())
		return 0
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, huh.ErrUserAborted) {
		ui.PrintInfo(wizardCancellationSummary(title))
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return 0
	}
	kind, summary, details := wizardFailurePresentation(title, err)
	if kind == "error" {
		ui.WritePanel(os.Stderr, kind, summary, details, nil)
		return 1
	}
	ui.PrintInfo(summary)
	if reason := wizardFailureReason(err); reason != "" {
		ui.PrintInfo("Reason: " + reason)
	}
	return 1
}

func wizardNsecNotes() []string {
	return []string{
		"1. Add SIGN_WITH=nsec1… to .env in this directory.",
		"2. Run: chmod 600 .env",
		"3. Keep .env out of Git: add it to .gitignore.",
		"4. Run zsp again.",
		"A bunker is safer: your key never touches this machine.",
	}
}

// wizardFailureReason renders the sanitized cause of a failed wizard step so a
// canned summary does not hide an actionable reason (wrong password, malformed
// keystore, unreachable signer). Credentials and key material are redacted
// before display.
func wizardFailureReason(err error) string {
	if err == nil {
		return ""
	}
	return strings.Join(strings.Fields(ui.SanitizeErrorMessage(err)), " ")
}

func wizardFailurePresentation(title string, err error) (kind, summary string, details []ui.KeyValue) {
	if details = wizardFailureDetails(err); len(details) > 0 {
		return "error", "This keystore does not match the APK. No proof was changed", details
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
	case "App discovery stopped", "No verified APK release found", "APK selection stopped", "Listing sources stopped":
		return "App discovery was cancelled. No changes were made"
	case "Identity setup stopped":
		return "No proof was changed"
	case "Publishing choice stopped", "Signing setup stopped", "Publication stopped":
		return "Publishing was cancelled. Nothing was published"
	case "CI/CD setup stopped":
		return "CI/CD setup was cancelled. No proof was changed"
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
	case "Identity setup stopped":
		return "No proof was changed"
	case "Configuration setup stopped":
		return "Couldn't use zapstore.yaml. Fix it and try again"
	case "Listing sources stopped":
		return "Couldn't save zapstore.yaml"
	case "Publishing choice stopped", "Signing setup stopped":
		return "Nothing was published"
	case "Publication stopped":
		return "This release was not published"
	case "CI/CD setup stopped":
		return "CI/CD publishing was not enabled. No proof was changed"
	default:
		return "That step could not be completed. You can try again"
	}
}

func wizardRelayStatus(appName string, locations nostrpkg.AppEventLocations) {
	if len(locations.ApplicationRelays) > 0 {
		ui.PrintInfo(fmt.Sprintf("%s is already on %s.", appName, strings.Join(locations.ApplicationRelays, ", ")))
		return
	}
	ui.PrintInfo(fmt.Sprintf("%s isn't on %s yet.", appName, strings.Join(locations.CheckedRelays, ", ")))
}

type wizardIdentityState struct {
	certificateHash string
	owner           string
	delegate        string
	signer          nostrpkg.Signer
	// hotbox, or privateKey and certificate, hold the certificate key for the
	// run once it is available, so a later step never asks for it again.
	hotbox      zsp.ProofSigner
	privateKey  crypto.PrivateKey
	certificate *x509.Certificate
	// proofPending means the proof goes with the first release.
	proofPending bool
	published    bool
	summary      string
	details      []ui.KeyValue
}

func (s *wizardIdentityState) close() {
	if s.signer != nil {
		_ = s.signer.Close()
	}
}

func (s *wizardIdentityState) hasCertificateKey() bool {
	return s.hotbox != nil || s.privateKey != nil
}

// certificateSigner signs the proof message with hotbox or the keystore,
// for the signer's npub.
func (s *wizardIdentityState) certificateSigner() zsp.ProofSigner {
	if s.hotbox != nil {
		return s.hotbox
	}
	return keystoreProofSigner{privateKey: s.privateKey, certificate: s.certificate, publicKey: s.signer.PublicKey()}
}

// wizardCertificateKey uses hotbox when it is configured, else asks for the
// keystore. With back, Esc on the keystore prompt returns ui.ErrBack.
func wizardCertificateKey(ctx context.Context, state *wizardIdentityState, description string, back bool) error {
	if hotbox := hotboxProofSigner(); hotbox != nil {
		state.hotbox = hotbox
		ui.PrintInfo("Hotbox signs the proof; no keystore needed.")
		return nil
	}
	privateKey, certificate, err := wizardKeystore(ctx, state.certificateHash, description, back)
	if err != nil {
		return err
	}
	state.privateKey, state.certificate = privateKey, certificate
	return nil
}

// wizardClaim gets the certificate key and a signer, then creates or renews
// the proof. A listed app gets the proof now; for an app not yet listed it
// goes with the first release.
func wizardClaim(ctx context.Context, certificate *x509.Certificate, appName string, proofs []*gonostr.Event, listed bool) (*wizardIdentityState, error) {
	state := &wizardIdentityState{certificateHash: identity.ComputeCertHash(certificate)}
	description := "Zapstore checks that the key that signed " + appName + " belongs to you.\nNot the developer? Press Ctrl-C to quit. Nothing was changed."
	if err := wizardCertificateKey(ctx, state, description, false); err != nil {
		return nil, err
	}
	signer, err := wizardSigner(ctx)
	if err != nil {
		return nil, err
	}
	state.signer, state.owner = signer, signer.PublicKey()
	if !listed {
		state.proofPending = true
		return state, nil
	}
	event, _ := activeProofForCertificate(proofs, certificate, time.Now())
	delegate := proofDelegate(event)
	spinner := ui.NewSpinner("Publishing your proof...")
	spinner.Start()
	err = publishWizardProof(ctx, state, delegate)
	spinner.Stop()
	if err != nil {
		signer.Close()
		return nil, err
	}
	state.delegate = delegate
	return state, nil
}

// publishWizardProof signs a proof with the certificate key and publishes it
// alone. It is used only for a listed app.
func publishWizardProof(ctx context.Context, state *wizardIdentityState, delegate string) error {
	now := time.Now()
	createdAt, expiry := now.Unix(), now.Add(identity.DefaultExpiry).Unix()
	signed, err := state.certificateSigner().SignProof(ctx, createdAt, expiry)
	if err != nil {
		return fmt.Errorf("sign proof: %w", err)
	}
	if signed.CertificateHash != state.certificateHash {
		return errCertificateHashMismatch(state.certificateHash, signed.CertificateHash)
	}
	if signed.PublicKey != state.signer.PublicKey() {
		return fmt.Errorf("the certificate key signed for a different Nostr key than SIGN_WITH")
	}
	proof := &identity.IdentityProof{
		CertHash:  signed.CertificateHash,
		Signature: base64.StdEncoding.EncodeToString(signed.Signature),
		CreatedAt: createdAt,
		Expiry:    expiry,
	}
	event := nostrpkg.BuildIdentityProofEvent(proof.ToEventTags(), state.signer.PublicKey(), createdAt)
	if delegate != "" {
		event.Tags = append(event.Tags, gonostr.Tag{"delegation", delegate})
	}
	if err := state.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign proof event: %w", err)
	}
	if state.certificate != nil {
		if _, err := identity.ValidateActiveProofEvent(event, state.certificate, now); err != nil {
			return fmt.Errorf("proof is invalid: %w", err)
		}
	}
	results := nostrpkg.NewPublisher(relaysFromEnv()).Publish(ctx, event)
	if !anyRelayAccepted(results) {
		return &c1RelayPublishError{CertificateHash: proof.CertHash, EventID: event.ID, Results: results}
	}
	return nil
}

// wizardKeystore asks for the signing keystore and returns its key and
// certificate once the pair is valid and matches the APK certificate.
func wizardKeystore(ctx context.Context, expectedCertificateHash, description string, back bool) (crypto.PrivateKey, *x509.Certificate, error) {
	var keystore string
	var err error
	if back {
		keystore, err = ui.PromptPathBack(ctx, "Signing keystore", description, true)
	} else {
		keystore, err = ui.PromptPath("Signing keystore", description, true)
	}
	if err != nil {
		return nil, nil, err
	}
	if certificate, err := loadCertificateMaterial(keystore); err == nil {
		if hash := identity.ComputeCertHash(certificate); hash != expectedCertificateHash {
			return nil, nil, errCertificateHashMismatch(expectedCertificateHash, hash)
		}
	}
	privateKey, certificate, err := loadIdentityMaterial(proofOptions{Keystore: keystore, Expiry: "2y"})
	if err != nil {
		return nil, nil, err
	}
	if err := identity.ValidateKeyCertPair(privateKey, certificate); err != nil {
		return nil, nil, err
	}
	if hash := identity.ComputeCertHash(certificate); hash != expectedCertificateHash {
		return nil, nil, errCertificateHashMismatch(expectedCertificateHash, hash)
	}
	return privateKey, certificate, nil
}

func wizardExistingIdentity(proofs []*gonostr.Event, certificate *x509.Certificate, appName string) *wizardIdentityState {
	event, proof := activeProofForCertificate(proofs, certificate, time.Now())
	if event == nil || time.Until(proof.ExpiryTime()) < c1RenewalWindow {
		return nil
	}
	return &wizardIdentityState{
		certificateHash: identity.ComputeCertHash(certificate),
		owner:           event.PubKey,
		delegate:        proofDelegate(event),
		summary:         appName + " is claimed by " + abbreviateHash(npubFromHex(event.PubKey)),
		details:         existingOwnershipProofDetails(event.PubKey, proof.ExpiryTime()),
	}
}

func authorizedPublisherSet(owner, delegate string) map[string]struct{} {
	authorized := map[string]struct{}{owner: {}}
	if delegate != "" {
		authorized[delegate] = struct{}{}
	}
	return authorized
}

func existingOwnershipProofDetails(owner string, expiry time.Time) []ui.KeyValue {
	details := make([]ui.KeyValue, 0, 2)
	if owner != "" {
		details = append(details, ui.KeyValue{Key: "Owner", Value: npubFromHex(owner)})
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

// activeProofForCertificate returns the newest C1 proof that is active at now
// for one certificate.
func activeProofForCertificate(events []*gonostr.Event, certificate *x509.Certificate, now time.Time) (*gonostr.Event, *identity.IdentityProof) {
	if certificate == nil {
		return nil, nil
	}
	want := identity.ComputeCertHash(certificate)
	var active *gonostr.Event
	var activeProof *identity.IdentityProof
	for _, event := range events {
		if event == nil {
			continue
		}
		tag := event.Tags.GetFirst([]string{"d"})
		if tag == nil || len(*tag) < 2 || strings.ToLower((*tag)[1]) != want {
			continue
		}
		proof, err := identity.ValidateActiveProofEvent(event, certificate, now)
		if err != nil {
			continue
		}
		if active == nil || event.CreatedAt > active.CreatedAt ||
			(event.CreatedAt == active.CreatedAt && event.ID < active.ID) {
			active, activeProof = event, proof
		}
	}
	return active, activeProof
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

const (
	signerBunker = "Paste a bunker:// URL (recommended)"
	signerNsec   = "Use an nsec from .env"
)

// wizardSigner returns the SIGN_WITH signer, else asks for a bunker. It never
// takes a pasted nsec: choosing nsec returns errNsecSetup.
func wizardSigner(ctx context.Context) (nostrpkg.Signer, error) {
	signWith, err := config.GetSignWith()
	if err != nil {
		return nil, err
	}
	for signWith == "" {
		kind, err := ui.SelectField("Nostr signer", "Your listing is signed with your Nostr key. A remote signer (bunker) keeps that key off this machine.", []string{signerBunker, signerNsec})
		if err != nil {
			return nil, err
		}
		if kind == signerNsec {
			return nil, errNsecSetup
		}
		// The bunker URL carries a NIP-46 secret; never echo it to the terminal.
		value, err := ui.PromptFieldBack(ctx, "Bunker URL", "Paste the bunker:// connection from your signer app. Esc goes back.", true, true)
		if errors.Is(err, ui.ErrBack) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(value, "bunker://") {
			ui.PrintInfo("That isn't a bunker:// URL. Try again.")
			continue
		}
		save, err := ui.ConfirmBack(ctx, "Save this bunker URL to .env?", "So zsp doesn't ask next time. .env stays owner-readable and out of Git.", true)
		if errors.Is(err, ui.ErrBack) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if save {
			if err := ensureEnvIgnored(); err != nil {
				return nil, err
			}
			if err := saveSignWith(value); err != nil {
				return nil, err
			}
		}
		signWith = value
		_ = os.Setenv("SIGN_WITH", signWith)
	}
	return nostrpkg.NewSigner(ctx, signWith)
}

// keystoreProofSigner signs the C1 message with the keystore opened in the
// wizard.
type keystoreProofSigner struct {
	privateKey  crypto.PrivateKey
	certificate *x509.Certificate
	publicKey   string
}

func (s keystoreProofSigner) SignProof(_ context.Context, createdAt, expiry int64) (zsp.ProofSignature, error) {
	signature, err := identity.SignProofMessage(s.privateKey, s.publicKey, createdAt, expiry)
	if err != nil {
		return zsp.ProofSignature{}, err
	}
	return zsp.ProofSignature{PublicKey: s.publicKey, CertificateHash: identity.ComputeCertHash(s.certificate), Signature: signature}, nil
}

// wizardFirstRelease offers the first release of an unlisted app; its proof
// goes with it.
func wizardFirstRelease(ctx context.Context, run wizardRun, selected *zsp.APK, state *wizardIdentityState, appName, version string) int {
	description := "Zapstore lists " + appName + " once its first release is published."
	if state.proofPending {
		description += " Your proof goes with it."
	}
	choice, err := ui.SelectField(fmt.Sprintf("Publish %s %s now?", appName, version), description, []string{"Publish now", "Quit"})
	if err != nil {
		return wizardError("Publishing choice stopped", err)
	}
	if choice == "Quit" {
		ui.PrintInfo("Nothing was published.")
		return 0
	}
	return wizardPublishNow(ctx, run, selected, state)
}

func wizardPublishNow(ctx context.Context, run wizardRun, selected *zsp.APK, state *wizardIdentityState) int {
	if state.signer == nil {
		signer, err := wizardSigner(ctx)
		if err != nil {
			return wizardError("Signing setup stopped", err)
		}
		state.signer = signer
	}
	options := zsp.PublishOptions{
		Preview: true,
		OnProgress: func(progress zsp.Progress) {
			if progress.Phase == "preview" && progress.Target != "" {
				ui.PrintInfo("Preview " + progress.Target)
			}
		},
	}
	if state.proofPending {
		options.ProofSigner = state.certificateSigner()
	} else if _, allowed := authorizedPublisherSet(state.owner, state.delegate)[state.signer.PublicKey()]; !allowed {
		return wizardError("Signing setup stopped", fmt.Errorf("SIGN_WITH is not the developer key or its delegate for this app"))
	}
	result, err := zsp.Publish(ctx, run.config.Publish, selected, options)
	if err != nil {
		if result != nil && result.Status == "partial" {
			ui.PrintInfo("Publication was incomplete. Some files or events were sent; run zsp again to retry.")
			if reason := wizardFailureReason(err); reason != "" {
				ui.PrintInfo("Reason: " + reason)
			}
			return 1
		}
		return wizardError("Publication stopped", err)
	}
	state.published = true
	ui.WritePanel(os.Stdout, "success", "Release published", []ui.KeyValue{{Key: "Release", Value: result.ID}}, nil)
	return 0
}

// wizardNextReleases is the last question: Zapstore publishes new releases
// when RELAYS includes its relay, else the developer does; or the
// developer's CI does. Esc on a CI/CD prompt returns to it.
func wizardNextReleases(ctx context.Context, state *wizardIdentityState, appName string, wroteYAML, hadYAML bool) int {
	zapstore := hasZapstoreRelay(relaysFromEnv())
	for {
		choice, err := ui.SelectField("How should new releases of "+appName+" reach Zapstore?", "", wizardNextReleaseChoices(zapstore))
		if err != nil {
			return wizardError("Setup stopped", err)
		}
		if choice != wizardChoiceCICD {
			wizardHandoff(appName, wroteYAML, hadYAML, zapstore)
			return 0
		}
		code, back := wizardDelegation(ctx, state, appName)
		if !back {
			return code
		}
	}
}

const wizardChoiceCICD = "Set up CI/CD publishing (advanced)"

func wizardNextReleaseChoices(zapstore bool) []string {
	if zapstore {
		return []string{"Let Zapstore publish them for you (recommended)", wizardChoiceCICD}
	}
	return []string{"Publish them yourself with zsp publish", wizardChoiceCICD}
}

// wizardDelegation names the CI's key as the delegate in a new proof. It
// reports back when the developer pressed Esc on its first prompt.
func wizardDelegation(ctx context.Context, state *wizardIdentityState, appName string) (int, bool) {
	for {
		value, err := ui.PromptFieldBack(ctx, "CI public key (npub)", "Create a separate Nostr key for your CI and paste its npub here. The nsec never leaves your CI: store it there as the SIGN_WITH secret. Your proof names this key as your delegate, so it can publish releases of "+appName+" but can't change who owns it. To rotate it, run zsp again and name a new key: the new proof replaces the old one. Esc goes back.", false, true)
		if errors.Is(err, ui.ErrBack) {
			return 0, true
		}
		if err != nil {
			return wizardError("CI/CD setup stopped", err), false
		}
		delegate, err := parseDelegate(value)
		if err != nil {
			ui.PrintInfo("That isn't an npub or hex public key. Paste the npub, never the nsec.")
			continue
		}
		if !state.hasCertificateKey() {
			err := wizardCertificateKey(ctx, state, "Choose the keystore that signed "+appName+". Esc goes back.", true)
			if errors.Is(err, ui.ErrBack) {
				continue
			}
			if err != nil {
				return wizardError("CI/CD setup stopped", err), false
			}
		}
		if state.signer == nil {
			signer, err := wizardSigner(ctx)
			if err != nil {
				return wizardError("CI/CD setup stopped", err), false
			}
			state.signer = signer
		}
		if state.signer.PublicKey() != state.owner {
			return wizardError("CI/CD setup stopped", fmt.Errorf("SIGN_WITH is not the developer key for this app")), false
		}
		if err := publishWizardProof(ctx, state, delegate); err != nil {
			return wizardError("CI/CD setup stopped", err), false
		}
		ui.WritePanel(os.Stdout, "success", "CI/CD publishing enabled", nil, []string{
			"In your CI, set the delegate's nsec as the SIGN_WITH secret and run zsp publish after each release.",
			"To rotate the key, run zsp again and choose CI/CD with a new npub.",
		})
		return 0, false
	}
}

// wizardHandoffNotes is the last note. A file written this run should be
// committed. A file the run left unchanged can still be improved by an agent.
func wizardHandoffNotes(appName string, wroteYAML, hadYAML, zapstore bool) (string, []string) {
	var next string
	switch {
	case (wroteYAML || !hadYAML) && zapstore:
		next = "Wrote zapstore.yaml. Commit it and push it. An agent can improve it."
	case wroteYAML || !hadYAML:
		next = "Wrote zapstore.yaml. Commit it, then run zsp publish after each release. An agent can improve it."
	case zapstore:
		next = `Next: ask your coding agent to "run zsp skill and improve the zapstore.yaml", then push it.`
	default:
		next = `Next: ask your coding agent to "run zsp skill and improve the zapstore.yaml", then run zsp publish after each release.`
	}
	if zapstore {
		return "Zapstore will keep " + appName + " up to date", []string{
			"Your proof lets Zapstore publish each new release, shown as developed by you.",
			next,
		}
	}
	return "You'll publish new releases of " + appName, []string{
		"Your proof lets you publish each release with zsp publish, shown as developed by you.",
		next,
	}
}

func wizardHandoff(appName string, wroteYAML, hadYAML, zapstore bool) {
	title, notes := wizardHandoffNotes(appName, wroteYAML, hadYAML, zapstore)
	ui.WritePanel(os.Stdout, "success", title, nil, notes)
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

func wizardReleaseLocation(cfg zsp.Config) string {
	if cfg.Fetch.ReleaseSource != nil {
		if cfg.Fetch.ReleaseSource.LocalPath != "" {
			return cfg.Fetch.ReleaseSource.LocalPath
		}
		if cfg.Fetch.ReleaseSource.URL != "" {
			return cfg.Fetch.ReleaseSource.URL
		}
		if cfg.Fetch.ReleaseSource.AssetURL != "" {
			return cfg.Fetch.ReleaseSource.AssetURL
		}
	}
	return cfg.Fetch.Repository
}
