package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zapstore/zsp"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/ui"
	"golang.org/x/term"
)

type cliErrorDocument struct {
	OK        bool               `json:"ok"`
	Operation string             `json:"operation"`
	Error     cliErrorBody       `json:"error"`
	Result    *zsp.PublishResult `json:"result,omitempty"`
}

type cliErrorBody struct {
	Code      string         `json:"code"`
	Summary   string         `json:"summary"`
	Retryable bool           `json:"retryable"`
	Context   map[string]any `json:"context,omitempty"`
	NextSteps []cliNextStep  `json:"next_steps"`
}

type cliNextStep struct {
	Command       string `json:"command"`
	Reason        string `json:"reason"`
	RequiresHuman bool   `json:"requires_human,omitempty"`
}

type codedError interface {
	Code() string
}

type cliOperationError struct {
	code    string
	message string
	cause   error
}

func (e *cliOperationError) Error() string { return e.message }
func (e *cliOperationError) Code() string  { return e.code }
func (e *cliOperationError) Unwrap() error { return e.cause }

func publishCommand(ctx context.Context, opts *cli.Options) int {
	config, err := loadPublicConfig(opts)
	if err != nil {
		return writePublishError(opts, err, nil)
	}
	var fetchWarnings []string
	reporter := ui.NewProgressReporter(os.Stderr, opts.ShouldShowSpinners() && !opts.Global.NoColor && term.IsTerminal(int(os.Stderr.Fd())))
	defer reporter.Finish()
	progress := func(progress zsp.Progress) {
		if progress.Phase == "warning" {
			fetchWarnings = append(fetchWarnings, progress.Target)
		}
		if !opts.Publish.Quiet && !opts.Global.JSON {
			reporter.Report(progress)
		}
	}
	candidates, err := zsp.Fetch(ctx, config.FetchConfig, zsp.FetchOptions{
		OnProgress:    progress,
		SkipHTTPCache: opts.Publish.OverwriteRelease || opts.Publish.Check,
	})
	if err != nil {
		return writePublishError(opts, err, nil)
	}
	defer closeCandidates(candidates)

	if opts.Publish.Check {
		return writeCheckSuccess(opts, candidates, fetchWarnings)
	}
	selected, err := selectPublicAPK(opts, candidates)
	if err != nil {
		return writePublishError(opts, err, candidates)
	}
	result, err := zsp.Publish(ctx, config.PublishConfig, selected, zsp.PublishOptions{
		Channel:              opts.Publish.Channel,
		Commit:               opts.Publish.Commit,
		SkipAppEvent:         opts.Publish.SkipAppEvent,
		SkipMediaCompression: opts.Publish.NoCompress,
		OverwriteRelease:     opts.Publish.OverwriteRelease,
		Preview:              !opts.Publish.SkipPreview && !opts.Publish.Quiet && !opts.Global.JSON && term.IsTerminal(int(os.Stdin.Fd())),
		BrowserPort:          opts.Publish.Port,
		OnProgress:           progress,
	})
	if err != nil {
		if result != nil {
			result.Warnings = append(fetchWarnings, result.Warnings...)
		}
		return writePublishError(opts, err, []*zsp.APK{selected}, result)
	}
	result.Warnings = append(fetchWarnings, result.Warnings...)
	return writePublishSuccess(opts, result, selected)
}

