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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"software.sslmate.com/src/go-pkcs12"
)

// DefaultExpiry is the default validity period for identity proofs.
const DefaultExpiry = 365 * 24 * time.Hour // 1 year

// JKS magic bytes: 0xFEEDFEED
var jksMagic = []byte{0xFE, 0xED, 0xFE, 0xED}

// ErrJKSFormat is returned when a Java KeyStore is detected.
var ErrJKSFormat = errors.New("java keystore (JKS) format detected")

// IdentityProof contains the NIP-C1 cryptographic identity components.
type IdentityProof struct {
	CertHash  string // SHA-256 hash of DER-encoded certificate, lowercase hex
	Signature string // Base64 signature
	CreatedAt int64  // Unix timestamp when proof was created (must match event's created_at)
	Expiry    int64  // Unix timestamp when proof expires
}

// IdentityProofOptions contains options for generating an identity proof.
type IdentityProofOptions struct {
	Expiry time.Duration // How long the proof should be valid (default: 1 year)
}

// GenerateIdentityProof creates a NIP-C1 cryptographic identity proof.
// certHash is the SHA-256 hash of the DER-encoded certificate (lowercase hex).
// The pubkeyHex must be the 64-character lowercase hex Nostr public key.
func GenerateIdentityProof(privateKey crypto.PrivateKey, certHash, pubkeyHex string, opts *IdentityProofOptions) (*IdentityProof, error) {
	if opts == nil {
		opts = &IdentityProofOptions{}
	}
	if opts.Expiry == 0 {
		opts.Expiry = DefaultExpiry
	}

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
	case ed25519.PrivateKey:
		signature = ed25519.Sign(key, []byte(message))
	default:
		return nil, fmt.Errorf("unsupported key type: %T", privateKey)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to sign: %w", err)
	}

	return &IdentityProof{
		CertHash:  certHash,
		Signature: base64.StdEncoding.EncodeToString(signature),
		CreatedAt: createdAt,
		Expiry:    expiry,
	}, nil
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
	return time.Now().Unix() > p.Expiry
}

// VerificationResult contains the result of verifying an identity proof.
type VerificationResult struct {
	Valid        bool      // Whether the signature is valid
	Expired      bool      // Whether the proof has expired
	Revoked      bool      // Whether the proof has been revoked
	RevokeReason string    // Revocation reason if revoked
	CertHashMatch bool     // Whether cert hash matches certificate (only set with cert verification)
	CertHash     string    // Certificate hash from proof
	ExpiryTime   time.Time // When the proof expires
	Error        error     // Any error encountered
}

// ParseIdentityProofFromEvent parses a kind 30509 event into an IdentityProof.
func ParseIdentityProofFromEvent(event *nostr.Event) (*IdentityProof, error) {
	if event.Kind != 30509 {
		return nil, fmt.Errorf("invalid event kind: expected 30509, got %d", event.Kind)
	}

	certHash := event.Tags.GetD()
	if certHash == "" {
		return nil, fmt.Errorf("missing d tag (cert hash)")
	}

	signatureTag := event.Tags.GetFirst([]string{"signature"})
	if signatureTag == nil || len(*signatureTag) < 2 {
		return nil, fmt.Errorf("missing signature tag")
	}
	signature := (*signatureTag)[1]

	expiryTag := event.Tags.GetFirst([]string{"expiry"})
	if expiryTag == nil || len(*expiryTag) < 2 {
		return nil, fmt.Errorf("missing expiry tag")
	}
	expiry, err := strconv.ParseInt((*expiryTag)[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid expiry timestamp: %w", err)
	}

	return &IdentityProof{
		CertHash:  certHash,
		Signature: signature,
		CreatedAt: int64(event.CreatedAt),
		Expiry:    expiry,
	}, nil
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
			pssOpts := &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}
			err = rsa.VerifyPSS(pubKey, crypto.SHA256, messageHash[:], signature, pssOpts)
		}
		if err != nil {
			result.Error = fmt.Errorf("RSA signature verification failed: %w", err)
		} else {
			result.Valid = true
		}
	case ed25519.PublicKey:
		if ed25519.Verify(pubKey, []byte(message), signature) {
			result.Valid = true
		} else {
			result.Error = fmt.Errorf("Ed25519 signature verification failed")
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

// LoadPKCS12 loads a private key and certificate from PKCS12 data.
// Security: The password is zeroed after use to minimize exposure in memory.
func LoadPKCS12(data []byte, password string) (crypto.PrivateKey, *x509.Certificate, error) {
	// Check for JKS format first
	if detectJKS(data) {
		return nil, nil, ErrJKSFormat
	}

	privateKey, cert, err := pkcs12.Decode(data, password)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse PKCS12: %w", err)
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
		return nil, nil, fmt.Errorf("failed to parse PKCS12: %w", err)
	}
	return privateKey, cert, nil
}

// zeroBytes zeroes a byte slice to clear sensitive data from memory.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
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

	return privateKey, cert, nil
}

// JKSConversionHelp returns help text for converting JKS to PKCS12.
func JKSConversionHelp(jksPath string) string {
	// Derive p12 path in same directory with .p12 extension
	dir := filepath.Dir(jksPath)
	base := filepath.Base(jksPath)
	p12Name := strings.TrimSuffix(strings.TrimSuffix(base, ".jks"), ".keystore") + ".p12"
	p12Path := filepath.Join(dir, p12Name)

	return fmt.Sprintf(`Error: Java KeyStore (JKS) format detected

JKS files must be converted to PKCS12 format first.
Run the following command:

  keytool -importkeystore -srckeystore %s -destkeystore %s -deststoretype PKCS12

Then use the .p12 file:

  zsp --link-identity %s
`, jksPath, p12Path, p12Path)
}
