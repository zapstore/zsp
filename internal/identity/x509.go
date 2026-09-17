// Package identity handles cryptographic identity proofs (NIP-C1).
package identity

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	keystore "github.com/pavlo-v-chernykh/keystore-go/v4"
	"software.sslmate.com/src/go-pkcs12"
)

// DefaultExpiry is the default validity period for identity proofs.
const DefaultExpiry = 2 * 365 * 24 * time.Hour // 2 years

// JKS magic bytes: 0xFEEDFEED
var jksMagic = []byte{0xFE, 0xED, 0xFE, 0xED}

// ErrJKSFormat is returned when a Java KeyStore is detected.
var ErrJKSFormat = errors.New("java keystore (JKS) format detected")

// ErrJKSKeyAliasRequired is returned when a JKS contains multiple private keys.
var ErrJKSKeyAliasRequired = errors.New("JKS contains multiple private-key entries; specify a key alias")

// ErrKeyCertMismatch is returned when a private key does not correspond to a certificate.
var ErrKeyCertMismatch = errors.New("private key does not match certificate")

// ErrInvalidPassword is returned when a keystore password does not open the store
// or decrypt its private-key entry.
var ErrInvalidPassword = errors.New("keystore password is incorrect")

// IdentityProof contains the NIP-C1 cryptographic identity components.
type IdentityProof struct {
	CertHash  string // SHA-256 hash of DER-encoded certificate, lowercase hex
	Signature string // Base64 signature
	CreatedAt int64  // Unix timestamp when proof was created (must match event's created_at)
	Expiry    int64  // Unix timestamp when proof expires
}

// IdentityProofOptions contains options for generating an identity proof.
type IdentityProofOptions struct {
	Expiry time.Duration // How long the proof should be valid (default: 2 years)
}

// GenerateIdentityProof creates a NIP-C1 cryptographic identity proof.
// The private key must correspond to cert; the cert hash is derived from cert.
// The pubkeyHex must be the 64-character lowercase hex Nostr public key.
// Before returning, the signature is verified against cert so a mismatched
// key/cert pair can never produce a publishable proof.
func GenerateIdentityProof(privateKey crypto.PrivateKey, cert *x509.Certificate, pubkeyHex string, opts *IdentityProofOptions) (*IdentityProof, error) {
	if cert == nil {
		return nil, fmt.Errorf("certificate is required")
	}
	if err := ValidateKeyCertPair(privateKey, cert); err != nil {
		return nil, err
	}

	if opts == nil {
		opts = &IdentityProofOptions{}
	}
	if opts.Expiry == 0 {
		opts.Expiry = DefaultExpiry
	}

	certHash := ComputeCertHash(cert)
	createdAt := time.Now().Unix()
	expiry := createdAt + int64(opts.Expiry.Seconds())

	message := fmt.Sprintf("Verifying at %d until %d that I control the following Nostr public key: %s", createdAt, expiry, pubkeyHex)

	var signature []byte
	var err error
	switch key := privateKey.(type) {
	case *ecdsa.PrivateKey:
		messageHash := sha256.Sum256([]byte(message))
		signature, err = ecdsa.SignASN1(rand.Reader, key, messageHash[:])
	case *rsa.PrivateKey:
		messageHash := sha256.Sum256([]byte(message))
		signature, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, messageHash[:])
	default:
		return nil, fmt.Errorf("unsupported key type: %T", privateKey)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to sign: %w", err)
	}

	proof := &IdentityProof{
		CertHash:  certHash,
		Signature: base64.StdEncoding.EncodeToString(signature),
		CreatedAt: createdAt,
		Expiry:    expiry,
	}

	// Defense in depth: refuse to return a proof the client cannot verify.
	result := VerifyIdentityProofWithCert(proof, nil, pubkeyHex, cert)
	if !result.Valid {
		if result.Error != nil {
			return nil, fmt.Errorf("generated identity proof failed self-verification: %w", result.Error)
		}
		return nil, fmt.Errorf("generated identity proof failed self-verification: signature does not verify against certificate")
	}

	return proof, nil
}

