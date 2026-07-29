package workflow

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/cli"
	znostr "github.com/zapstore/zsp/internal/nostr"
)

type testSigner struct {
	signerType znostr.SignerType
}

func (s testSigner) Type() znostr.SignerType                  { return s.signerType }
func (testSigner) PublicKey() string                          { return "" }
func (testSigner) Sign(context.Context, *gonostr.Event) error { return nil }
func (testSigner) Close() error                               { return nil }

func TestOutputIndexerAppID(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	OutputIndexerAppID("dev.zapstore.app")
	_ = w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := "{\"app_id\":\"dev.zapstore.app\"}\n"
	if got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestPublisherValidateIndexerModeSigner(t *testing.T) {
	tests := []struct {
		name       string
		ci         bool
		signerType znostr.SignerType
		wantErr    bool
	}{
		{name: "non CI npub", signerType: znostr.SignerNpub},
		{name: "CI nsec", ci: true, signerType: znostr.SignerNsec},
		{name: "CI npub", ci: true, signerType: znostr.SignerNpub, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publisher := &Publisher{
				opts:   &cli.Options{Publish: cli.PublishOptions{IndexerMode: tt.ci}},
				signer: testSigner{signerType: tt.signerType},
			}
			err := publisher.validateIndexerModeSigner()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateIndexerModeSigner() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPublisherOutputPublishResultIndexerMode(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Stdout = old
		_ = r.Close()
		_ = w.Close()
	})
	os.Stdout = w

	publisher := &Publisher{
		opts:    &cli.Options{Publish: cli.PublishOptions{IndexerMode: true}},
		apkInfo: &apk.APKInfo{PackageID: "dev.zapstore.app"},
	}
	publisher.outputPublishResult()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "{\"app_id\":\"dev.zapstore.app\"}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
