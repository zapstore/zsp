//go:build release && !race

package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func TestCLIBranches(t *testing.T) {
	app := newReleaseApp(t, "dev.zapstore.branchcli", "Branch CLI")
	apk := app.sign(t, 2)
	env := []string{"RELAYS=" + relayURL, "BLOSSOM_URL=" + blossomURL}

	skill := runZSP(t, app.work, nil, "skill")
	if skill.code != 0 || !strings.Contains(skill.stdout, "zapstore.yaml") {
		t.Fatalf("zsp skill exit %d\n%s", skill.code, skill.stdout)
	}

	missing := runZSP(t, t.TempDir(), env, "publish", "--json", "--skip-preview")
	if missing.code == 0 || errorCode(t, missing.stderr) != "invalid_arguments" {
		t.Fatalf("publish without config = %d %s", missing.code, missing.stderr)
	}

	app.yaml(t, apk)
	absent := runZSP(t, app.work, env, "check", "--json", "-s", "http://127.0.0.1:1/missing.apk")
	if absent.code == 0 || errorCode(t, absent.stderr) != "temporary_failure" {
		t.Fatalf("unreachable apk = %d %s", absent.code, absent.stderr)
	}

	empty := filepath.Join(app.work, "empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	none := runZSP(t, app.work, env, "check", "--json", "-s", empty)
	if none.code == 0 || errorCode(t, none.stderr) != "source_failed" {
		t.Fatalf("empty directory = %d %s", none.code, none.stderr)
	}
	filtered := runZSP(t, app.work, env, "check", "--json", "--match", "no-such", "-s", apk)
	if filtered.code == 0 || errorCode(t, filtered.stderr) != "no_apk" {
		t.Fatalf("match miss = %d %s", filtered.code, filtered.stderr)
	}

	before := decodeCheck(t, runZSP(t, app.work, env, "check", "-s", apk))
	if before.Signer != nil || before.NextStep == nil || before.NextStep.Command != "zsp" || !before.NextStep.RequiresHuman {
		t.Fatalf("check with no signer = %+v", before)
	}

	noProof := runZSP(t, app.work, append(env, "SIGN_WITH="+app.secret), "publish", "-q", "--json", "--skip-preview", "-s", apk)
	if noProof.code == 0 || errorCode(t, noProof.stderr) != "proof_required" {
		t.Fatalf("publish without proof = %d %s", noProof.code, noProof.stderr)
	}

	dir := filepath.Join(app.work, "choices")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	otherKeystore := filepath.Join(app.work, "other.keystore")
	makeKeystore(t, otherKeystore)
	otherUnsigned := buildAppAPK(t, app.work, app.id, "Branch CLI", 3)
	otherSigned := strings.TrimSuffix(otherUnsigned, "-unsigned.apk") + ".apk"
	signWithKeystore(t, otherKeystore, otherUnsigned, otherSigned)
	copySigned(t, apk, filepath.Join(dir, "two.apk"))
	copySigned(t, otherSigned, filepath.Join(dir, "three.apk"))
	choose := runZSP(t, app.work, append(env, "SIGN_WITH="+app.secret), "publish", "--json", "--skip-preview", "-s", dir)
	if choose.code == 0 || errorCode(t, choose.stderr) != "apk_selection_required" {
		t.Fatalf("two apks = %d %s", choose.code, choose.stderr)
	}
	picked := runZSP(t, app.work, append(env, "SIGN_WITH="+app.secret), "publish", "--json", "--skip-preview", "-s", dir, "--apk-hash", fileHash(t, otherSigned))
	if picked.code == 0 || errorCode(t, picked.stderr) != "proof_required" {
		t.Fatalf("apk-hash = %d %s", picked.code, picked.stderr)
	}

	url := serveAPK(t, apk)
	remote := decodeCheck(t, runZSP(t, app.work, env, "check", "-s", url))
	if remote.APK.AppID != app.id {
		t.Fatalf("check url app = %s", remote.APK.AppID)
	}
}

func TestUploadRejected(t *testing.T) {
	app := newReleaseApp(t, "dev.zapstore.branchupload", "Branch Upload")
	apk := app.sign(t, 2)
	app.yaml(t, apk)
	box := startWizard(t, app.work, []string{"SIGN_WITH=" + app.secret, "KEYSTORE_PASSWORD=" + keystorePass})
	wizardToChoice(t, box, "", app.keystore)
	mustSend(t, box, "\r")
	if err := approvePreview(promptCtx(t, 60*time.Second)); err != nil {
		t.Fatalf("approve preview: %v\n%s", err, tail(box.text()))
	}
	mustWait(t, box, promptCtx(t, 30*time.Second), "not published")
	endWizard(t, box, 1)
	if !strings.Contains(box.text(), "not allowed") && !strings.Contains(box.text(), "upload") {
		t.Fatalf("upload rejection text missing:\n%s", tail(box.text()))
	}
}

func TestWizardStops(t *testing.T) {
	dead := startWizard(t, t.TempDir(), []string{"RELAYS=ws://127.0.0.1:1"})
	mustWait(t, dead, promptCtx(t, 20*time.Second), "Couldn't reach any configured relay")
	endWizard(t, dead, 1)

	partial := startWizard(t, t.TempDir(), []string{"RELAYS=ws://127.0.0.1:1," + relayURL})
	ctx := promptCtx(t, 20*time.Second)
	mustWait(t, partial, ctx, "Continuing with")
	mustWait(t, partial, ctx, "Where is your APK?")
	mustSend(t, partial, "\x03")
	mustWait(t, partial, ctx, "No changes were made")
	endWizard(t, partial, 0)

	bad := newReleaseApp(t, "dev.zapstore.branchbad", "Branch Bad")
	apk := bad.sign(t, 2)
	bad.yaml(t, apk)
	other := newReleaseApp(t, "dev.zapstore.branchother", "Branch Other")
	box := startWizard(t, bad.work, []string{"SIGN_WITH=" + bad.secret, "KEYSTORE_PASSWORD=" + keystorePass})
	wizardSource(t, box, "")
	mustWait(t, box, promptCtx(t, 30*time.Second), "Signing keystore")
	mustSend(t, box, other.keystore+"\r")
	mustWait(t, box, promptCtx(t, 20*time.Second), "does not match the APK")
	endWizard(t, box, 1)

	notes := newReleaseApp(t, "dev.zapstore.branchnsec", "Branch Nsec")
	notes.yaml(t, notes.sign(t, 2))
	nsec := startWizard(t, notes.work, []string{"KEYSTORE_PASSWORD=" + keystorePass})
	wizardToKeystore(t, nsec, "", notes.keystore)
	mustWait(t, nsec, promptCtx(t, 20*time.Second), "Nostr signer")
	mustSend(t, nsec, "j\r")
	mustWait(t, nsec, promptCtx(t, 20*time.Second), "Use your nsec from .env")
	endWizard(t, nsec, 0)

	quit := newReleaseApp(t, "dev.zapstore.branchquit", "Branch Quit")
	quit.yaml(t, quit.sign(t, 2))
	q := startWizard(t, quit.work, []string{"SIGN_WITH=" + quit.secret, "KEYSTORE_PASSWORD=" + keystorePass})
	wizardToChoice(t, q, "", quit.keystore)
	mustSend(t, q, "j\r")
	mustWait(t, q, promptCtx(t, 20*time.Second), "Nothing was published")
	endWizard(t, q, 0)
}

func TestPreviewRejectAndCancel(t *testing.T) {
	reject := newReleaseApp(t, "dev.zapstore.branchreject", "Branch Reject")
	reject.yaml(t, reject.sign(t, 2))
	box := startWizard(t, reject.work, []string{"SIGN_WITH=" + reject.secret, "KEYSTORE_PASSWORD=" + keystorePass})
	wizardToChoice(t, box, "", reject.keystore)
	mustSend(t, box, "\r")
	if err := decidePreview(promptCtx(t, 60*time.Second), "/api/reject"); err != nil {
		t.Fatalf("reject preview: %v\n%s", err, tail(box.text()))
	}
	mustWait(t, box, promptCtx(t, 20*time.Second), "not published")
	endWizard(t, box, 1)

	cancel := newReleaseApp(t, "dev.zapstore.branchcancel", "Branch Cancel")
	cancel.yaml(t, cancel.sign(t, 2))
	stopped := startWizard(t, cancel.work, []string{"SIGN_WITH=" + cancel.secret, "KEYSTORE_PASSWORD=" + keystorePass})
	wizardToChoice(t, stopped, "", cancel.keystore)
	mustSend(t, stopped, "\r")
	waitPreview(t)
	err := stopped.finish(promptCtx(t, 10*time.Second), true)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 130 {
		t.Fatalf("cancel exit %v\n%s", err, tail(stopped.text()))
	}
}

func TestWizardPublishBranches(t *testing.T) {
	app := newReleaseApp(t, "dev.zapstore.branchpub", "Branch Publish")
	first := app.sign(t, 2)
	app.yaml(t, first)
	admitPublisher(t, app.pubkey)
	delegateSecret, delegate := newKey(t)
	admitPublisher(t, delegate)
	npub, err := nip19.EncodePublicKey(delegate)
	if err != nil {
		t.Fatal(err)
	}

	box := startWizard(t, app.work, []string{"SIGN_WITH=" + app.secret, "KEYSTORE_PASSWORD=" + keystorePass})
	wizardToChoice(t, box, "", app.keystore)
	mustSend(t, box, "\r")
	if err := approvePreview(promptCtx(t, 60*time.Second)); err != nil {
		t.Fatalf("approve preview: %v\n%s", err, tail(box.text()))
	}
	ctx := promptCtx(t, 45*time.Second)
	outcome := mustWaitAny(t, box, ctx, "How should new releases", "Publication was incomplete")
	ciDone := outcome == "How should new releases"
	if ciDone {
		wizardEnableCI(t, box, npub, true)
	} else {
		endWizard(t, box, 1)
		if len(query(t, nostr.Filter{Kinds: []int{kindRelease}, Authors: []string{app.pubkey}})) == 0 {
			logs := ""
			if relayLogs != nil {
				logs = tail(relayLogs.String())
			}
			t.Fatalf("publish was rejected and the release was not stored\n%s", logs)
		}
	}

	again := startWizard(t, app.work, []string{"SIGN_WITH=" + app.secret, "KEYSTORE_PASSWORD=" + keystorePass})
	againCtx := promptCtx(t, 45*time.Second)
	wizardSource(t, again, "")
	mustWait(t, again, againCtx, "is claimed by")
	mustWait(t, again, againCtx, "Nothing to publish")
	if ciDone {
		mustWait(t, again, againCtx, "How should new releases")
		mustSend(t, again, "\r")
		endWizard(t, again, 0)
	} else {
		wizardEnableCI(t, again, npub, false)
	}

	env := []string{"RELAYS=" + relayURL, "BLOSSOM_URL=" + blossomURL, "SIGN_WITH=" + app.secret}
	otherSecret, _ := newKey(t)
	newer := app.sign(t, 3)
	unauthorized := decodeCheck(t, runZSP(t, app.work, []string{"RELAYS=" + relayURL, "BLOSSOM_URL=" + blossomURL, "SIGN_WITH=" + otherSecret}, "check", "--json", "-s", newer))
	if unauthorized.Authorized || unauthorized.Signer == nil || unauthorized.NextStep == nil || unauthorized.NextStep.Command != "zsp" || !unauthorized.NextStep.RequiresHuman {
		t.Fatalf("unauthorized check = %+v", unauthorized)
	}
	human := decodeCheck(t, runZSP(t, app.work, []string{"RELAYS=" + relayURL, "BLOSSOM_URL=" + blossomURL}, "check", "--json", "-s", newer))
	if human.Signer != nil || human.NextStep == nil || human.NextStep.Command != "zsp" || !human.NextStep.RequiresHuman {
		t.Fatalf("check without signer = %+v", human)
	}

	if code := publishCode(t, app.work, env, first); code != "release_already_published" {
		t.Fatalf("republish = %s", code)
	}
	publishArgs(t, app.work, env, "--overwrite-release", "-s", first)
	low := app.sign(t, 1)
	if code := publishCode(t, app.work, env, low); code != "release_downgrade" {
		t.Fatalf("downgrade = %s", code)
	}
	publishArgs(t, app.work, env, "--channel", "beta", "-s", low)
	queryUntil(t, nostr.Filter{Kinds: []int{kindRelease}, Authors: []string{app.pubkey}, Tags: nostr.TagMap{"c": []string{"beta"}}}, 1)

	before := appEventID(t, app.id)
	next := app.sign(t, 4)
	publishArgs(t, app.work, env, "--skip-metadata", "--skip-app-event", "-s", next)
	if got := appEventID(t, app.id); got != before {
		t.Fatalf("app event changed to %s under --skip-app-event", got)
	}
	if got := proofDelegate(t, app.pubkey); got != delegate {
		t.Fatalf("proof delegate = %s, want %s", got, delegate)
	}
	publishArgs(t, app.work, []string{"RELAYS=" + relayURL, "BLOSSOM_URL=" + blossomURL, "SIGN_WITH=" + delegateSecret}, "-s", app.sign(t, 5))
	replaced := app.sign(t, 6)
	publishArgs(t, app.work, env, "--overwrite-app-event", "-s", replaced)
	if got := appEventID(t, app.id); got == before {
		t.Fatal("app event was not replaced after --overwrite-app-event")
	}

	denied := runZSP(t, app.work, []string{"RELAYS=" + relayURL, "BLOSSOM_URL=" + blossomURL, "SIGN_WITH=" + otherSecret}, "publish", "-q", "--json", "--skip-preview", "-s", app.sign(t, 7))
	if denied.code == 0 || errorCode(t, denied.stderr) != "proof_unauthorized" {
		t.Fatalf("unauthorized publish = %d %s", denied.code, denied.stderr)
	}
}

func TestBunkerSavedToEnv(t *testing.T) {
	app := newReleaseApp(t, "dev.zapstore.branchbunker", "Branch Bunker")
	apk := app.sign(t, 2)
	app.yaml(t, apk)
	keys := filepath.Join(app.work, "keys")
	if err := os.MkdirAll(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	vault := createVault(t, app.work, filepath.Join(app.work, "vault"), app.keystore, keys)
	entries, err := os.ReadDir(keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("keys directory has %d entries", len(entries))
	}
	grant := parseGrant(t, vault.text())

	box := startWizard(t, app.work, []string{"KEYSTORE_PASSWORD=" + keystorePass})
	wizardToKeystore(t, box, "", app.keystore)
	ctx := promptCtx(t, 30*time.Second)
	mustWait(t, box, ctx, "Nostr signer")
	mustSend(t, box, "\r")
	mustWait(t, box, ctx, "Bunker URL")
	signers := strings.Count(box.text(), "Nostr signer")
	mustSend(t, box, "\x1b")
	mustWaitMore(t, box, ctx, "Nostr signer", signers)
	urls := strings.Count(box.text(), "Bunker URL")
	mustSend(t, box, "\r")
	mustWaitMore(t, box, ctx, "Bunker URL", urls)
	mustSend(t, box, grant.signWith+"\r")
	mustWait(t, box, ctx, "Save this bunker URL")
	mustSend(t, box, "\r")
	mustWait(t, box, ctx, "Publish now")
	mustSend(t, box, "j\r")
	mustWait(t, box, ctx, "Nothing was published")
	endWizard(t, box, 0)

	info, err := os.Stat(filepath.Join(app.work, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode = %o", info.Mode().Perm())
	}
	body, err := os.ReadFile(filepath.Join(app.work, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "SIGN_WITH="+grant.signWith) {
		t.Fatal(".env does not contain the bunker URL")
	}
}

type releaseApp struct {
	id       string
	name     string
	work     string
	keystore string
	secret   string
	pubkey   string
}

func newReleaseApp(t *testing.T, id, name string) releaseApp {
	t.Helper()
	secret, pubkey := newKey(t)
	work := t.TempDir()
	keystore := filepath.Join(work, "release.jks")
	makeKeystore(t, keystore)
	return releaseApp{id: id, name: name, work: work, keystore: keystore, secret: secret, pubkey: pubkey}
}

func (a releaseApp) sign(t *testing.T, version int) string {
	t.Helper()
	unsigned := buildAppAPK(t, a.work, a.id, a.name, version)
	out := strings.TrimSuffix(unsigned, "-unsigned.apk") + ".apk"
	signWithKeystore(t, a.keystore, unsigned, out)
	return out
}

func (a releaseApp) yaml(t *testing.T, apk string) {
	t.Helper()
	body := fmt.Sprintf("release_source: %q\nname: %s\nsummary: Release test build\nmetadata_sources: []\n", apk, a.name)
	if err := os.WriteFile(filepath.Join(a.work, "zapstore.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newKey(t *testing.T) (secret, pubkey string) {
	t.Helper()
	hexKey := nostr.GeneratePrivateKey()
	var err error
	pubkey, err = nostr.GetPublicKey(hexKey)
	if err != nil {
		t.Fatal(err)
	}
	secret, err = nip19.EncodePrivateKey(hexKey)
	if err != nil {
		t.Fatal(err)
	}
	return secret, pubkey
}

func startWizard(t *testing.T, work string, extra []string) *console {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", previewPort))
	if err != nil {
		t.Fatalf("preview port %d is in use: %v", previewPort, err)
	}
	ln.Close()
	shim := filepath.Join(work, "path-shim")
	if err := os.MkdirAll(shim, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if err := os.WriteFile(filepath.Join(shim, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"TERM=xterm-256color",
		"NO_COLOR=1",
		"PATH=" + shim + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	if !hasEnv(extra, "RELAYS") {
		env = append(env, "RELAYS="+relayURL)
	}
	if !hasEnv(extra, "BLOSSOM_URL") {
		env = append(env, "BLOSSOM_URL="+blossomURL)
	}
	cmd := exec.Command(zspBin)
	cmd.Dir = work
	cmd.Env = childEnv(append(env, extra...))
	box, err := startConsole(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopProc(box) })
	return box
}

func hasEnv(env []string, key string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func wizardEnableCI(t *testing.T, box *console, npub string, atChoice bool) {
	t.Helper()
	ctx := promptCtx(t, 45*time.Second)
	if !atChoice {
		mustWait(t, box, ctx, "How should new releases")
	}
	mustSend(t, box, "j\r")
	mustWait(t, box, ctx, "CI public key")
	seen := strings.Count(box.text(), "How should new releases")
	mustSend(t, box, "\x1b")
	mustWaitMore(t, box, ctx, "How should new releases", seen)
	mustSend(t, box, "j\r")
	mustWait(t, box, ctx, "CI public key")
	waitNextSecond(t)
	mustSend(t, box, npub+"\r")
	mustWait(t, box, ctx, "CI/CD publishing enabled")
	endWizard(t, box, 0)
}

func waitNextSecond(t *testing.T) {
	t.Helper()
	start := time.Now().Unix()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for time.Now().Unix() == start {
		select {
		case <-ctx.Done():
			t.Fatal("clock did not advance")
		case <-tick.C:
		}
	}
}

func wizardSource(t *testing.T, box *console, source string) {
	t.Helper()
	ctx := promptCtx(t, 30*time.Second)
	mustWait(t, box, ctx, "Where is your APK?")
	if source == "" {
		mustSend(t, box, "\r")
		return
	}
	mustSend(t, box, source+"\r")
}

func wizardToKeystore(t *testing.T, box *console, source, keystore string) {
	t.Helper()
	wizardSource(t, box, source)
	mustWait(t, box, promptCtx(t, 30*time.Second), "Signing keystore")
	mustSend(t, box, keystore+"\r")
}

func wizardToChoice(t *testing.T, box *console, source, keystore string) {
	t.Helper()
	wizardToKeystore(t, box, source, keystore)
	mustWait(t, box, promptCtx(t, 30*time.Second), "Publish now")
}

func endWizard(t *testing.T, box *console, code int) {
	t.Helper()
	err := box.finish(promptCtx(t, 20*time.Second), false)
	if code == 0 {
		if err != nil {
			t.Fatalf("wizard exit %v\n%s", err, tail(box.text()))
		}
		return
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != code {
		t.Fatalf("wizard exit %v, want %d\n%s", err, code, tail(box.text()))
	}
}

func waitPreview(t *testing.T) {
	t.Helper()
	ctx := promptCtx(t, 60*time.Second)
	page := fmt.Sprintf("http://127.0.0.1:%d/", previewPort)
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := previewNonce(ctx, client, page); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

type branchCheck struct {
	APK struct {
		AppID string `json:"app_id"`
	} `json:"apk"`
	Signer     *string `json:"signer"`
	Authorized bool    `json:"authorized"`
	NextStep   *struct {
		Command       string `json:"command"`
		RequiresHuman bool   `json:"requires_human"`
	} `json:"next_step"`
}

func decodeCheck(t *testing.T, res cmdResult) branchCheck {
	t.Helper()
	if res.code != 0 {
		t.Fatalf("zsp check exit %d\n%s", res.code, res.stderr)
	}
	var doc branchCheck
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("check json: %v\n%s", err, res.stdout)
	}
	return doc
}

func publishArgs(t *testing.T, work string, env []string, args ...string) {
	t.Helper()
	full := append([]string{"publish", "-q", "--json", "--skip-preview"}, args...)
	res := runZSP(t, work, env, full...)
	if res.code != 0 {
		t.Fatalf("zsp publish %s exit %d\n%s", args, res.code, res.stderr)
	}
}

func publishCode(t *testing.T, work string, env []string, apk string) string {
	t.Helper()
	res := runZSP(t, work, env, "publish", "-q", "--json", "--skip-preview", "-s", apk)
	if res.code == 0 {
		t.Fatalf("zsp publish %s succeeded\n%s", apk, res.stdout)
	}
	return errorCode(t, res.stderr)
}

func appEventID(t *testing.T, id string) string {
	t.Helper()
	events := query(t, nostr.Filter{Kinds: []int{kindApp}, Tags: nostr.TagMap{"d": []string{id}}})
	if len(events) != 1 {
		t.Fatalf("app events = %d", len(events))
	}
	return events[0].ID
}

func proofDelegate(t *testing.T, pubkey string) string {
	t.Helper()
	events := query(t, nostr.Filter{Kinds: []int{kindProof}, Authors: []string{pubkey}})
	var tags []string
	for _, event := range events {
		tags = append(tags, fmt.Sprint(event.Tags))
		tag := event.Tags.GetFirst([]string{"delegation"})
		if tag != nil && len(*tag) >= 2 && (*tag)[1] != "" {
			return (*tag)[1]
		}
	}
	t.Fatalf("proofs %d, none delegate to %s: %v", len(events), pubkey, tags)
	return ""
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func copySigned(t *testing.T, src, dest string) {
	t.Helper()
	if err := copyFile(src, dest); err != nil {
		t.Fatal(err)
	}
}

func serveAPK(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/app.apk"
}