func loadPublicConfig(opts *cli.Options) (zsp.Config, error) {
	var config zsp.Config
	var err error
	if len(opts.Args) > 1 {
		return config, fmt.Errorf("publish accepts at most one config or APK argument")
	}
	if len(opts.Args) == 1 && strings.HasSuffix(strings.ToLower(opts.Args[0]), ".apk") {
		path, pathErr := filepath.Abs(opts.Args[0])
		if pathErr != nil {
			return config, pathErr
		}
		config.ReleaseSource = &zsp.ReleaseSource{LocalPath: path}
	} else if len(opts.Args) == 0 && (opts.Publish.RepoURL != "" || opts.Publish.ReleaseSource != "") {
		// Flags provide the source configuration.
	} else {
		path := "zapstore.yaml"
		if len(opts.Args) == 1 {
			path = opts.Args[0]
		}
		config, err = zsp.LoadConfig(path)
		if err != nil {
			return config, err
		}
	}
	if opts.Publish.RepoURL != "" {
		config.Repository = normalizeRepoURL(opts.Publish.RepoURL)
	}
	if opts.Publish.ReleaseSource != "" {
		value := opts.Publish.ReleaseSource
		if !strings.Contains(value, "://") {
			if absolute, pathErr := filepath.Abs(value); pathErr == nil {
				if _, statErr := os.Stat(absolute); statErr == nil {
					config.ReleaseSource = &zsp.ReleaseSource{LocalPath: absolute}
				} else {
					config.ReleaseSource = &zsp.ReleaseSource{URL: normalizeRepoURL(value)}
				}
			}
		} else {
			config.ReleaseSource = &zsp.ReleaseSource{URL: value}
		}
	}
	if opts.Publish.Match != "" {
		config.Match = opts.Publish.Match
	}
	if opts.Publish.ReleaseFilter != "" {
		config.ReleaseFilter = opts.Publish.ReleaseFilter
	}
	if opts.Publish.PrereleaseChannel != "" {
		config.PrereleaseChannel = opts.Publish.PrereleaseChannel
	}
	if len(opts.Publish.Metadata) > 0 {
		config.MetadataSources = append([]string(nil), opts.Publish.Metadata...)
	}
	if opts.Publish.SkipMetadata {
		config.MetadataSources = []string{}
	}
	return config, nil
}

func normalizeRepoURL(value string) string {
	if strings.Contains(value, "://") {
		return value
	}
	return "https://" + value
}

func selectPublicAPK(opts *cli.Options, candidates []*zsp.APK) (*zsp.APK, error) {
	return selectPublicAPKWithPrompt(
		opts,
		candidates,
		"Select the APK to publish",
		"Each candidate was downloaded and signature-verified.",
	)
}

func selectAPKForAppSetup(candidates []*zsp.APK) (*zsp.APK, error) {
	return selectPublicAPKWithPrompt(
		&cli.Options{},
		candidates,
		"Select the certificate that identifies your app",
		"Used to verify the app's package and signing certificate. This does not pin the APK in zapstore.yaml.",
	)
}

func selectPublicAPKWithPrompt(opts *cli.Options, candidates []*zsp.APK, title, description string) (*zsp.APK, error) {
	if opts.Publish.APKHash != "" {
		for index := range candidates {
			if candidates[index].Hash == strings.ToLower(opts.Publish.APKHash) {
				return candidates[index], nil
			}
		}
		return nil, &cliOperationError{
			code: "apk_not_checked", message: fmt.Sprintf("no verified APK has hash %s", opts.Publish.APKHash),
			cause: zsp.ErrAPKNotChecked,
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	choices := apkSelectionChoices(candidates)
	if len(choices) == 1 {
		return candidates[0], nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || opts.Publish.Quiet || opts.Global.JSON {
		return nil, &cliOperationError{
			code: "apk_selection_required", message: "multiple verified APKs require --apk-hash",
			cause: zsp.ErrAPKSelectionRequired,
		}
	}
	selected, err := ui.SelectChoiceField(title, description, choices)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.Hash == selected {
			return candidate, nil
		}
	}
	return nil, &cliOperationError{
		code: "apk_not_checked", message: "selected APK is unavailable",
		cause: zsp.ErrAPKNotChecked,
	}
}

func apkSelectionChoices(candidates []*zsp.APK) []ui.SelectChoice {
	choices := make([]ui.SelectChoice, 0, len(candidates))
	byCertificateHash := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		if index, ok := byCertificateHash[candidate.CertificateHash]; ok {
			choices[index].Label += "\n  " + candidate.Filename
			continue
		}
		byCertificateHash[candidate.CertificateHash] = len(choices)
		choices = append(choices, ui.SelectChoice{
			Label: fmt.Sprintf("%s\n  %s", candidate.CertificateHash, candidate.Filename),
			Value: candidate.Hash,
		})
	}
	return choices
}

func closeCandidates(candidates []*zsp.APK) {
	for _, candidate := range candidates {
		_ = candidate.Close()
	}
}

func writeCheckSuccess(opts *cli.Options, candidates []*zsp.APK, warnings []string) int {
	if opts.Global.JSON {
		if warnings == nil {
			warnings = []string{}
		}
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			OK        bool       `json:"ok"`
			Operation string     `json:"operation"`
			APKs      []*zsp.APK `json:"apks"`
			Warnings  []string   `json:"warnings"`
		}{true, "check", candidates, warnings})
		return 0
	}
	for _, candidate := range candidates {
		ui.WritePanel(os.Stdout, "success", "APK verified", []ui.KeyValue{
			{Key: "File", Value: candidate.Filename},
			{Key: "Package", Value: candidate.AppID},
			{Key: "SHA-256", Value: candidate.Hash},
		}, nil)
	}
	for _, warning := range warnings {
		ui.WritePanel(os.Stderr, "warning", "Verification warning", nil, []string{warning})
	}
	return 0
}