// ValidateKeyCertPair checks that privateKey is the private half of cert's public key.
func ValidateKeyCertPair(privateKey crypto.PrivateKey, cert *x509.Certificate) error {
	if privateKey == nil {
		return fmt.Errorf("private key is required")
	}
	if cert == nil {
		return fmt.Errorf("certificate is required")
	}
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return fmt.Errorf("unsupported private key type: %T", privateKey)
	}
	if !publicKeysEqual(signer.Public(), cert.PublicKey) {
		return fmt.Errorf("%w: wrong keystore alias or mixed-up PEM files", ErrKeyCertMismatch)
	}
	return nil
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	switch ak := a.(type) {
	case *rsa.PublicKey:
		bk, ok := b.(*rsa.PublicKey)
		return ok && ak.Equal(bk)
	case *ecdsa.PublicKey:
		bk, ok := b.(*ecdsa.PublicKey)
		return ok && ak.Equal(bk)
	case ed25519.PublicKey:
		bk, ok := b.(ed25519.PublicKey)
		return ok && ak.Equal(bk)
	default:
		return false
	}
}

// ToEventTags returns the NIP-C1 tags for a kind 30509 event.
func (p *IdentityProof) ToEventTags() nostr.Tags {
	return nostr.Tags{
		{"d", p.CertHash},
		{"signature", p.Signature},
		{"expiry", strconv.FormatInt(p.Expiry, 10)},
	}
}

// CreatedAtTime returns the creation timestamp as a time.Time.
func (p *IdentityProof) CreatedAtTime() time.Time {
	return time.Unix(p.CreatedAt, 0)
}

// ExpiryTime returns the expiry as a time.Time.
func (p *IdentityProof) ExpiryTime() time.Time {
	return time.Unix(p.Expiry, 0)
}

// IsExpired returns true if the proof has expired.
func (p *IdentityProof) IsExpired() bool {
	return time.Now().Unix() >= p.Expiry
}

// VerificationResult contains the result of verifying an identity proof.
type VerificationResult struct {
	Valid         bool      // Whether the signature is valid
	Expired       bool      // Whether the proof has expired
	Revoked       bool      // Whether the proof has been revoked
	RevokeReason  string    // Revocation reason if revoked
	CertHashMatch bool      // Whether cert hash matches certificate (only set with cert verification)
	CertHash      string    // Certificate hash from proof
	ExpiryTime    time.Time // When the proof expires
	Error         error     // Any error encountered
}

// ParseIdentityProofFromEvent parses a kind 30509 event into an IdentityProof.
func ParseIdentityProofFromEvent(event *nostr.Event) (*IdentityProof, error) {
	if event == nil {
		return nil, fmt.Errorf("identity proof event is required")
	}
	if event.Kind != 30509 {
		return nil, fmt.Errorf("invalid event kind: expected 30509, got %d", event.Kind)
	}

	certHash, err := singletonTagValue(event, "d", true)
	if err != nil {
		return nil, err
	}
	if !isLowerHex(certHash, sha256.Size*2) {
		return nil, fmt.Errorf("invalid d tag (certificate hash)")
	}

	signature, err := singletonTagValue(event, "signature", true)
	if err != nil {
		return nil, err
	}
	decodedSignature, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || len(decodedSignature) == 0 || base64.StdEncoding.EncodeToString(decodedSignature) != signature {
		return nil, fmt.Errorf("invalid signature tag")
	}

	expiryValue, err := singletonTagValue(event, "expiry", true)
	if err != nil {
		return nil, err
	}
	expiry, err := strconv.ParseInt(expiryValue, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid expiry timestamp: %w", err)
	}
	if strconv.FormatInt(expiry, 10) != expiryValue {
		return nil, fmt.Errorf("invalid expiry timestamp")
	}
	if _, err := singletonTagValue(event, "cert", false); err != nil {
		return nil, err
	}
	delegation, err := singletonTagValue(event, "delegation", false)
	if err != nil {
		return nil, err
	}
	if delegation != "" && !isLowerHex(delegation, 64) {
		return nil, fmt.Errorf("invalid delegation tag")
	}

	return &IdentityProof{
		CertHash:  certHash,
		Signature: signature,
		CreatedAt: int64(event.CreatedAt),
		Expiry:    expiry,
	}, nil
}

