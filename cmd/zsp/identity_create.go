package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/zapstore/zsp/internal/config"
	"github.com/zapstore/zsp/internal/identity"
	nostrpkg "github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/ui"
	"golang.org/x/term"
)

// isTTY reports whether stdin is an interactive terminal. It is a seam so
// tests can force the non-interactive path deterministically.
var isTTY = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

type c1RelayPublishError struct {
	CertificateHash string
	EventID         string
	Results         []nostrpkg.PublishResult
}

type proofOptions struct {
	Keystore       string
	Delegate       string
	Expiry         string
	PrivateKey     string
	KeyAlias       string
	NonInteractive bool
}

func (e *c1RelayPublishError) Error() string {
	return fmt.Sprintf("C1 proof was not accepted by any relay (%d attempted)", len(e.Results))
}

func (e *c1RelayPublishError) Diagnostics() []map[string]any {
	diagnostics := make([]map[string]any, 0, len(e.Results))
	for _, result := range e.Results {
		message := "relay did not accept the event"
		if result.Error != nil {
			message = safeRelayDiagnostic(result.Error)
		}
		diagnostics = append(diagnostics, map[string]any{
			"relay_url": sanitizeRelayURL(result.RelayURL),
			"accepted":  result.Success,
			"duplicate": result.IsDuplicate,
			"message":   message,
		})
	}
	return diagnostics
}

func safeRelayDiagnostic(err error) string {
	message := ui.SanitizeErrorMessage(err)
	if message == "" {
		return "relay did not accept the event"
	}
	return message
}

func sanitizeRelayURL(value string) string {
	parsed, err := url.Parse(value)
	if err == nil && parsed.Host != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	if before, _, ok := strings.Cut(value, "?"); ok {
		value = before
	}
	if before, _, ok := strings.Cut(value, "#"); ok {
		value = before
	}
	return value
}

type publishedC1Proof struct {
	Proof *identity.IdentityProof
	Event *gonostr.Event
}

// publishC1Proof creates, signs, and sends a C1 event for the wizard proof flow.
func publishC1Proof(ctx context.Context, privateKey crypto.PrivateKey, certificate *x509.Certificate, signer nostrpkg.Signer, expiry time.Duration, delegateValue string) (*publishedC1Proof, error) {
	published, err := createSignedC1Proof(ctx, privateKey, certificate, signer, expiry, delegateValue)
	if err != nil {
		return nil, err
	}
	results := nostrpkg.NewPublisher(relaysFromEnv()).Publish(ctx, published.Event)
	if !anyRelayAccepted(results) {
		return nil, &c1RelayPublishError{
			CertificateHash: published.Proof.CertHash,
			EventID:         published.Event.ID,
			Results:         results,
		}
	}
	return published, nil
}

// createSignedC1Proof builds the proof event and signs it in place.
func createSignedC1Proof(ctx context.Context, privateKey crypto.PrivateKey, certificate *x509.Certificate, signer nostrpkg.Signer, expiry time.Duration, delegateValue string) (*publishedC1Proof, error) {
	proof, err := identity.GenerateIdentityProof(privateKey, certificate, signer.PublicKey(), &identity.IdentityProofOptions{Expiry: expiry})
	if err != nil {
		return nil, fmt.Errorf("create C1 proof: %w", err)
	}
	event := nostrpkg.BuildIdentityProofEvent(proof.ToEventTags(), signer.PublicKey(), proof.CreatedAt)
	if delegateValue != "" {
		delegate, err := parseDelegate(delegateValue)
		if err != nil {
			return nil, err
		}
		event.Tags = append(event.Tags, gonostr.Tag{"delegation", delegate})
	}
	if err := signer.Sign(ctx, event); err != nil {
		return nil, fmt.Errorf("sign C1 proof: %w", err)
	}
	return &publishedC1Proof{Proof: proof, Event: event}, nil
}