func writePublishSuccess(opts *cli.Options, result *zsp.PublishResult, selected *zsp.APK) int {
	if opts.Global.JSON {
		if result.Warnings == nil {
			result.Warnings = []string{}
		}
		var application *string
		if result.Events.Application != "" {
			application = &result.Events.Application
		}
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			OK          bool                  `json:"ok"`
			Operation   string                `json:"operation"`
			ID          string                `json:"id"`
			Status      string                `json:"status"`
			SelectedAPK *zsp.APK              `json:"selected_apk"`
			Events      publishEventsDocument `json:"events"`
			Uploads     []zsp.BlobResult      `json:"uploads"`
			Relays      []zsp.RelayResult     `json:"relays"`
			Warnings    []string              `json:"warnings"`
		}{
			true, "publish", result.ID, result.Status, selected,
			publishEventsDocument{Application: application, Release: result.Events.Release, Assets: result.Events.Assets},
			result.Uploads, result.Relays, result.Warnings,
		})
		return 0
	}
	ui.WritePanel(os.Stdout, "success", "Release published", []ui.KeyValue{
		{Key: "Application", Value: result.AppID},
		{Key: "Event", Value: result.ID},
		{Key: "Release", Value: result.Events.Release},
	}, []string{ui.RenderCommand("zsp publish --check")})
	for _, warning := range result.Warnings {
		ui.WritePanel(os.Stderr, "warning", "Publish warning", nil, []string{warning})
	}
	return 0
}

type publishEventsDocument struct {
	Application *string  `json:"application"`
	Release     string   `json:"release"`
	Assets      []string `json:"assets"`
}

