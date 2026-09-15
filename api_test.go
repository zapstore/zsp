package zsp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/blossom"
	"github.com/zapstore/zsp/internal/identity"
	internalnostr "github.com/zapstore/zsp/internal/nostr"
	"github.com/zapstore/zsp/internal/source"
)

var (
	_ func(string) (Config, error)                                                       = LoadConfig
	_ func(context.Context, FetchConfig, FetchOptions) ([]*APK, error)                   = Fetch
	_ func(context.Context, PublishConfig, *APK, PublishOptions) (*PublishResult, error) = Publish
)

func TestPublicStructFieldsMatchContract(t *testing.T) {
	tests := []struct {
		value any
		want  []string
	}{
		{Config{}, []string{"FetchConfig", "PublishConfig"}},
		{FetchConfig{}, []string{"Repository", "ReleaseSource", "ReleaseFilter", "Match", "PrereleaseChannel"}},
		{PublishConfig{}, []string{"Name", "Summary", "Description", "Tags", "License", "Website", "Icon", "Images", "ReleaseNotes", "SupportedNIPs", "MinAllowedVersion", "MinAllowedVersionCode", "MetadataSources", "Channel"}},
		{ReleaseSource{}, []string{"URL", "LocalPath", "Type", "AssetURL", "VersionExtractor", "AssetExtractor"}},
		{Extractor{}, []string{"URL", "Selector", "Attribute", "Path", "Header", "Match"}},
		{FetchOptions{}, []string{"OnProgress"}},
		{APK{}, []string{"Hash", "Filename", "SourceURL", "Size", "AppID", "VersionName", "VersionCode", "MinSDK", "TargetSDK", "Name", "CertificateHash", "LineageHashes", "Architectures"}},
		{Progress{}, []string{"Operation", "Phase", "Target", "Completed", "Total"}},
		{PublishOptions{}, []string{"BlossomURL", "Relays", "Channel", "Commit", "SkipAppEvent", "SkipMediaCompression", "SkipProofCheck", "OverwriteRelease", "Preview", "BrowserPort", "OnProgress"}},
		{EventIDs{}, []string{"Application", "Release", "Assets"}},
		{PublishResult{}, []string{"ID", "Status", "AppID", "CertificateHash", "LineageHashes", "Events", "Uploads", "Relays", "Warnings"}},
		{BlobResult{}, []string{"URL", "Hash", "Size", "Type", "Uploaded", "Accepted", "Message"}},
		{RelayResult{}, []string{"RelayURL", "EventID", "Accepted", "Duplicate", "Message"}},
	}

	for _, test := range tests {
		typ := reflect.TypeOf(test.value)
		var got []string
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			if field.PkgPath == "" {
				got = append(got, field.Name)
			}
		}
		sort.Strings(got)
		sort.Strings(test.want)
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s exported fields = %v, want %v", typ.Name(), got, test.want)
		}
	}
}

func TestLoadConfigResolvesLocalSourceRelativeToConfig(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "zapstore.yaml")
	if err := os.WriteFile(configPath, []byte(`
release_source: builds/*.apk
name: Example
icon: media/icon.png
images: [media/one.png, https://example.com/two.png]
release_notes: notes.md
`), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.ReleaseSource == nil {
		t.Fatal("ReleaseSource is nil")
	}
	want := filepath.Join(directory, "builds", "*.apk")
	if config.ReleaseSource.LocalPath != want {
		t.Fatalf("LocalPath = %q, want %q", config.ReleaseSource.LocalPath, want)
	}
	if config.Name != "Example" {
		t.Fatalf("Name = %q, want Example", config.Name)
	}
	if config.Icon != filepath.Join(directory, "media", "icon.png") {
		t.Fatalf("Icon = %q, want resolved local path", config.Icon)
	}
	if !reflect.DeepEqual(config.Images, []string{
		filepath.Join(directory, "media", "one.png"),
		"https://example.com/two.png",
	}) {
		t.Fatalf("Images = %v, want resolved local paths and unchanged URLs", config.Images)
	}
	if config.ReleaseNotes != filepath.Join(directory, "notes.md") {
		t.Fatalf("ReleaseNotes = %q, want resolved local path", config.ReleaseNotes)
	}
}

