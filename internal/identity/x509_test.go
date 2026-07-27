package identity

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	keystore "github.com/pavlo-v-chernykh/keystore-go/v4"
)

func TestValidateKeyCertPair(t *testing.T) {
	rsaKey, rsaCert := mustGenerateRSA(t)
	ecKey, ecCert := mustGenerateECDSA(t)
	edKey, edCert := mustGenerateEd25519(t)
	otherRSA, _ := mustGenerateRSA(t)

	tests := []struct {
		name    string
		key     crypto.PrivateKey
		cert    *x509.Certificate
		wantErr error
	}{
		{name: "rsa match", key: rsaKey, cert: rsaCert},
		{name: "ecdsa match", key: ecKey, cert: ecCert},
		{name: "ed25519 match", key: edKey, cert: edCert},
		{name: "rsa mismatch", key: otherRSA, cert: rsaCert, wantErr: ErrKeyCertMismatch},
		{name: "nil key", key: nil, cert: rsaCert, wantErr: nil}, // checked via error string
		{name: "nil cert", key: rsaKey, cert: nil, wantErr: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateKeyCertPair(tt.key, tt.cert)
			switch {
			case tt.name == "nil key" || tt.name == "nil cert":
				if err == nil {
					t.Fatal("expected error for nil input")
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ValidateKeyCertPair() error = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("ValidateKeyCertPair() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestGenerateIdentityProof(t *testing.T) {
	pubkeyHex := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

	rsaKey, rsaCert := mustGenerateRSA(t)
	ecKey, ecCert := mustGenerateECDSA(t)
	edKey, edCert := mustGenerateEd25519(t)
	mismatchKey, _ := mustGenerateRSA(t)
	_, mismatchCert := mustGenerateRSA(t)

	tests := []struct {
		name    string
		key     crypto.PrivateKey
		cert    *x509.Certificate
		wantErr bool
	}{
		{name: "rsa happy path self-verifies", key: rsaKey, cert: rsaCert},
		{name: "ecdsa happy path self-verifies", key: ecKey, cert: ecCert},
		{name: "ed25519 happy path self-verifies", key: edKey, cert: edCert},
		{name: "mismatched rsa key and cert", key: mismatchKey, cert: mismatchCert, wantErr: true},
		{name: "nil certificate", key: rsaKey, cert: nil, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proof, err := GenerateIdentityProof(tt.key, tt.cert, pubkeyHex, &IdentityProofOptions{
				Expiry: time.Hour,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatal("GenerateIdentityProof() expected error")
				}
				if tt.name == "mismatched rsa key and cert" && !errors.Is(err, ErrKeyCertMismatch) {
					t.Fatalf("error = %v, want ErrKeyCertMismatch", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GenerateIdentityProof() unexpected error: %v", err)
			}
			if proof.CertHash != ComputeCertHash(tt.cert) {
				t.Fatalf("CertHash = %s, want %s", proof.CertHash, ComputeCertHash(tt.cert))
			}
			if proof.Signature == "" {
				t.Fatal("Signature is empty")
			}
			if proof.Expiry <= proof.CreatedAt {
				t.Fatalf("Expiry %d must be greater than CreatedAt %d", proof.Expiry, proof.CreatedAt)
			}

			result := VerifyIdentityProofWithCert(proof, nil, pubkeyHex, tt.cert)
			if !result.Valid || result.Error != nil {
				t.Fatalf("self-verify failed: valid=%v err=%v", result.Valid, result.Error)
			}
			if !result.CertHashMatch {
				t.Fatal("CertHashMatch = false")
			}
		})
	}
}

func TestGenerateIdentityProof_DerivesHashFromCert(t *testing.T) {
	key, cert := mustGenerateRSA(t)
	pubkeyHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	proof, err := GenerateIdentityProof(key, cert, pubkeyHex, nil)
	if err != nil {
		t.Fatalf("GenerateIdentityProof() error: %v", err)
	}
	if proof.CertHash != ComputeCertHash(cert) {
		t.Fatalf("proof used unexpected cert hash %s", proof.CertHash)
	}
}

func TestLoadPEM_RejectsMismatchedKey(t *testing.T) {
	key1, cert1 := mustGenerateRSA(t)
	key2, _ := mustGenerateRSA(t)

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pem")
	certPath := filepath.Join(dir, "cert.pem")
	writePEMKey(t, keyPath, key2) // wrong key
	writePEMCert(t, certPath, cert1)

	_, _, err := LoadPEM(keyPath, certPath)
	if !errors.Is(err, ErrKeyCertMismatch) {
		t.Fatalf("LoadPEM() error = %v, want ErrKeyCertMismatch", err)
	}

	writePEMKey(t, keyPath, key1) // matching key
	gotKey, gotCert, err := LoadPEM(keyPath, certPath)
	if err != nil {
		t.Fatalf("LoadPEM() unexpected error: %v", err)
	}
	if err := ValidateKeyCertPair(gotKey, gotCert); err != nil {
		t.Fatalf("loaded pair invalid: %v", err)
	}
}

func TestLoadJKS(t *testing.T) {
	rsaKey, rsaCert := mustGenerateRSA(t)
	ecKey, ecCert := mustGenerateECDSA(t)
	edKey, edCert := mustGenerateEd25519(t)
	otherKey, _ := mustGenerateRSA(t)

	tests := []struct {
		name      string
		entries   []jksTestEntry
		storePass string
		keyPass   string
		loadStore string
		loadKey   string
		alias     string
		wantCert  *x509.Certificate
		wantErr   error
	}{
		{
			name:      "single RSA entry uses default alias",
			entries:   []jksTestEntry{{alias: "release", key: rsaKey, cert: rsaCert}},
			storePass: "storepass",
			loadStore: "storepass",
			wantCert:  rsaCert,
		},
		{
			name:      "ECDSA explicit alias",
			entries:   []jksTestEntry{{alias: "rsa", key: rsaKey, cert: rsaCert}, {alias: "ec", key: ecKey, cert: ecCert}},
			storePass: "storepass",
			loadStore: "storepass",
			alias:     "ec",
			wantCert:  ecCert,
		},
		{
			name:      "Ed25519 separate key password",
			entries:   []jksTestEntry{{alias: "ed", key: edKey, cert: edCert}},
			storePass: "storepass",
			keyPass:   "keypass",
			loadStore: "storepass",
			loadKey:   "keypass",
			wantCert:  edCert,
		},
		{
			name:      "multiple aliases require selection",
			entries:   []jksTestEntry{{alias: "rsa", key: rsaKey, cert: rsaCert}, {alias: "ec", key: ecKey, cert: ecCert}},
			storePass: "storepass",
			loadStore: "storepass",
			wantErr:   ErrJKSKeyAliasRequired,
		},
		{
			name:      "mismatched key and certificate",
			entries:   []jksTestEntry{{alias: "broken", key: otherKey, cert: rsaCert}},
			storePass: "storepass",
			loadStore: "storepass",
			wantErr:   ErrKeyCertMismatch,
		},
		{
			name:      "wrong alias",
			entries:   []jksTestEntry{{alias: "release", key: rsaKey, cert: rsaCert}},
			storePass: "storepass",
			loadStore: "storepass",
			alias:     "missing",
			wantErr:   keystore.ErrEntryNotFound,
		},
		{
			name:      "wrong store password",
			entries:   []jksTestEntry{{alias: "release", key: rsaKey, cert: rsaCert}},
			storePass: "storepass",
			loadStore: "wrongpass",
			wantErr:   nil,
		},
		{
			name:      "wrong key password",
			entries:   []jksTestEntry{{alias: "release", key: rsaKey, cert: rsaCert}},
			storePass: "storepass",
			keyPass:   "keypass",
			loadStore: "storepass",
			loadKey:   "wrongpass",
			wantErr:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := mustEncodeJKS(t, tt.storePass, tt.keyPass, tt.entries...)
			key, cert, err := LoadJKS(data, []byte(tt.loadStore), []byte(tt.loadKey), tt.alias)

			if tt.name == "wrong store password" || tt.name == "wrong key password" {
				if err == nil {
					t.Fatalf("LoadJKS() expected error for %s", tt.name)
				}
				return
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("LoadJKS() error = %v, want %v", err, tt.wantErr)
				}
				if key != nil || cert != nil {
					t.Fatalf("LoadJKS() returned key=%T cert=%v with error", key, cert)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadJKS() unexpected error: %v", err)
			}
			if ComputeCertHash(cert) != ComputeCertHash(tt.wantCert) {
				t.Fatalf("loaded certificate hash = %s, want %s", ComputeCertHash(cert), ComputeCertHash(tt.wantCert))
			}
			if err := ValidateKeyCertPair(key, cert); err != nil {
				t.Fatalf("loaded JKS pair invalid: %v", err)
			}
		})
	}
}

func TestLoadJKS_MalformedData(t *testing.T) {
	_, _, err := LoadJKS([]byte("not a JKS"), []byte("storepass"), nil, "")
	if err == nil {
		t.Fatal("LoadJKS() expected error for malformed data")
	}
}

func TestIsJKS(t *testing.T) {
	if !IsJKS([]byte{0xFE, 0xED, 0xFE, 0xED}) {
		t.Fatal("IsJKS() = false for JKS magic bytes")
	}
	if IsJKS([]byte("not a JKS")) {
		t.Fatal("IsJKS() = true for non-JKS data")
	}
}

func TestLoadJKS_JDKFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "jdk-generated.jks"))
	if err != nil {
		t.Fatalf("ReadFile JKS fixture: %v", err)
	}

	key, cert, err := LoadJKS(data, []byte("fixture-store-password"), []byte("fixture-key-password"), "release")
	if err != nil {
		t.Fatalf("LoadJKS() JDK fixture error: %v", err)
	}
	if _, ok := key.(*rsa.PrivateKey); !ok {
		t.Fatalf("JDK fixture key type = %T, want *rsa.PrivateKey", key)
	}
	if got, want := ComputeCertHash(cert), "787ffed5a9f5518ee973a99e528d5442b1507ce100b08ac041d1b337b3320f79"; got != want {
		t.Fatalf("JDK fixture certificate hash = %s, want %s", got, want)
	}
	if err := ValidateKeyCertPair(key, cert); err != nil {
		t.Fatalf("JDK fixture key/cert pair invalid: %v", err)
	}
}

type jksTestEntry struct {
	alias string
	key   crypto.PrivateKey
	cert  *x509.Certificate
}

func mustEncodeJKS(t *testing.T, storePassword, keyPassword string, entries ...jksTestEntry) []byte {
	t.Helper()
	store := keystore.New(keystore.WithOrderedAliases())
	entryPassword := keyPassword
	if entryPassword == "" {
		entryPassword = storePassword
	}
	for _, entry := range entries {
		privateKeyDER, err := x509.MarshalPKCS8PrivateKey(entry.key)
		if err != nil {
			t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
		}
		err = store.SetPrivateKeyEntry(entry.alias, keystore.PrivateKeyEntry{
			PrivateKey: privateKeyDER,
			CertificateChain: []keystore.Certificate{{
				Type:    "X.509",
				Content: entry.cert.Raw,
			}},
		}, []byte(entryPassword))
		if err != nil {
			t.Fatalf("SetPrivateKeyEntry: %v", err)
		}
	}

	var out bytes.Buffer
	if err := store.Store(&out, []byte(storePassword)); err != nil {
		t.Fatalf("Store: %v", err)
	}
	return out.Bytes()
}

func mustGenerateRSA(t *testing.T) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return key, mustSelfSigned(t, key, &key.PublicKey)
}

func mustGenerateECDSA(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	return key, mustSelfSigned(t, key, &key.PublicKey)
}

func mustGenerateEd25519(t *testing.T) (ed25519.PrivateKey, *x509.Certificate) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return priv, mustSelfSigned(t, priv, pub)
}

func mustSelfSigned(t *testing.T, key crypto.PrivateKey, pub crypto.PublicKey) *x509.Certificate {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "zsp-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func writePEMKey(t *testing.T, path string, key crypto.PrivateKey) {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}
}

func writePEMCert(t *testing.T, path string, cert *x509.Certificate) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}
}
