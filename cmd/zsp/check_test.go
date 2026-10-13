package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zapstore/zsp"
)

func TestCheckNextStep(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	owner, delegate, other := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	longProof := &zsp.ProofStatus{Owner: owner, Delegate: delegate, ExpiresAt: now.AddDate(1, 0, 0)}
	expiring := &zsp.ProofStatus{Owner: owner, ExpiresAt: now.AddDate(0, 0, 10)}
	apk := &zsp.APK{VersionName: "1.2.0", VersionCode: 120}

	tests := []struct {
		name          string
		status        zsp.CheckResult
		hotbox        bool
		zapstore      bool
		command       string
		human         bool
		reasonContain string
	}{
		{"no proof", zsp.CheckResult{}, false, true, "zsp", true, "Prove the app is yours"},
		{"no proof with hotbox", zsp.CheckResult{Signer: owner}, true, true, "zsp publish", false, "Hotbox signs it"},
		{"no proof with hotbox but no signer", zsp.CheckResult{}, true, true, "zsp", true, "Prove"},
		{"own proof expiring", zsp.CheckResult{Proof: expiring, Signer: owner, Authorized: true, PublishedVersionCode: 120}, true, true, "zsp", true, "Renew your proof"},
		{"own proof expiring with hotbox and new version", zsp.CheckResult{Proof: expiring, Signer: owner, Authorized: true, PublishedVersionCode: 110}, true, true, "zsp publish", false, "Renew"},
		{"already published", zsp.CheckResult{Proof: longProof, Listed: true, PublishedVersionCode: 120}, false, true, "", false, ""},
		{"downgrade", zsp.CheckResult{Proof: longProof, Listed: true, PublishedVersionCode: 130, Signer: owner, Authorized: true}, false, true, "", false, ""},
		{"newer, no signer, Zapstore indexes", zsp.CheckResult{Proof: longProof, Listed: true, PublishedVersionCode: 110}, false, true, "zsp", true, "next visit"},
		{"newer, no signer, other relays", zsp.CheckResult{Proof: longProof, Listed: true, PublishedVersionCode: 110}, false, false, "zsp", true, "Set SIGN_WITH"},
		{"newer, unauthorized signer", zsp.CheckResult{Proof: longProof, PublishedVersionCode: 110, Signer: other}, false, false, "zsp", true, "delegate"},
		{"newer, delegate", zsp.CheckResult{Proof: longProof, PublishedVersionCode: 110, Signer: delegate, Authorized: true}, false, true, "zsp publish", false, "Publish version 1.2.0 (120)"},
		{"claimed, never published", zsp.CheckResult{Proof: longProof, Signer: owner, Authorized: true}, false, true, "zsp publish", false, "Publish"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			step := checkNextStep(apk, &test.status, test.hotbox, test.zapstore, now)
			if test.command == "" {
				if step != nil {
					t.Fatalf("next step = %+v, want none", step)
				}
				return
			}
			if step == nil || step.Command != test.command || step.RequiresHuman != test.human || !strings.Contains(step.Reason, test.reasonContain) {
				t.Fatalf("next step = %+v, want %s (human %v) mentioning %q", step, test.command, test.human, test.reasonContain)
			}
		})
	}
}

func TestCheckDocumentIsMinimal(t *testing.T) {
	owner := strings.Repeat("a", 64)
	apk := &zsp.APK{
		AppID: "com.example.app", Name: "Example", VersionName: "1.2.0", VersionCode: 120,
		CertificateHash: strings.Repeat("c", 64), Hash: strings.Repeat("d", 64),
		Architectures: []string{"arm64-v8a"}, SourceURL: "https://example.com/app.apk",
	}
	status := &zsp.CheckResult{Proof: &zsp.ProofStatus{Owner: owner, ExpiresAt: time.Date(2028, 1, 2, 3, 4, 5, 0, time.UTC)}}
	encoded, err := json.Marshal(newCheckDocument(apk, status, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ok", "operation", "apk", "listed", "proof", "published_version_code", "signer", "authorized", "next_step", "warnings"} {
		if _, ok := document[key]; !ok {
			t.Errorf("missing %q in %s", key, encoded)
		}
	}
	if len(document) != 10 {
		t.Errorf("unexpected keys in %s", encoded)
	}
	apkFields := document["apk"].(map[string]any)
	if len(apkFields) != 6 || apkFields["hash"] == nil || apkFields["certificate_hash"] == nil {
		t.Errorf("apk = %v", apkFields)
	}
	proof := document["proof"].(map[string]any)
	if !strings.HasPrefix(proof["owner"].(string), "npub1") || proof["expires_at"] != "2028-01-02T03:04:05Z" {
		t.Errorf("proof = %v", proof)
	}
	if _, ok := proof["delegate"]; ok {
		t.Errorf("proof without delegate has one: %v", proof)
	}
	if document["signer"] != nil || document["next_step"] != nil {
		t.Errorf("signer and next_step must be null: %s", encoded)
	}
	if strings.Contains(string(encoded), "sha256") {
		t.Errorf("JSON names sha256: %s", encoded)
	}
}
