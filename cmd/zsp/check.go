package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/ui"
	"golang.org/x/term"
)

const zapstoreRelayHost = "relay.zapstore.dev"

func checkCommand(ctx context.Context, opts *cli.Options) int {
	config, err := loadPublicConfig(opts)
	if err != nil {
		return writePublishError(opts, err, nil)
	}
	var warnings []string
	reporter := ui.NewProgressReporter(os.Stderr, opts.ShouldShowSpinners() && !opts.Global.NoColor && term.IsTerminal(int(os.Stderr.Fd())))
	defer reporter.Finish()
	candidates, err := zsp.Fetch(ctx, config.Fetch, zsp.FetchOptions{
		OnProgress: func(progress zsp.Progress) {
			if progress.Phase == "warning" {
				warnings = append(warnings, progress.Target)
			}
			if !opts.Global.JSON {
				reporter.Report(progress)
			}
		},
		SkipHTTPCache: true,
	})
	if err != nil {
		return writePublishError(opts, err, nil)
	}
	defer closeCandidates(candidates)

	noPrompt := *opts
	noPrompt.Publish.Quiet = true
	selected, err := selectPublicAPK(&noPrompt, candidates)
	if err != nil {
		return writePublishError(opts, err, candidates)
	}
	status, err := zsp.Check(ctx, config.Publish, selected, zsp.CheckOptions{Channel: opts.Publish.Channel})
	reporter.Finish()
	if err != nil {
		return writePublishError(opts, err, []*zsp.APK{selected})
	}
	warnings = append(warnings, status.Warnings...)
	step := checkNextStep(selected, status, hotboxProofSigner() != nil, hasZapstoreRelay(relaysFromEnv()), time.Now())
	if opts.Global.JSON {
		writeCheckJSON(selected, status, step, warnings)
	} else {
		writeCheckText(selected, status, step, warnings)
	}
	return 0
}

// checkNextStep is the first of: create or renew the proof, nothing to
// publish, a human must provide or authorize SIGN_WITH, publish.
func checkNextStep(apk *zsp.APK, status *zsp.CheckResult, hotbox, zapstoreRelay bool, now time.Time) *cliNextStep {
	version := fmt.Sprintf("%s (%d)", apk.VersionName, apk.VersionCode)
	newer := apk.VersionCode > status.PublishedVersionCode
	proof := status.Proof
	renew := proof != nil && status.Signer != "" && proof.Owner == status.Signer && proof.ExpiresAt.Sub(now) < c1RenewalWindow
	if proof == nil || renew {
		reason := "Prove the app is yours."
		if renew {
			reason = "Renew your proof; it expires " + proof.ExpiresAt.Format("2006-01-02") + "."
		}
		if hotbox && status.Signer != "" && newer {
			return &cliNextStep{Command: "zsp publish", Reason: reason + " Hotbox signs it while publishing version " + version + "."}
		}
		return &cliNextStep{Command: "zsp", Reason: reason, RequiresHuman: true}
	}
	if !newer {
		return nil
	}
	if status.Signer == "" || !status.Authorized {
		reason := "Set SIGN_WITH to publish version " + version + "."
		if status.Signer != "" {
			reason = "SIGN_WITH isn't the proof's owner or delegate. The owner can name it as delegate under CI/CD publishing."
		}
		if status.Listed && zapstoreRelay {
			reason += " Zapstore also publishes new forge releases on its next visit."
		}
		return &cliNextStep{Command: "zsp", Reason: reason, RequiresHuman: true}
	}
	return &cliNextStep{Command: "zsp publish", Reason: "Publish version " + version + "."}
}

func hasZapstoreRelay(relays []string) bool {
	for _, relay := range relays {
		if parsed, err := url.Parse(relay); err == nil && strings.EqualFold(parsed.Hostname(), zapstoreRelayHost) {
			return true
		}
	}
	return false
}

type checkAPKBody struct {
	AppID           string `json:"app_id"`
	Name            string `json:"name"`
	VersionName     string `json:"version_name"`
	VersionCode     int64  `json:"version_code"`
	CertificateHash string `json:"certificate_hash"`
	Hash            string `json:"hash"`
}