func writePublishError(opts *cli.Options, err error, candidates []*zsp.APK, partial ...*zsp.PublishResult) int {
	code := publishErrorCode(err)
	retryable := false
	var operationError zsp.Error
	if errors.As(err, &operationError) {
		retryable = operationError.Retryable()
	}
	body := cliErrorBody{Code: code, Summary: safeSummary(code), Retryable: retryable, NextSteps: []cliNextStep{}}
	if code == "proof_required" {
		if len(candidates) == 1 {
			body.Context = map[string]any{"certificate_hash": candidates[0].CertificateHash}
		}
		body.NextSteps = []cliNextStep{{
			Command:       "zsp",
			Reason:        "Create the required certificate ownership proof.",
			RequiresHuman: true,
		}}
	}
	if code == "apk_selection_required" {
		body.Context = map[string]any{"candidates": candidates}
		body.NextSteps = []cliNextStep{{
			Command: "zsp publish --quiet --skip-preview --apk-hash <sha256> <input>",
			Reason:  "Select one verified APK by its exact hash.",
		}}
	}
	if code == "no_new_apk" {
		body.NextSteps = []cliNextStep{{
			Command: "zsp publish --overwrite-release",
			Reason:  "Fetch and publish even if the source ETag is unchanged.",
		}}
	}
	var result *zsp.PublishResult
	if len(partial) > 0 {
		result = partial[0]
	}
	if opts.Global.JSON {
		operation := "publish"
		if opts.Publish.Check {
			operation = "check"
		}
		_ = json.NewEncoder(os.Stderr).Encode(cliErrorDocument{OK: false, Operation: operation, Error: body, Result: result})
	} else {
		ui.WritePanel(os.Stderr, "error", safeSummary(code), nil, []string{ui.SanitizeErrorMessage(err)})
		if code == "proof_required" {
			fmt.Fprintln(os.Stderr, ui.StatusLine("info", ui.RenderCommand("zsp")))
		} else if code == "apk_selection_required" {
			for _, candidate := range candidates {
				fmt.Fprintln(os.Stderr, ui.StatusLine("info", candidate.Filename+" "+candidate.Hash))
			}
			fmt.Fprintln(os.Stderr, ui.StatusLine("info", ui.RenderCommand("zsp publish --apk-hash <sha256>")))
		} else if code == "no_new_apk" {
			fmt.Fprintln(os.Stderr, ui.StatusLine("info", ui.RenderCommand("zsp publish --overwrite-release")))
		}
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	return 1
}

func publishErrorCode(err error) string {
	var coded codedError
	if errors.As(err, &coded) && coded.Code() != "" {
		return coded.Code()
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, zsp.ErrAlreadyPublished):
		return "release_already_published"
	case errors.Is(err, zsp.ErrReleaseDowngrade):
		return "release_downgrade"
	case errors.Is(err, zsp.ErrAPKNotChecked):
		return "apk_not_checked"
	case errors.Is(err, zsp.ErrAPKSelectionRequired):
		return "apk_selection_required"
	case errors.Is(err, zsp.ErrProofRequired):
		return "proof_required"
	case errors.Is(err, zsp.ErrProofUnauthorized):
		return "proof_unauthorized"
	case errors.Is(err, zsp.ErrSigner):
		return "signer_unavailable"
	case errors.Is(err, zsp.ErrUploadRejected):
		return "upload_rejected"
	case errors.Is(err, zsp.ErrPublishRejected):
		return "publish_rejected"
	case errors.Is(err, zsp.ErrInvalidConfig):
		return "invalid_arguments"
	case errors.Is(err, zsp.ErrSourceFailed):
		return "source_failed"
	case errors.Is(err, zsp.ErrNoAPK):
		return "no_apk"
	case errors.Is(err, zsp.ErrNoNewAPK):
		return "no_new_apk"
	case errors.Is(err, zsp.ErrTooManyCandidates):
		return "too_many_candidates"
	case errors.Is(err, zsp.ErrInvalidAPK):
		return "invalid_apk"
	case errors.Is(err, zsp.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, zsp.ErrTemporaryFailure):
		return "temporary_failure"
	default:
		return "publish_failed"
	}
}

func safeSummary(code string) string {
	switch code {
	case "proof_required":
		return "No active proof was found for this certificate."
	case "release_already_published":
		return "This Android version code is already published."
	case "release_downgrade":
		return "The selected APK has a lower Android version code than a published release."
	case "apk_selection_required":
		return "Multiple verified APKs require an explicit selection."
	case "apk_not_checked":
		return "The requested APK hash is not among the verified candidates."
	case "invalid_arguments":
		return "The command arguments are invalid."
	case "invalid_config":
		return "The release configuration is invalid."
	case "source_failed":
		return "The release source could not be read."
	case "no_apk":
		return "No APK passed source filtering and verification."
	case "no_new_apk":
		return "The release source has not changed since the last fetch."
	case "too_many_candidates":
		return "More than ten APK candidates matched; narrow the match pattern."
	case "invalid_apk":
		return "The selected APK is invalid, unsigned, or changed after verification."
	case "rate_limited":
		return "A remote service rate-limited the operation."
	case "temporary_failure":
		return "A temporary network or remote-service failure stopped the operation."
	case "cancelled":
		return "The operation was cancelled."
	default:
		return "The publish operation failed."
	}
}