// ValidateActiveProofEvent verifies that event is a current, valid C1 proof
// for certificate at now.
func ValidateActiveProofEvent(event *nostr.Event, certificate *x509.Certificate, now time.Time) (*IdentityProof, error) {
	if event == nil || certificate == nil {
		return nil, fmt.Errorf("identity proof event and certificate are required")
	}
	if event.Content != "" {
		return nil, fmt.Errorf("identity proof content must be empty")
	}
	if !isLowerHex(event.PubKey, 64) {
		return nil, fmt.Errorf("invalid identity proof pubkey")
	}
	valid, err := event.CheckSignature()
	if err != nil || !valid {
		return nil, fmt.Errorf("invalid identity proof event signature")
	}
	proof, err := ParseIdentityProofFromEvent(event)
	if err != nil {
		return nil, err
	}
	if proof.CertHash != ComputeCertHash(certificate) {
		return nil, fmt.Errorf("identity proof certificate does not match")
	}
	if proof.Expiry <= int64(event.CreatedAt) {
		return nil, fmt.Errorf("expiry must be greater than created_at")
	}
	if now.Unix() >= proof.Expiry {
		return nil, fmt.Errorf("identity proof has expired")
	}
	revoked, _ := IsRevoked(event)
	if revoked {
		return nil, fmt.Errorf("identity proof is revoked")
	}
	if encodedCert, err := singletonTagValue(event, "cert", false); err != nil {
		return nil, err
	} else if encodedCert != "" {
		der, decodeErr := base64.StdEncoding.DecodeString(encodedCert)
		if decodeErr != nil || base64.StdEncoding.EncodeToString(der) != encodedCert {
			return nil, fmt.Errorf("invalid cert tag")
		}
		embedded, parseErr := x509.ParseCertificate(der)
		if parseErr != nil || !bytes.Equal(embedded.Raw, certificate.Raw) {
			return nil, fmt.Errorf("identity proof certificate does not match")
		}
	}
	verification := VerifyIdentityProofWithCert(proof, event, event.PubKey, certificate)
	if !verification.Valid || verification.Error != nil {
		return nil, fmt.Errorf("invalid certificate proof signature")
	}
	return proof, nil
}

func singletonTagValue(event *nostr.Event, name string, required bool) (string, error) {
	var value string
	found := false
	for _, tag := range event.Tags {
		if len(tag) == 0 || tag[0] != name {
			continue
		}
		if len(tag) != 2 || found || tag[1] == "" {
			return "", fmt.Errorf("identity proof must contain at most one valid %s tag", name)
		}
		found = true
		value = tag[1]
	}
	if required && !found {
		return "", fmt.Errorf("identity proof is missing %s tag", name)
	}
	return value, nil
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

// IsRevoked checks if a kind 30509 event has been revoked.
func IsRevoked(event *nostr.Event) (bool, string) {
	revokedTag := event.Tags.GetFirst([]string{"revoked"})
	if revokedTag == nil {
		return false, ""
	}
	reason := ""
	if len(*revokedTag) >= 2 {
		reason = (*revokedTag)[1]
	}
	return true, reason
}

// VerifyIdentityProof verifies a cryptographic identity proof against a hex pubkey.
// This performs signature verification only (no certificate comparison).
func VerifyIdentityProof(proof *IdentityProof, event *nostr.Event, pubkeyHex string) *VerificationResult {
	return verifyProofSignature(proof, event, pubkeyHex, nil)
}

// VerifyIdentityProofWithCert verifies an identity proof against a pubkey and certificate.
// This performs full verification: cert hash match and signature.
func VerifyIdentityProofWithCert(proof *IdentityProof, event *nostr.Event, pubkeyHex string, cert *x509.Certificate) *VerificationResult {
	return verifyProofSignature(proof, event, pubkeyHex, cert)
}

// verifyProofSignature performs the actual verification.
func verifyProofSignature(proof *IdentityProof, event *nostr.Event, pubkeyHex string, cert *x509.Certificate) *VerificationResult {
	result := &VerificationResult{
		CertHash:   proof.CertHash,
		ExpiryTime: proof.ExpiryTime(),
		Expired:    proof.IsExpired(),
	}

	if event != nil {
		revoked, reason := IsRevoked(event)
		if revoked {
			result.Revoked = true
			result.RevokeReason = reason
		}

		if proof.Expiry <= int64(event.CreatedAt) {
			result.Error = fmt.Errorf("expiry must be greater than created_at")
			return result
		}
	}

	if cert != nil {
		certHash := ComputeCertHash(cert)
		result.CertHashMatch = (certHash == proof.CertHash)
	}

	signature, err := base64.StdEncoding.DecodeString(proof.Signature)
	if err != nil {
		result.Error = fmt.Errorf("failed to decode signature: %w", err)
		return result
	}

	var pubKeyInterface crypto.PublicKey
	if cert != nil {
		pubKeyInterface = cert.PublicKey
	} else {
		result.Error = fmt.Errorf("certificate required for signature verification")
		return result
	}

	message := fmt.Sprintf("Verifying at %d until %d that I control the following Nostr public key: %s", proof.CreatedAt, proof.Expiry, pubkeyHex)

	switch pubKey := pubKeyInterface.(type) {
	case *ecdsa.PublicKey:
		messageHash := sha256.Sum256([]byte(message))
		if ecdsa.VerifyASN1(pubKey, messageHash[:], signature) {
			result.Valid = true
		} else {
			result.Error = fmt.Errorf("ECDSA signature verification failed")
		}
	case *rsa.PublicKey:
		messageHash := sha256.Sum256([]byte(message))
		err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, messageHash[:], signature)
		if err != nil {
			result.Error = fmt.Errorf("RSA signature verification failed: %w", err)
		} else {
			result.Valid = true
		}
	default:
		result.Error = fmt.Errorf("unsupported public key type: %T", pubKeyInterface)
	}

	return result
}