type checkProofBody struct {
	Owner     string `json:"owner"`
	Delegate  string `json:"delegate,omitempty"`
	ExpiresAt string `json:"expires_at"`
}

type checkDocument struct {
	OK                   bool            `json:"ok"`
	Operation            string          `json:"operation"`
	APK                  checkAPKBody    `json:"apk"`
	Listed               bool            `json:"listed"`
	Proof                *checkProofBody `json:"proof"`
	PublishedVersionCode int64           `json:"published_version_code"`
	Signer               *string         `json:"signer"`
	Authorized           bool            `json:"authorized"`
	NextStep             *cliNextStep    `json:"next_step"`
	Warnings             []string        `json:"warnings"`
}

func newCheckDocument(apk *zsp.APK, status *zsp.CheckResult, step *cliNextStep, warnings []string) checkDocument {
	document := checkDocument{
		OK: true, Operation: "check",
		APK: checkAPKBody{
			AppID: apk.AppID, Name: apk.Name, VersionName: apk.VersionName, VersionCode: apk.VersionCode,
			CertificateHash: apk.CertificateHash, Hash: apk.Hash,
		},
		Listed:               status.Listed,
		PublishedVersionCode: status.PublishedVersionCode,
		Authorized:           status.Authorized,
		NextStep:             step,
		Warnings:             warnings,
	}
	if document.Warnings == nil {
		document.Warnings = []string{}
	}
	if proof := status.Proof; proof != nil {
		document.Proof = &checkProofBody{Owner: npubFromHex(proof.Owner), ExpiresAt: proof.ExpiresAt.UTC().Format(time.RFC3339)}
		if proof.Delegate != "" {
			document.Proof.Delegate = npubFromHex(proof.Delegate)
		}
	}
	if status.Signer != "" {
		signer := npubFromHex(status.Signer)
		document.Signer = &signer
	}
	return document
}

func writeCheckJSON(apk *zsp.APK, status *zsp.CheckResult, step *cliNextStep, warnings []string) {
	_ = json.NewEncoder(os.Stdout).Encode(newCheckDocument(apk, status, step, warnings))
}

func writeCheckText(apk *zsp.APK, status *zsp.CheckResult, step *cliNextStep, warnings []string) {
	name := apk.Name
	if name == "" {
		name = apk.AppID
	}
	details := []ui.KeyValue{
		{Key: "App", Value: apk.AppID},
		{Key: "Version", Value: fmt.Sprintf("%s (%d)", apk.VersionName, apk.VersionCode)},
		{Key: "Certificate", Value: apk.CertificateHash},
	}
	listed := "No"
	if status.Listed {
		listed = "Yes"
	}
	details = append(details, ui.KeyValue{Key: "Listed", Value: listed})
	if proof := status.Proof; proof != nil {
		details = append(details,
			ui.KeyValue{Key: "Developer", Value: npubFromHex(proof.Owner)},
			ui.KeyValue{Key: "Proof expires", Value: formatProofExpiry(proof.ExpiresAt)},
		)
		if proof.Delegate != "" {
			details = append(details, ui.KeyValue{Key: "Delegate", Value: npubFromHex(proof.Delegate)})
		}
	} else {
		details = append(details, ui.KeyValue{Key: "Developer", Value: "No proof yet"})
	}
	if status.PublishedVersionCode > 0 {
		details = append(details, ui.KeyValue{Key: "Published", Value: fmt.Sprintf("version code %d", status.PublishedVersionCode)})
	}
	if status.Signer != "" {
		signer := npubFromHex(status.Signer)
		if !status.Authorized {
			signer += " (not authorized)"
		}
		details = append(details, ui.KeyValue{Key: "SIGN_WITH", Value: signer})
	}
	var notes []string
	if step == nil {
		notes = []string{fmt.Sprintf("Nothing to publish: version code %d is not newer than the published %d.", apk.VersionCode, status.PublishedVersionCode)}
	} else {
		notes = []string{step.Reason, ui.RenderCommand(step.Command)}
	}
	ui.WritePanel(os.Stdout, "info", name, details, notes)
	for _, warning := range warnings {
		ui.WritePanel(os.Stderr, "warning", "Check warning", nil, []string{warning})
	}
}