// resolveKeystorePath prompts for the certificate keystore when it was not
// supplied. Machine-readable and non-TTY invocations must provide the flag.
func resolveKeystorePath(path string, interactive bool) (string, error) {
	if path = strings.TrimSpace(path); path != "" {
		return path, nil
	}
	if !interactive || !isTTY() {
		return "", fmt.Errorf("--keystore is required")
	}
	return ui.PromptPath("Keystore or certificate", "Path to the signing keystore or PEM certificate. Tab completes files.", true)
}

// loadIdentityMaterial loads a private key and certificate for the wizard
// proof flow from a keystore or PEM certificate/key pair. JKS (.jks,
// .keystore), PKCS#12 (.p12, .pfx), and PEM (.pem, .crt, .cer) inputs are
// supported. Missing aliases, PEM keys, or passwords are prompted for on a
// TTY; non-TTY use reports the missing value.
func loadIdentityMaterial(options proofOptions) (crypto.PrivateKey, *x509.Certificate, error) {
	if options.PrivateKey != "" {
		return loadPEMIdentity(options.Keystore, options.PrivateKey)
	}

	lower := strings.ToLower(options.Keystore)
	switch {
	case strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".crt") || strings.HasSuffix(lower, ".cer"):
		keyPath, err := resolvePEMKeyPath(options.Keystore, !options.NonInteractive)
		if err != nil {
			return nil, nil, err
		}
		return loadPEMIdentity(options.Keystore, keyPath)
	case strings.HasSuffix(lower, ".jks") || strings.HasSuffix(lower, ".keystore"):
		return loadJKSIdentity(options.Keystore, options.KeyAlias, !options.NonInteractive)
	case strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".pfx"):
		return loadPKCS12Identity(options.Keystore, options.KeyAlias, !options.NonInteractive)
	default:
		data, err := os.ReadFile(options.Keystore)
		if err != nil {
			return nil, nil, fmt.Errorf("read keystore file: %w", err)
		}
		if identity.IsJKS(data) {
			return loadJKSIdentityFromData(data, options.Keystore, options.KeyAlias, !options.NonInteractive)
		}
		return nil, nil, fmt.Errorf("unsupported keystore type: %s (use .jks, .keystore, .p12, .pfx, .pem, or .crt)", options.Keystore)
	}
}

// loadCertificateMaterial reads certificate-only PEM input so the wizard can
// discover an existing proof without requesting private signing material.
func loadCertificateMaterial(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read certificate: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("a PEM certificate is required to check an existing proof")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return certificate, nil
}

// loadPEMIdentity loads a private key and certificate from separate PEM files.
func loadPEMIdentity(certPath, keyPath string) (crypto.PrivateKey, *x509.Certificate, error) {
	privateKey, cert, err := identity.LoadPEM(keyPath, certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load PEM identity material: %w", err)
	}
	return privateKey, cert, nil
}

// resolvePEMKeyPath prompts for the private key path paired with a PEM
// certificate when --private-key was not supplied. Non-TTY use reports the
// missing flag instead of prompting.
func resolvePEMKeyPath(certPath string, interactive bool) (string, error) {
	if !interactive || !isTTY() {
		return "", fmt.Errorf("--private-key is required for a PEM certificate")
	}
	return ui.PromptPath("Private key path", "PEM private key paired with "+certPath+". Tab completes files.", true)
}

// loadJKSIdentity loads a private key and certificate from a JKS keystore file.
func loadJKSIdentity(path, alias string, interactive bool) (crypto.PrivateKey, *x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read keystore file: %w", err)
	}
	return loadJKSIdentityFromData(data, path, alias, interactive)
}