// ComputeCertHash computes the SHA-256 hash of the DER-encoded certificate.
func ComputeCertHash(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(h[:])
}

// detectJKS checks if data starts with JKS magic bytes.
func detectJKS(data []byte) bool {
	return len(data) >= 4 && bytes.Equal(data[:4], jksMagic)
}

// IsJKS reports whether data has the Java KeyStore magic bytes.
func IsJKS(data []byte) bool {
	return detectJKS(data)
}

// LoadPKCS12 loads a private key and certificate from PKCS12 data.
// Security: The password is zeroed after use to minimize exposure in memory.
func LoadPKCS12(data []byte, password string) (crypto.PrivateKey, *x509.Certificate, error) {
	// Check for JKS format first
	if detectJKS(data) {
		return nil, nil, ErrJKSFormat
	}

	privateKey, cert, err := pkcs12.Decode(data, password)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse PKCS12: %w", wrapKeystorePasswordError(err))
	}
	return privateKey, cert, nil
}

// LoadPKCS12WithSecurePassword loads a private key and certificate from PKCS12 data.
// The password byte slice is zeroed after use for security.
func LoadPKCS12WithSecurePassword(data []byte, password []byte) (crypto.PrivateKey, *x509.Certificate, error) {
	// Zero the password when done
	defer zeroBytes(password)

	// Check for JKS format first
	if detectJKS(data) {
		return nil, nil, ErrJKSFormat
	}

	privateKey, cert, err := pkcs12.Decode(data, string(password))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse PKCS12: %w", wrapKeystorePasswordError(err))
	}
	return privateKey, cert, nil
}

// zeroBytes zeroes a byte slice to clear sensitive data from memory.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func wrapKeystorePasswordError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pkcs12.ErrIncorrectPassword) || errors.Is(err, pkcs12.ErrDecryption) || strings.Contains(err.Error(), "got invalid digest") {
		return fmt.Errorf("%w", ErrInvalidPassword)
	}
	return err
}

// LoadPKCS12File loads a private key and certificate from a PKCS12 file.
// Security: The password is zeroed after use.
func LoadPKCS12File(path, password string) (crypto.PrivateKey, *x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read keystore file: %w", err)
	}
	// Convert to bytes and use secure version that zeros after use
	passwordBytes := []byte(password)
	return LoadPKCS12WithSecurePassword(data, passwordBytes)
}

// JKSKeyAliasRequiredError identifies the private-key aliases available in a JKS.
type JKSKeyAliasRequiredError struct {
	Aliases []string
}

func (e *JKSKeyAliasRequiredError) Error() string {
	return fmt.Sprintf("%v: %s", ErrJKSKeyAliasRequired, strings.Join(e.Aliases, ", "))
}

func (e *JKSKeyAliasRequiredError) Unwrap() error {
	return ErrJKSKeyAliasRequired
}

