package release

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"

	"github.com/zapstore/zsp/internal/identity"
	internalnostr "github.com/zapstore/zsp/internal/nostr"
)

type fakeProofSigner struct {
	key       *ecdsa.PrivateKey
	cert      *x509.Certificate
	pubkey    string
	createdAt int64
	expiry    int64
}

func (f *fakeProofSigner) SignProof(_ context.Context, createdAt, expiry int64) (ProofSignature, error) {
	f.createdAt, f.expiry = createdAt, expiry
	message := fmt.Sprintf("Verifying at %d until %d that I control the following Nostr public key: %s", createdAt, expiry, f.pubkey)
	sum := sha256.Sum256([]byte(message))
	sig, err := ecdsa.SignASN1(rand.Reader, f.key, sum[:])
	if err != nil {
		return ProofSignature{}, err
	}
	return ProofSignature{PublicKey: f.pubkey, CertificateHash: identity.ComputeCertHash(f.cert), Signature: sig}, nil
}

func proofFixture(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate, internalnostr.Signer) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "zsp-proof-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	nsec, err := nip19.EncodePrivateKey(gonostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	signer, err := internalnostr.NewSigner(t.Context(), nsec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { signer.Close() })
	return key, certificate, signer
}

func TestBuildC1ProofCreatesActiveProof(t *testing.T) {
	key, certificate, signer := proofFixture(t)
	fake := &fakeProofSigner{key: key, cert: certificate, pubkey: signer.PublicKey()}
	now := time.Now()
	event, err := buildC1Proof(t.Context(), fake, signer, certificate, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if fake.createdAt != now.Unix() || fake.expiry != now.Add(identity.DefaultExpiry).Unix() {
		t.Fatalf("window %d..%d", fake.createdAt, fake.expiry)
	}
	if active := activeC1Proof([]*gonostr.Event{event}, certificate, now); active != event {
		t.Fatal("built proof is not active")
	}
	if event.Tags.GetFirst([]string{"delegation"}) != nil {
		t.Fatal("new proof has a delegation")
	}
	if event.Tags.GetFirst([]string{"cert"}) != nil {
		t.Fatal("proof embeds the certificate")
	}
}

func TestPendingC1PublishersKeepsOwnDelegate(t *testing.T) {
	signer, other, delegate := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("d", 64)
	tags := gonostr.Tags{{"delegation", delegate}}
	tests := []struct {
		name     string
		previous *gonostr.Event
		want     []string
	}{
		{"new proof", nil, []string{signer}},
		{"own renewal", &gonostr.Event{PubKey: signer, Tags: tags}, []string{signer, delegate}},
		{"other owner", &gonostr.Event{PubKey: other, Tags: tags}, []string{signer}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pendingC1Publishers(signer, tc.previous)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for _, key := range tc.want {
				if _, ok := got[key]; !ok {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestBuildC1ProofRenewalKeepsDelegation(t *testing.T) {
	key, certificate, signer := proofFixture(t)
	fake := &fakeProofSigner{key: key, cert: certificate, pubkey: signer.PublicKey()}
	delegate := strings.Repeat("d", 64)
	previous := &gonostr.Event{PubKey: signer.PublicKey(), Tags: gonostr.Tags{{"delegation", delegate}}}
	event, err := buildC1Proof(t.Context(), fake, signer, certificate, previous, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if tag := event.Tags.GetFirst([]string{"delegation"}); tag == nil || (*tag)[1] != delegate {
		t.Fatalf("delegation = %v", tag)
	}
}

func TestBuildC1ProofRejectsMismatch(t *testing.T) {
	key, certificate, signer := proofFixture(t)
	_, otherCertificate, _ := proofFixture(t)
	tests := []struct {
		name string
		fake *fakeProofSigner
	}{
		{"other nostr key", &fakeProofSigner{key: key, cert: certificate, pubkey: strings.Repeat("a", 64)}},
		{"other certificate", &fakeProofSigner{key: key, cert: otherCertificate, pubkey: signer.PublicKey()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildC1Proof(t.Context(), tc.fake, signer, certificate, nil, time.Now())
			if !errors.Is(err, ErrSigner) {
				t.Fatalf("err = %v, want ErrSigner", err)
			}
		})
	}
}

func TestC1ProofNeedsRenewal(t *testing.T) {
	now := time.Now()
	owner := strings.Repeat("a", 64)
	proofEvent := func(pubkey string, expiry time.Time) *gonostr.Event {
		proof := &identity.IdentityProof{CertHash: strings.Repeat("c", 64), Signature: "AA==", CreatedAt: now.Unix() - 10, Expiry: expiry.Unix()}
		return &gonostr.Event{Kind: 30509, PubKey: pubkey, CreatedAt: gonostr.Timestamp(proof.CreatedAt), Tags: proof.ToEventTags()}
	}
	tests := []struct {
		name   string
		active *gonostr.Event
		want   bool
	}{
		{"no proof", nil, true},
		{"own proof far from expiry", proofEvent(owner, now.Add(365*24*time.Hour)), false},
		{"own proof expiring soon", proofEvent(owner, now.Add(10*24*time.Hour)), true},
		{"other key's proof expiring soon", proofEvent(strings.Repeat("b", 64), now.Add(time.Hour)), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := c1ProofNeedsRenewal(tc.active, owner, now); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
