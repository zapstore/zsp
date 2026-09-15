package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	publiczsp "github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/cli"
)

func TestSelectPublicAPKWrapsSelectionSentinel(t *testing.T) {
	options := &cli.Options{}
	options.Publish.Quiet = true
	_, err := selectPublicAPK(options, []*publiczsp.APK{{Hash: "one"}, {Hash: "two"}})
	if !errors.Is(err, publiczsp.ErrAPKSelectionRequired) {
		t.Fatalf("selectPublicAPK error = %v, want ErrAPKSelectionRequired", err)
	}
}

func TestSelectPublicAPKWrapsUncheckedSentinel(t *testing.T) {
	options := &cli.Options{}
	options.Publish.APKHash = "missing"
	_, err := selectPublicAPK(options, []*publiczsp.APK{{Hash: "present"}})
	if !errors.Is(err, publiczsp.ErrAPKNotChecked) {
		t.Fatalf("selectPublicAPK error = %v, want ErrAPKNotChecked", err)
	}
}

func TestCLIErrorDocumentKeepsPartialResultAtTopLevel(t *testing.T) {
	encoded, err := json.Marshal(cliErrorDocument{
		OK:        false,
		Operation: "publish",
		Error:     cliErrorBody{Code: "temporary_failure"},
		Result:    &publiczsp.PublishResult{Status: "partial"},
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