// LoadJKS loads a private key and its leaf certificate from JKS data.
// When keyPassword is empty, storePassword is used for the private-key entry.
// If alias is empty, the only private-key entry is selected; otherwise callers
// must provide an alias.
func LoadJKS(data, storePassword, keyPassword []byte, alias string) (crypto.PrivateKey, *x509.Certificate, error) {
	defer zeroBytes(storePassword)
	defer zeroBytes(keyPassword)

	store := keystore.New(keystore.WithOrderedAliases())
	if err := store.Load(bytes.NewReader(data), storePassword); err != nil {
		return nil, nil, fmt.Errorf("load JKS: %w", wrapKeystorePasswordError(err))
	}

	var aliases []string
	for _, candidate := range store.Aliases() {
		if store.IsPrivateKeyEntry(candidate) {
			aliases = append(aliases, candidate)
		}
	}
	if len(aliases) == 0 {
		return nil, nil, fmt.Errorf("JKS contains no private-key entries")
	}
	if alias == "" {
		if len(aliases) > 1 {
			return nil, nil, &JKSKeyAliasRequiredError{Aliases: aliases}
		}
		alias = aliases[0]
	}
	if !store.IsPrivateKeyEntry(alias) {
		for _, candidate := range store.Aliases() {
			if candidate == alias {
				return nil, nil, fmt.Errorf("JKS key alias %q is not a private-key entry", alias)
			}
		}
		return nil, nil, fmt.Errorf("JKS key alias %q: %w", alias, keystore.ErrEntryNotFound)
	}

	if len(keyPassword) == 0 {
		keyPassword = storePassword
	}
	entry, err := store.GetPrivateKeyEntry(alias, keyPassword)
	if err != nil {
		return nil, nil, fmt.Errorf("load private key for JKS alias %q: %w", alias, wrapKeystorePasswordError(err))
	}
	if len(entry.CertificateChain) == 0 {
		return nil, nil, fmt.Errorf("JKS key alias %q has no certificate chain", alias)
	}

	privateKey, err := x509.ParsePKCS8PrivateKey(entry.PrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("parse private key for JKS alias %q: %w", alias, err)
	}
	cert, err := x509.ParseCertificate(entry.CertificateChain[0].Content)
	if err != nil {
		return nil, nil, fmt.Errorf("parse certificate for JKS alias %q: %w", alias, err)
	}
	if err := ValidateKeyCertPair(privateKey, cert); err != nil {
		return nil, nil, err
	}
	return privateKey, cert, nil
}

// LoadJKSFile loads a private key and leaf certificate from a JKS file.
func LoadJKSFile(path, storePassword, keyPassword, alias string) (crypto.PrivateKey, *x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read JKS file: %w", err)
	}
	return LoadJKS(data, []byte(storePassword), []byte(keyPassword), alias)
}

// LoadPEM loads a private key and certificate from PEM files.
func LoadPEM(keyPath, certPath string) (crypto.PrivateKey, *x509.Certificate, error) {
	// Load private key
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read key file: %w", err)
	}

	// Find and decode the private key block (skip EC PARAMETERS and other blocks)
	var keyBlock *pem.Block
	remaining := keyData
	for {
		keyBlock, remaining = pem.Decode(remaining)
		if keyBlock == nil {
			return nil, nil, fmt.Errorf("no private key found in PEM file")
		}
		// Look for private key blocks
		if keyBlock.Type == "PRIVATE KEY" ||
			keyBlock.Type == "EC PRIVATE KEY" ||
			keyBlock.Type == "RSA PRIVATE KEY" {
			break
		}
		// Continue scanning if we found something else (like EC PARAMETERS)
	}

	var privateKey crypto.PrivateKey
	privateKey, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		// Try EC private key format
		privateKey, err = x509.ParseECPrivateKey(keyBlock.Bytes)
		if err != nil {
			// Try PKCS1 (RSA)
			privateKey, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to parse private key: %w", err)
			}
		}
	}

	// Load certificate
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read cert file: %w", err)
	}

	certBlock, _ := pem.Decode(certData)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("failed to decode PEM certificate")
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	if err := ValidateKeyCertPair(privateKey, cert); err != nil {
		return nil, nil, err
	}

	return privateKey, cert, nil
}