// loadJKSIdentityFromData loads a private key and certificate from JKS
// bytes, resolving the store password from KEYSTORE_PASSWORD (or a TTY
// prompt) and the key alias from --key-alias (or a TTY selection) when the
// keystore contains multiple private-key entries.
func loadJKSIdentityFromData(data []byte, path, alias string, interactive bool) (crypto.PrivateKey, *x509.Certificate, error) {
	useSavedPassword := true
	for {
		storePassword, err := resolveKeystorePassword(path, interactive, !useSavedPassword)
		if err != nil {
			return nil, nil, err
		}
		keyPassword := ""
		if useSavedPassword {
			keyPassword = config.GetKeystoreKeyPassword()
		}
		privateKey, cert, err := identity.LoadJKS(data, []byte(storePassword), []byte(keyPassword), alias)
		var aliasErr *identity.JKSKeyAliasRequiredError
		if err != nil && alias == "" && errors.As(err, &aliasErr) {
			alias, err = resolveKeyAlias(aliasErr.Aliases, interactive)
			if err != nil {
				return nil, nil, err
			}
			privateKey, cert, err = identity.LoadJKS(data, []byte(storePassword), []byte(keyPassword), alias)
		}
		if err == nil {
			return privateKey, cert, nil
		}
		if interactive && isTTY() && errors.Is(err, identity.ErrInvalidPassword) {
			ui.PrintInfo("That password is incorrect. Try again.")
			useSavedPassword = false
			continue
		}
		return nil, nil, fmt.Errorf("load JKS keystore: %w", err)
	}
}

// loadPKCS12Identity loads a private key and certificate from a PKCS#12
// keystore file, resolving the password from KEYSTORE_PASSWORD or a TTY
// prompt. Falls back to JKS loading when the file is actually JKS-formatted.
func loadPKCS12Identity(path, alias string, interactive bool) (crypto.PrivateKey, *x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read keystore file: %w", err)
	}
	useSavedPassword := true
	for {
		password, err := resolveKeystorePassword(path, interactive, !useSavedPassword)
		if err != nil {
			return nil, nil, err
		}
		privateKey, cert, err := identity.LoadPKCS12WithSecurePassword(data, []byte(password))
		if err == nil {
			return privateKey, cert, nil
		}
		if errors.Is(err, identity.ErrJKSFormat) {
			return loadJKSIdentityFromData(data, path, alias, interactive)
		}
		if interactive && isTTY() && errors.Is(err, identity.ErrInvalidPassword) {
			ui.PrintInfo("That password is incorrect. Try again.")
			useSavedPassword = false
			continue
		}
		return nil, nil, fmt.Errorf("load PKCS12 keystore: %w", err)
	}
}

// resolveKeystorePassword resolves a keystore password from KEYSTORE_PASSWORD
// (checked via the process environment, then .env in the current working
// directory) or, on a TTY, an interactive masked prompt. Non-TTY use reports
// the missing environment variable.
func resolveKeystorePassword(path string, interactive, promptOnly bool) (string, error) {
	if !promptOnly {
		if password := config.GetKeystorePassword(); password != "" {
			return password, nil
		}
	}
	if !interactive || !isTTY() {
		return "", fmt.Errorf("KEYSTORE_PASSWORD is required for %s", path)
	}
	return promptKeystorePassword(path)
}

var promptKeystorePassword = func(path string) (string, error) {
	return promptRequired("Keystore password", "Password for "+path+".", true)
}

// resolveKeyAlias prompts for one of the given JKS private-key aliases when
// --key-alias was not supplied. Non-TTY use reports the missing flag and the
// available aliases instead of prompting.
func resolveKeyAlias(aliases []string, interactive bool) (string, error) {
	if !interactive || !isTTY() {
		return "", fmt.Errorf("--key-alias is required: choose one of %s", strings.Join(aliases, ", "))
	}
	return ui.SelectField("Key alias", "This keystore has multiple private-key entries; choose one.", aliases)
}

// promptRequired runs a single-field huh form and returns the trimmed,
// required value. Secrets are masked with EchoModePassword.
func promptRequired(title, description string, secret bool) (string, error) {
	return ui.PromptField(title, description, secret, true)
}

