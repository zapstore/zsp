package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/ui"
)

func TestSelectPublicAPKWrapsSelectionSentinel(t *testing.T) {
	options := &cli.Options{}
	options.Publish.Quiet = true
	_, err := selectPublicAPK(options, []*zsp.APK{
		{Hash: "one", CertificateHash: "certificate-one"},
		{Hash: "two", CertificateHash: "certificate-two"},
	})
	if !errors.Is(err, zsp.ErrAPKSelectionRequired) {
		t.Fatalf("selectPublicAPK error = %v, want ErrAPKSelectionRequired", err)
	}
}

func TestSelectPublicAPKSkipsSelectionForOneCertificate(t *testing.T) {
	options := &cli.Options{}
	options.Publish.Quiet = true
	candidates := []*zsp.APK{
		{Hash: "one", CertificateHash: "certificate"},
		{Hash: "two", CertificateHash: "certificate"},
	}

	got, err := selectPublicAPK(options, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if got != candidates[0] {
		t.Fatalf("selectPublicAPK() = %#v, want first candidate %#v", got, candidates[0])
	}
}

func TestSelectPublicAPKWrapsUncheckedSentinel(t *testing.T) {
	options := &cli.Options{}
	options.Publish.APKHash = "missing"
	_, err := selectPublicAPK(options, []*zsp.APK{{Hash: "present"}})
	if !errors.Is(err, zsp.ErrAPKNotChecked) {
		t.Fatalf("selectPublicAPK error = %v, want ErrAPKNotChecked", err)
	}
}

func TestAPKSelectionChoicesGroupsCandidatesByCertificateHash(t *testing.T) {
	candidates := []*zsp.APK{
		{Hash: "apk-a", CertificateHash: "certificate-a", Filename: "example-arm64.apk"},
		{Hash: "apk-b", CertificateHash: "certificate-a", Filename: "example-offline-arm64.apk"},
		{Hash: "apk-c", CertificateHash: "certificate-b", Filename: "example-x86_64.apk"},
	}

	got := apkSelectionChoices(candidates)
	want := []ui.SelectChoice{
		{Label: "certificate-a\n  example-arm64.apk\n  example-offline-arm64.apk", Value: "apk-a"},
		{Label: "certificate-b\n  example-x86_64.apk", Value: "apk-c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("apkSelectionChoices() = %#v, want %#v", got, want)
	}
}

func TestCLIErrorDocumentKeepsPartialResultAtTopLevel(t *testing.T) {
	encoded, err := json.Marshal(cliErrorDocument{
		OK:        false,
		Operation: "publish",
		Error:     cliErrorBody{Code: "temporary_failure"},
		Result:    &zsp.PublishResult{Status: "partial"},
	})
	if err != nil {
		t.Fatal(err)
	}
	document := string(encoded)
	if !strings.Contains(document, `"result":{"id":"","status":"partial"`) {
		t.Fatalf("partial result is not top-level: %s", document)
	}
	if strings.Contains(document, `"publication"`) {
		t.Fatalf("partial result retained legacy nesting: %s", document)
	}
}