func TestLoadConfigPreservesEmptyMetadataSources(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "zapstore.yaml")
	if err := os.WriteFile(configPath, []byte("repository: https://github.com/example/app\nmetadata_sources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.MetadataSources == nil {
		t.Fatal("MetadataSources = nil, want non-nil empty slice")
	}
	if len(config.MetadataSources) != 0 {
		t.Fatalf("MetadataSources = %v, want empty", config.MetadataSources)
	}
}

func TestLoadConfigLoadsChannelFields(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "zapstore.yaml")
	if err := os.WriteFile(configPath, []byte(`
repository: https://github.com/example/app
prerelease_channel: beta
channel: nightly
`), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.PrereleaseChannel != "beta" {
		t.Errorf("PrereleaseChannel = %q, want beta", config.PrereleaseChannel)
	}
	if config.Channel != "nightly" {
		t.Errorf("Channel = %q, want nightly", config.Channel)
	}
}

func TestLoadConfigRejectsUnusedVersionExtractor(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "zapstore.yaml")
	if err := os.WriteFile(configPath, []byte(`
release_source:
  url: https://github.com/example/app
  version:
    url: https://example.com/latest
    path: $.version
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(configPath)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("LoadConfig error = %v, want ErrInvalidConfig", err)
	}
}

func TestSelectedChannelPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		options PublishOptions
		config  PublishConfig
		want    string
	}{
		{name: "default", want: "main"},
		{name: "config", config: PublishConfig{Channel: "nightly"}, want: "nightly"},
		{name: "options wins over config", options: PublishOptions{Channel: "dev"}, config: PublishConfig{Channel: "nightly"}, want: "dev"},
		{name: "whitespace is omitted", options: PublishOptions{Channel: " "}, config: PublishConfig{Channel: " "}, want: "main"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := selectedChannel(test.options, test.config); got != test.want {
				t.Errorf("selectedChannel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFetchRejectsRelativeDirectLocalSource(t *testing.T) {
	_, err := Fetch(t.Context(), FetchConfig{
		ReleaseSource: &ReleaseSource{LocalPath: "builds/app.apk"},
	}, FetchOptions{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Fetch error = %v, want ErrInvalidConfig", err)
	}
}

func TestFetchRejectsUnsupportedSourceAsInvalidConfig(t *testing.T) {
	_, err := Fetch(t.Context(), FetchConfig{
		Repository: "https://downloads.example.com/project",
	}, FetchOptions{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Fetch error = %v, want ErrInvalidConfig", err)
	}
}

func TestDirectStructuredLocalSourceUsesAbsoluteURLPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.apk")
	internal, err := fetchInternalConfig(FetchConfig{
		ReleaseSource: &ReleaseSource{Type: "local", URL: path},
	})
	if err != nil {
		t.Fatal(err)
	}
	if internal.ReleaseSource.LocalPath != path || internal.ReleaseSource.URL != "" {
		t.Fatalf("internal release source = %+v, want local path %q", internal.ReleaseSource, path)
	}
}

func TestConfigurationConversionDoesNotMutateCallerValues(t *testing.T) {
	fetchConfig := FetchConfig{
		ReleaseSource: &ReleaseSource{
			URL:      "https://example.com/app-{version}.apk",
			AssetURL: "https://example.com/app.apk",
			VersionExtractor: &Extractor{
				URL:  "https://example.com/latest",
				Path: "$.version",
			},
		},
	}
	fetchBefore := cloneFetchConfig(fetchConfig)
	_, _ = fetchInternalConfig(fetchConfig)
	if !reflect.DeepEqual(fetchConfig, fetchBefore) {
		t.Fatalf("fetch configuration was mutated: got %+v want %+v", fetchConfig, fetchBefore)
	}

	publishConfig := PublishConfig{
		Tags:            []string{"social", "tools"},
		Images:          []string{"https://example.com/one.png"},
		SupportedNIPs:   []string{"01", "82"},
		MetadataSources: []string{"github"},
	}
	publishBefore := PublishConfig{
		Tags:            append([]string(nil), publishConfig.Tags...),
		Images:          append([]string(nil), publishConfig.Images...),
		SupportedNIPs:   append([]string(nil), publishConfig.SupportedNIPs...),
		MetadataSources: append([]string(nil), publishConfig.MetadataSources...),
	}
	_ = toPublishInternalConfig(publishConfig)
	if !reflect.DeepEqual(publishConfig, publishBefore) {
		t.Fatalf("publish configuration was mutated: got %+v want %+v", publishConfig, publishBefore)
	}
}

func TestAPKCloseIsIdempotentForLocalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.apk")
	if err := os.WriteFile(path, []byte("user-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := APK{ownership: newAPKOwnership(path, "", false)}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Close removed local APK: %v", err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAPKCloseRemovesManagedTemporaryDirectory(t *testing.T) {
	directory := t.TempDir()
	managed := filepath.Join(directory, "managed")
	if err := os.Mkdir(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(managed, "app.apk")
	if err := os.WriteFile(path, []byte("managed"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := APK{ownership: newAPKOwnership(path, managed, true)}

	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(managed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed directory still exists: %v", err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
}

type fakeAPKTimer struct {
	stopped bool
}

func (timer *fakeAPKTimer) Stop() bool {
	wasRunning := !timer.stopped
	timer.stopped = true
	return wasRunning
}

func TestAPKLifetimePausesForPublishAndRestartsAfterFailure(t *testing.T) {
	originalSchedule := scheduleAPKExpiry
	var callbacks []func()
	var timers []*fakeAPKTimer
	scheduleAPKExpiry = func(after time.Duration, expire func()) apkTimer {
		if after != 5*time.Minute {
			t.Fatalf("APK lifetime = %v, want 5m", after)
		}
		timer := &fakeAPKTimer{}
		timers = append(timers, timer)
		callbacks = append(callbacks, expire)
		return timer
	}
	t.Cleanup(func() { scheduleAPKExpiry = originalSchedule })

	managed := filepath.Join(t.TempDir(), "managed")
	if err := os.Mkdir(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(managed, "app.apk")
	if err := os.WriteFile(path, []byte("managed"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := &APK{ownership: newAPKOwnership(path, managed, true)}
	candidate.startLifetime()
	if len(callbacks) != 1 {
		t.Fatalf("scheduled timers = %d, want 1", len(callbacks))
	}

	if _, ok := candidate.beginPublish(); !ok {
		t.Fatal("beginPublish() rejected an open APK")
	}
	if !timers[0].stopped {
		t.Fatal("beginPublish() did not stop the lifetime timer")
	}
	callbacks[0]()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expired timer removed APK during Publish: %v", err)
	}

	if err := candidate.finishPublish(false); err != nil {
		t.Fatal(err)
	}
	if len(callbacks) != 2 {
		t.Fatalf("scheduled timers after failed Publish = %d, want 2", len(callbacks))
	}
	callbacks[1]()
	if _, err := os.Stat(managed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired APK was not removed: %v", err)
	}
	if _, ok := candidate.beginPublish(); ok {
		t.Fatal("beginPublish() accepted an expired APK")
	}
}

func TestAPKCloseDuringPublishClosesAfterPublishReturns(t *testing.T) {
	managed := filepath.Join(t.TempDir(), "managed")
	if err := os.Mkdir(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(managed, "app.apk")
	if err := os.WriteFile(path, []byte("managed"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := &APK{ownership: newAPKOwnership(path, managed, true)}
	if _, ok := candidate.beginPublish(); !ok {
		t.Fatal("beginPublish() rejected an open APK")
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Close removed APK during Publish: %v", err)
	}
	if err := candidate.finishPublish(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(managed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("requested cleanup did not run after Publish: %v", err)
	}
}

func TestOperationErrorsAreInspectable(t *testing.T) {
	_, err := Fetch(t.Context(), FetchConfig{}, FetchOptions{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Fetch error = %v, want ErrInvalidConfig", err)
	}
	var operationError Error
	if !errors.As(err, &operationError) {
		t.Fatalf("Fetch error %T does not implement Error", err)
	}
	if operationError.Retryable() {
		t.Fatal("invalid configuration must not be retryable")
	}
}

func TestOperationErrorsHideWrappedSecrets(t *testing.T) {
	const secret = "nsec1must-never-appear"
	err := wrapOperationError(ErrInvalidConfig, errors.New(secret), false, "create signer")
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("public error message exposed a signing secret")
	}
	var operationError Error
	if !errors.As(err, &operationError) || operationError.Retryable() {
		t.Fatalf("unexpected structured error: %#v", err)
	}
}

func TestTemporaryErrorsAreAlwaysRetryable(t *testing.T) {
	err := operationErr(ErrTemporaryFailure, false, "temporary")
	var operationError Error
	if !errors.As(err, &operationError) || !operationError.Retryable() {
		t.Fatalf("ErrTemporaryFailure must be retryable: %#v", err)
	}
}

func TestContextDeadlineRemainsInspectable(t *testing.T) {
	err := contextOperationError(context.DeadlineExceeded, "fetch")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v, want context deadline", err)
	}
	var operationError Error
	if !errors.As(err, &operationError) || operationError.Retryable() {
		t.Fatalf("an already-cancelled context must not be retryable: %#v", err)
	}
}

func TestFetchClassifiesCandidateDownloadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	_, err := Fetch(context.Background(), FetchConfig{
		ReleaseSource: &ReleaseSource{URL: server.URL + "/app.apk"},
	}, FetchOptions{})
	if !errors.Is(err, ErrTemporaryFailure) {
		t.Fatalf("Fetch() error = %v, want ErrTemporaryFailure", err)
	}
	var operationError Error
	if !errors.As(err, &operationError) || !operationError.Retryable() {
		t.Fatalf("Fetch() error must be retryable: %#v", err)
	}
}

func TestBlossomErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		sentinel  error
		retryable bool
	}{
		{"invalid auth", blossom.ErrInvalidAuthEvent, ErrInvalidConfig, false},
		{"rate limited", &blossom.StatusError{Err: blossom.ErrUploadRejected, StatusCode: http.StatusTooManyRequests}, ErrRateLimited, true},
		{"bad request", &blossom.StatusError{Err: blossom.ErrUploadRejected, StatusCode: http.StatusBadRequest}, ErrUploadRejected, false},
		{"invalid descriptor", blossom.ErrInvalidDescriptor, ErrTemporaryFailure, true},
		{"missing local file", os.ErrNotExist, ErrSourceFailed, false},
		{"connection reset", errors.New("write: connection reset by peer"), ErrTemporaryFailure, true},
		{"unknown permanent failure", errors.New("unsupported operation"), ErrSourceFailed, false},
		{"cancelled", context.Canceled, context.Canceled, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sentinel, retryable := blossomErrorClassification(test.err)
			if !errors.Is(sentinel, test.sentinel) || retryable != test.retryable {
				t.Fatalf("classification = (%v, %v), want (%v, %v)", sentinel, retryable, test.sentinel, test.retryable)
			}
		})
	}
}

func TestSourceErrorClassification(t *testing.T) {
	tests := []struct {
		err       error
		sentinel  error
		retryable bool
	}{
		{errors.New("status 429"), ErrRateLimited, true},
		{errors.New("service temporarily unavailable"), ErrTemporaryFailure, true},
		{errors.New("invalid repository"), ErrSourceFailed, false},
		{context.DeadlineExceeded, ErrTemporaryFailure, true},
	}
	for _, test := range tests {
		sentinel, retryable := sourceErrorClassification(test.err)
		if !errors.Is(sentinel, test.sentinel) || retryable != test.retryable {
			t.Fatalf("classification(%v) = (%v, %v), want (%v, %v)", test.err, sentinel, retryable, test.sentinel, test.retryable)
		}
	}
}

func TestAllRelayQueryFailuresAreRetryable(t *testing.T) {
	for _, queryErr := range []error{
		errors.New("relay closed without a response"),
		errors.New("connection refused"),
		errors.New("status 429"),
	} {
		sentinel, retryable := relayQueryErrorClassification(queryErr)
		if !retryable {
			t.Errorf("classification(%v) is not retryable", queryErr)
		}
		if !errors.Is(sentinel, ErrTemporaryFailure) && !errors.Is(sentinel, ErrRateLimited) {
			t.Errorf("classification(%v) sentinel = %v, want temporary or rate-limited", queryErr, sentinel)
		}
	}
}

func TestExcludedFilenameUsesTokenBoundaries(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"app (x86).apk", true},
		{"app[armeabi-v7a].apk", true},
		{"app unsigned release.apk", true},
		{"app-split.apk", true},
		{"app.config.apk", true},
		{"configuration.apk", false},
		{"unsignedly.apk", false},
		{"arm64-v8a.apk", false},
	}
	for _, test := range tests {
		if got := excludedFilename(test.name); got != test.want {
			t.Errorf("excludedFilename(%q) = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestReleaseFilterMatchesReleaseName(t *testing.T) {
	release := &source.Release{
		Version: "1.2.3",
		TagName: "v1.2.3",
		Name:    "Android nightly",
	}
	if !releaseMatches(release, `^Android nightly$`) {
		t.Fatal("release filter did not match the release name")
	}
}

func TestRelayPublicationErrorsRemainClassifiable(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name      string
		ctx       context.Context
		err       error
		sentinel  error
		retryable bool
	}{
		{"cancelled", cancelled, errors.New("publish failed"), context.Canceled, false},
		{"rate limited", t.Context(), errors.New("relay returned status 429"), ErrRateLimited, true},
		{"temporary outage", t.Context(), errors.New("connection refused"), ErrTemporaryFailure, true},
		{"policy rejection", t.Context(), errors.New("blocked by relay policy"), ErrPublishRejected, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := relayPublicationError(test.ctx, []internalnostr.PublishResult{{Error: test.err}})
			if !errors.Is(err, test.sentinel) {
				t.Fatalf("relayPublicationError() = %v, want %v", err, test.sentinel)
			}
			var operationError Error
			if !errors.As(err, &operationError) || operationError.Retryable() != test.retryable {
				t.Fatalf("unexpected retryability for %v", err)
			}
		})
	}
}

func TestRelayQueryWarningsHideUnderlyingErrors(t *testing.T) {
	warnings := safeRelayQueryWarnings("C1 proof", []error{errors.New("wss://relay/?token=secret")})
	if len(warnings) != 1 || strings.Contains(warnings[0], "secret") {
		t.Fatalf("unsafe warnings: %v", warnings)
	}
	warnings = relayWarnings([]RelayResult{{
		RelayURL: "wss://relay.example/?token=secret",
		Message:  "rejected token secret",
	}})
	if len(warnings) != 1 || strings.Contains(warnings[0], "secret") {
		t.Fatalf("unsafe publication warnings: %v", warnings)
	}
}

func TestRelayCredentialsAreExcludedFromPublicData(t *testing.T) {
	const privateRelay = "wss://relay.example.com/path?token=secret"
	const publicRelay = "wss://relay.example.com/path"
	if got := firstRelay([]string{privateRelay}); got != publicRelay {
		t.Fatalf("firstRelay() = %q, want %q", got, publicRelay)
	}
	if got := publicRelayURLs([]string{privateRelay}); !reflect.DeepEqual(got, []string{publicRelay}) {
		t.Fatalf("publicRelayURLs() = %v, want credential-free relay", got)
	}
}

func TestNewestAuthorizedApplicationUsesNIP01Ordering(t *testing.T) {
	secret := gonostr.GeneratePrivateKey()
	pubkey, err := gonostr.GetPublicKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	makeEvent := func(createdAt gonostr.Timestamp) *gonostr.Event {
		event := &gonostr.Event{
			Kind:      internalnostr.KindAppMetadata,
			CreatedAt: createdAt,
			Tags:      gonostr.Tags{{"d", "com.example.app"}},
		}
		if err := event.Sign(secret); err != nil {
			t.Fatal(err)
		}
		return event
	}
	older := makeEvent(10)
	newer := makeEvent(20)
	unauthorized := makeEvent(30)
	unauthorized.PubKey = strings.Repeat("f", 64)
	malformed := makeEvent(40)
	malformed.Tags = append(malformed.Tags, gonostr.Tag{"d", "com.example.app"})
	if err := malformed.Sign(secret); err != nil {
		t.Fatal(err)
	}

	got := newestAuthorizedApplication(
		[]*gonostr.Event{older, unauthorized, malformed, newer},
		"com.example.app",
		map[string]struct{}{pubkey: {}},
	)
	if got != newer {
		t.Fatalf("newestAuthorizedApplication() = %v, want newer authorized event", got)
	}
}

func TestActiveC1PublishersIncludesOwnerAndDelegate(t *testing.T) {
	certificateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "zsp-api-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &certificateKey.PublicKey, certificateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	ownerSecret := gonostr.GeneratePrivateKey()
	owner, err := gonostr.GetPublicKey(ownerSecret)
	if err != nil {
		t.Fatal(err)
	}
	delegateSecret := gonostr.GeneratePrivateKey()
	delegate, err := gonostr.GetPublicKey(delegateSecret)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := identity.GenerateIdentityProof(
		certificateKey,
		certificate,
		owner,
		&identity.IdentityProofOptions{Expiry: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	event := internalnostr.BuildIdentityProofEvent(proof.ToEventTags(), owner, proof.CreatedAt)
	event.Tags = append(event.Tags, gonostr.Tag{"delegation", delegate})
	if err := event.Sign(ownerSecret); err != nil {
		t.Fatal(err)
	}

	authorized := activeC1Publishers([]*gonostr.Event{event}, proof.CertHash, certificate)
	for _, pubkey := range []string{owner, delegate} {
		if _, ok := authorized[pubkey]; !ok {
			t.Errorf("authorized publishers missing %s", pubkey)
		}
	}
	if _, ok := authorized[strings.Repeat("f", 64)]; ok {
		t.Error("unrelated signer was authorized")
	}
}

func TestStatusAfterExternal(t *testing.T) {
	tests := []struct {
		name   string
		result *PublishResult
		want   string
	}{
		{"before requests", &PublishResult{}, "failed"},
		{"after upload", &PublishResult{Uploads: []BlobResult{{Accepted: false}}}, "partial"},
		{"after relay", &PublishResult{Relays: []RelayResult{{Accepted: false}}}, "partial"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := statusAfterExternal(test.result); got != test.want {
				t.Fatalf("statusAfterExternal() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidatePublishInputRejectsRelativeMedia(t *testing.T) {
	err := validatePublishInput(PublishConfig{Icon: "icon.png"}, PublishOptions{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("validatePublishInput error = %v, want ErrInvalidConfig", err)
	}
}

func TestValidatePublishInputAcceptsGiteaMetadata(t *testing.T) {
	err := validatePublishInput(PublishConfig{MetadataSources: []string{"gitea"}}, PublishOptions{})
	if err != nil {
		t.Fatalf("validatePublishInput() = %v", err)
	}
}

func TestValidatePublishInputRejectsInvalidBrowserPort(t *testing.T) {
	for _, port := range []int{-1, 65536} {
		err := validatePublishInput(PublishConfig{}, PublishOptions{BrowserPort: port})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("BrowserPort %d error = %v, want ErrInvalidConfig", port, err)
		}
	}
}

func TestEffectivePublishTargetsValidateEnvironmentDefaults(t *testing.T) {
	t.Setenv("BLOSSOM_URL", "http://uploads.example.test")
	t.Setenv("RELAYS", "ws://relay.example.test")

	_, _, err := effectivePublishTargets(PublishOptions{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("effectivePublishTargets() = %v, want ErrInvalidConfig", err)
	}

	t.Setenv("BLOSSOM_URL", "https://uploads.example.test")
	_, _, err = effectivePublishTargets(PublishOptions{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("effectivePublishTargets() = %v, want ErrInvalidConfig for RELAYS", err)
	}
}

func TestEffectivePublishTargetsUseContractDefaults(t *testing.T) {
	t.Setenv("BLOSSOM_URL", "")
	t.Setenv("RELAYS", "")

	blossomURL, relayURLs, err := effectivePublishTargets(PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if blossomURL != blossom.DefaultServer {
		t.Fatalf("Blossom URL = %q, want %q", blossomURL, blossom.DefaultServer)
	}
	if !reflect.DeepEqual(relayURLs, []string{internalnostr.DefaultRelay}) {
		t.Fatalf("relay URLs = %v, want default relay", relayURLs)
	}
}

func TestEffectivePublishTargetsRejectCredentialBearingBlossomURL(t *testing.T) {
	for _, target := range []string{
		"https://user:secret@cdn.example.com",
		"https://cdn.example.com?token=secret",
		"https://cdn.example.com/#secret",
	} {
		_, _, err := effectivePublishTargets(PublishOptions{BlossomURL: target})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("effectivePublishTargets(%q) error = %v, want ErrInvalidConfig", target, err)
		}
	}
}

func TestPublishRejectsClosedAPK(t *testing.T) {
	candidate := &APK{ownership: &apkOwnership{path: "/unused", closed: true}, Hash: "hash"}
	_, err := Publish(t.Context(), PublishConfig{}, candidate, PublishOptions{})
	if !errors.Is(err, ErrAPKNotChecked) {
		t.Fatalf("Publish error = %v, want ErrAPKNotChecked", err)
	}
}

func TestFetchLocalAPKReturnsVerifiedIdentity(t *testing.T) {
	path := buildSignedTestAPK(t)
	candidates, err := Fetch(t.Context(), FetchConfig{
		ReleaseSource: &ReleaseSource{LocalPath: path},
	}, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.Hash == "" || candidate.AppID == "" || candidate.CertificateHash == "" {
		t.Fatalf("missing verified identity: %+v", candidate)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Close removed caller-owned APK: %v", err)
	}
}

func TestFetchAPKUsesPrivateVerifiedSnapshot(t *testing.T) {
	path := buildSignedTestAPK(t)
	candidates, err := Fetch(t.Context(), FetchConfig{
		ReleaseSource: &ReleaseSource{LocalPath: path},
	}, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = candidates[0].Close() })

	candidate := candidates[0]
	verified := candidate.verified
	candidate.Hash = "mutated"
	candidate.AppID = "mutated"
	candidate.VersionCode = 0
	candidate.CertificateHash = "mutated"
	candidate.LineageHashes = []string{"mutated"}

	if candidate.verified.hash != verified.hash ||
		candidate.verified.appID != verified.appID ||
		candidate.verified.versionCode != verified.versionCode ||
		candidate.verified.certificateHash != verified.certificateHash ||
		strings.Join(candidate.verified.lineageHashes, ",") != strings.Join(verified.lineageHashes, ",") {
		t.Fatalf("verified snapshot changed: %#v", candidate.verified)
	}
}

func buildSignedTestAPK(t *testing.T) string {
	t.Helper()
	root := "/opt/homebrew/share/android-commandlinetools"
	aapt := androidTool(t, filepath.Join(root, "build-tools", "*", "aapt"))
	apksigner := androidTool(t, filepath.Join(root, "build-tools", "*", "apksigner"))
	androidJar := androidTool(t, filepath.Join(root, "platforms", "android-*", "android.jar"))

	dir := t.TempDir()
	manifest := filepath.Join(dir, "AndroidManifest.xml")
	manifestXML := `<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="com.example.zsptest" android:versionCode="42" android:versionName="1.0"><uses-sdk android:minSdkVersion="23" android:targetSdkVersion="35"/><application android:label="ZSP Test"/></manifest>`
	if err := os.WriteFile(manifest, []byte(manifestXML), 0o600); err != nil {
		t.Fatal(err)
	}
	unsignedAPK := filepath.Join(dir, "unsigned.apk")
	runTestCommand(t, aapt, "package", "-f", "-M", manifest, "-I", androidJar, "-F", unsignedAPK)

	keystore := filepath.Join(dir, "release.jks")
	runTestCommand(t, "keytool", "-genkeypair", "-alias", "release", "-keystore", keystore,
		"-storetype", "JKS", "-storepass", "storepass", "-keypass", "storepass",
		"-dname", "CN=zsp-api-test", "-keyalg", "RSA", "-keysize", "2048", "-validity", "1", "-noprompt")

	signedAPK := filepath.Join(dir, "sample.apk")
	runTestCommand(t, apksigner, "sign", "--ks", keystore, "--ks-key-alias", "release",
		"--ks-pass", "pass:storepass", "--key-pass", "pass:storepass", "--out", signedAPK, unsignedAPK)
	runTestCommand(t, apksigner, "verify", "--verbose", signedAPK)
	return signedAPK
}

func androidTool(t *testing.T, pattern string) string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	for index := len(matches) - 1; index >= 0; index-- {
		if info, err := os.Stat(matches[index]); err == nil && !info.IsDir() {
			return matches[index]
		}
	}
	t.Fatalf("required Android tool not found: %s", pattern)
	return ""
}

func runTestCommand(t *testing.T, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", filepath.Base(name), err, output)
	}
}