func requiredValue(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("required")
	}
	return nil
}

// parseDelegate normalizes --delegate to a lowercase hexadecimal public key,
// accepting either an npub or hex input.
func parseDelegate(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "npub1") {
		prefix, decoded, err := nip19.Decode(value)
		if err != nil || prefix != "npub" {
			return "", fmt.Errorf("invalid --delegate npub")
		}
		publicKey, ok := decoded.(string)
		if !ok {
			return "", fmt.Errorf("invalid --delegate npub")
		}
		value = publicKey
	}
	if len(value) != 64 || strings.ToLower(value) != value {
		return "", fmt.Errorf("--delegate must be an npub or lowercase hexadecimal public key")
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return "", fmt.Errorf("--delegate must be an npub or lowercase hexadecimal public key")
		}
	}
	return value, nil
}

// relaysFromEnv returns the comma-separated relay URLs from RELAYS
// (process environment, then .env in the current working directory),
// defaulting to nostrpkg.DefaultRelay.
func relaysFromEnv() []string {
	value := config.GetEnv("RELAYS")
	if value == "" {
		return []string{nostrpkg.DefaultRelay}
	}
	var relays []string
	for _, relay := range strings.Split(value, ",") {
		if relay = strings.TrimSpace(relay); relay != "" {
			relays = append(relays, relay)
		}
	}
	return relays
}

// anyRelayAccepted reports whether at least one relay accepted the proof.
func anyRelayAccepted(results []nostrpkg.PublishResult) bool {
	for _, result := range results {
		if result.Success {
			return true
		}
	}
	return false
}

func renderC1RelayFailure(failure *c1RelayPublishError) string {
	var body strings.Builder
	body.WriteString(ui.RenderPanel("error", "C1 proof was not accepted by any relay", []ui.KeyValue{
		{Key: "Certificate hash", Value: failure.CertificateHash},
		{Key: "Event id", Value: failure.EventID},
	}, []string{"The proof was signed, but no configured relay confirmed it."}))
	body.WriteString("\n" + ui.StatusLine("info", "Relay results"))
	for _, result := range failure.Results {
		kind := "error"
		if result.Success {
			kind = "success"
		}
		message := "no acceptance response"
		if result.Error != nil {
			message = safeRelayDiagnostic(result.Error)
		}
		body.WriteString("\n" + ui.StatusLine(kind, sanitizeRelayURL(result.RelayURL)+": "+message))
	}
	body.WriteString("\n" + ui.StatusLine("info", "Check RELAYS, network access, and relay policy, then retry"))
	return body.String()
}

// identitySource resolves the app source to submit to the indexer. --source
// is used when supplied; otherwise a TTY prompts for an optional value.
// Non-TTY use returns an empty source without prompting or failing.
func identitySource(source string) (string, error) {
	if source != "" {
		return validateSuggestionSource(source)
	}
	if !isTTY() {
		return "", nil
	}
	value, err := ui.PromptField("App source", "Repository or APK release URL to submit for indexer-managed updates. Leave blank to submit later.", false, false)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", nil
	}
	return validateSuggestionSource(value)
}

func validateSuggestionSource(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("--source must be an HTTPS repository or release URL")
	}
	if err := config.ValidateURL(value); err != nil {
		return "", fmt.Errorf("--source must be an HTTPS repository or release URL: %w", err)
	}
	return value, nil
}

func isRepositorySuggestion(source string) bool {
	parsed, err := url.Parse(source)
	if err != nil {
		return false
	}
	path := strings.ToLower(parsed.Path)
	if strings.HasSuffix(path, ".apk") || strings.Contains(path, "/releases") {
		return false
	}
	switch config.DetectSourceType(source) {
	case config.SourceGitHub, config.SourceGitLab, config.SourceGitea:
		return true
	default:
		return false
	}
}

func isLoopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}
