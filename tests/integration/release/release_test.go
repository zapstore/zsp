//go:build release && !race

package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	nostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"

	"github.com/zapstore/zsp/v2/tests/integration/fixture"
)

const (
	kindApp     = 32267
	kindRelease = 30063
	kindAsset   = 3063
	kindProof   = 30509

	// previewPort is zsp's DefaultPreviewPort. The wizard always listens there.
	previewPort = 17008
)

var (
	zspBin       string
	hotboxBin    string
	relayURL     string
	blossomURL   string
	policySocket string
	stopRelayFn  = func() {}
)

func TestMain(m *testing.M) {
	code := 1
	defer func() { os.Exit(code) }()
	defer func() { stopRelayFn() }()
	zspPath, zspCleanup, err := fixture.ZSP()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer zspCleanup()
	hotboxPath, hotboxCleanup, err := fixture.Hotbox()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer hotboxCleanup()
	relayBin, relayCleanup, err := fixture.Relay()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer relayCleanup()
	zspBin = zspPath
	hotboxBin = hotboxPath
	relayURL, blossomURL, policySocket, stopRelayFn, err = startLocalRelay(relayBin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	code = m.Run()
}

func TestRelease(t *testing.T) {
	work := t.TempDir()
	keys := filepath.Join(work, "keys")
	if err := os.MkdirAll(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	keystore := filepath.Join(work, "release.jks")
	makeKeystore(t, keystore)
	vault := filepath.Join(work, fmt.Sprintf("e2e%d.hotbox", os.Getpid()))

	firstUnsigned := buildUnsignedAPK(t, work, 2)
	reference := filepath.Join(work, "keystore-signed.apk")
	signWithKeystore(t, keystore, firstUnsigned, reference)
	wantCert := certificateDigest(t, reference)

	box := createVault(t, work, vault, keystore, keys)
	if !strings.Contains(box.text(), "no passphrase and no key file") {
		t.Fatalf("vault was not passphrase-only:\n%s", tail(box.text()))
	}
	if entries, err := os.ReadDir(keys); err != nil || len(entries) != 0 {
		t.Fatalf("key directory = %v, %v; want empty", entries, err)
	}
	grant := parseGrant(t, box.text())

	writeDotEnv(t, work, grant)
	signed := signAPK(t, work, keys, firstUnsigned)
	if got := certificateDigest(t, signed); !strings.EqualFold(got, wantCert) {
		t.Fatalf("hotbox certificate %s, keystore certificate %s", got, wantCert)
	}
	writeProject(t, work, signed, grant)

	before := zspCheck(t, work)
	if before.Listed || before.Proof != nil || before.PublishedVersionCode != 0 {
		t.Fatalf("check before publish = %+v", before)
	}
	if before.NextStep == nil || before.NextStep.Command != "zsp publish" {
		t.Fatalf("next step before publish = %+v, want zsp publish", before.NextStep)
	}

	admitPublisher(t, grant.pubkey)
	runWizard(t, work)
	proofID, certHash := assertCatalog(t, grant.pubkey, 1, 1, 1)

	after := zspCheck(t, work)
	npub, err := nip19.EncodePublicKey(grant.pubkey)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Listed || !after.Authorized || after.PublishedVersionCode != 2 || after.APK.CertificateHash != certHash {
		t.Fatalf("check after wizard = %+v, cert %s", after, certHash)
	}
	if after.Signer == nil || *after.Signer != npub || after.Proof == nil || after.Proof.Owner != npub {
		t.Fatalf("check signer = %+v proof %+v, want %s", after.Signer, after.Proof, npub)
	}
	if after.NextStep != nil {
		t.Fatalf("next step after publish = %+v, want none", after.NextStep)
	}

	second := signAPK(t, work, keys, buildUnsignedAPK(t, work, 3))
	publishOK(t, work, second)
	again, _ := assertCatalog(t, grant.pubkey, 1, 2, 2)
	if again != proofID {
		t.Fatalf("proof changed from %s to %s", proofID, again)
	}

	downgrade := signAPK(t, work, keys, buildUnsignedAPK(t, work, 1))
	if code := publishErr(t, work, downgrade); code != "release_downgrade" {
		t.Fatalf("downgrade code = %s, want release_downgrade", code)
	}
	if code := publishErr(t, work, second); code != "release_already_published" {
		t.Fatalf("republish code = %s, want release_already_published", code)
	}
	if got, _ := assertCatalog(t, grant.pubkey, 1, 2, 2); got != proofID {
		t.Fatalf("proof changed after rejected publishes: %s", got)
	}

	envPath := filepath.Join(work, ".env")
	if err := os.Chmod(envPath, 0o644); err != nil {
		t.Fatal(err)
	}
	insecure := runZSP(t, work, nil, "check", "--json")
	if insecure.code == 0 || errorCode(t, insecure.stderr) != "insecure_env" {
		t.Fatalf("insecure .env exit=%d stderr=%s", insecure.code, insecure.stderr)
	}
	if err := os.Chmod(envPath, 0o600); err != nil {
		t.Fatal(err)
	}

	bad := filepath.Join(work, "other-unsigned.apk")
	if err := copyFile(firstUnsigned, bad); err != nil {
		t.Fatal(err)
	}
	wrong := runHotbox(t, work, keys, []string{"HOTBOX_PIN=" + strings.Repeat("a", 32), "HOTBOX_SOCKET=" + grant.socket}, "sign", bad)
	if wrong.code == 0 {
		t.Fatal("hotbox sign accepted a wrong PIN")
	}

	fourth := signAPK(t, work, keys, buildUnsignedAPK(t, work, 4))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	err = box.stop(ctx)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if code := publishErr(t, work, fourth); code != "signer_unavailable" && code != "temporary_failure" {
		t.Fatalf("publish with hotbox stopped = %s, want signer_unavailable or temporary_failure", code)
	}
	if got, _ := assertCatalog(t, grant.pubkey, 1, 2, 2); got != proofID {
		t.Fatalf("stopped hotbox still published, proof %s", got)
	}

	opened := openVault(t, work, vault, keys)
	reopened := parseGrant(t, opened.text())
	if reopened.pubkey != grant.pubkey || reopened.socket != grant.socket {
		t.Fatalf("reopened grant pubkey %s socket %s, want %s %s", reopened.pubkey, reopened.socket, grant.pubkey, grant.socket)
	}
	writeDotEnv(t, work, reopened)
	publishOK(t, work, fourth)
	if got, _ := assertCatalog(t, grant.pubkey, 1, 3, 3); got != proofID {
		t.Fatalf("proof after reopen = %s, want %s", got, proofID)
	}
}

type grant struct {
	signWith string
	pin      string
	socket   string
	pubkey   string
}

func createVault(t *testing.T, work, vault, keystore, keys string) *console {
	t.Helper()
	cmd := exec.Command(hotboxBin)
	cmd.Dir = work
	cmd.Env = childEnv([]string{"HOTBOX_KEYS=" + keys, "TERM=xterm-256color", "NO_COLOR=1"})
	box, err := startConsole(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopProc(box) })
	ctx := promptCtx(t, 20*time.Second)
	mustWait(t, box, ctx, "Vault path")
	mustSend(t, box, vault+"\r")
	mustWait(t, box, ctx, "Key storage")
	// G jumps to the last row, Passphrase only, on either default.
	mustSend(t, box, "G\r")
	mustWait(t, box, ctx, "Nostr private key")
	mustSend(t, box, "\r")
	mustWait(t, box, ctx, "Vault password")
	mustSend(t, box, "\r")
	mustWait(t, box, ctx, "Confirm password")
	mustSend(t, box, "\r")
	mustWait(t, box, ctx, "Android keystore")
	mustSend(t, box, keystore+"\r")
	mustWait(t, box, ctx, "Keystore password")
	mustSend(t, box, keystorePass+"\r")
	switch mustWaitAny(t, box, ctx, "Key password", "Seal this certificate?") {
	case "Key password":
		mustSend(t, box, keystorePass+"\r")
		mustWait(t, box, ctx, "Seal this certificate?")
	}
	mustSend(t, box, "\r")
	mustWait(t, box, promptCtx(t, 30*time.Second), "SIGN_WITH=")
	return box
}

func openVault(t *testing.T, work, vault, keys string) *pipeProc {
	t.Helper()
	cmd := exec.Command(hotboxBin, vault)
	cmd.Dir = work
	cmd.Env = childEnv([]string{"HOTBOX_KEYS=" + keys})
	proc, err := startPipe(cmd, "\n")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopProc(proc) })
	if err := proc.wait(promptCtx(t, 30*time.Second), "SIGN_WITH="); err != nil {
		t.Fatal(err)
	}
	return proc
}

func runWizard(t *testing.T, work string) {
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
	open := filepath.Join(shim, "open")
	if runtime.GOOS != "darwin" {
		open = filepath.Join(shim, "xdg-open")
	}
	script := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(open, script, 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(zspBin)
	cmd.Dir = work
	cmd.Env = childEnv([]string{
		"RELAYS=" + relayURL,
		"BLOSSOM_URL=" + blossomURL,
		"TERM=xterm-256color",
		"NO_COLOR=1",
		"PATH=" + shim + string(os.PathListSeparator) + os.Getenv("PATH"),
	})
	box, err := startConsole(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopProc(box) })
	ctx := promptCtx(t, 45*time.Second)
	mustWait(t, box, ctx, "Where is your APK?")
	mustSend(t, box, "\r")
	mustWait(t, box, ctx, "Hotbox signs the proof")
	mustWait(t, box, ctx, "Publish now")
	mustSend(t, box, "\r")
	if err := approvePreview(promptCtx(t, 60*time.Second)); err != nil {
		t.Fatalf("approve preview: %v\n%s", err, tail(box.text()))
	}
	mustWait(t, box, ctx, "How should new releases")
	mustSend(t, box, "\r")
	if err := box.finish(promptCtx(t, 20*time.Second), false); err != nil {
		t.Fatalf("wizard: %v\n%s", err, tail(box.text()))
	}
	if !strings.Contains(box.text(), "Release published") && !strings.Contains(box.text(), "Release pending") {
		t.Fatalf("wizard did not publish:\n%s", tail(box.text()))
	}
}

func stopProc(p interface{ stop(context.Context) error }) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = p.stop(ctx)
}

var noncePattern = regexp.MustCompile(`const sessionNonce = "([0-9a-f]+)"`)

func approvePreview(ctx context.Context) error {
	return decidePreview(ctx, "/api/approve")
}

func decidePreview(ctx context.Context, path string) error {
	page := fmt.Sprintf("http://127.0.0.1:%d/", previewPort)
	origin := fmt.Sprintf("http://127.0.0.1:%d", previewPort)
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		nonce, err := previewNonce(ctx, client, page)
		if err == nil {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, nil)
			if err != nil {
				return err
			}
			req.Header.Set("Origin", origin)
			req.Header.Set("X-Session-Nonce", nonce)
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				return fmt.Errorf("status %d", resp.StatusCode)
			}
			return nil
		}
		if !errors.Is(err, errPreviewNotReady) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

var errPreviewNotReady = errors.New("preview is not ready")

func previewNonce(ctx context.Context, client *http.Client, page string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errPreviewNotReady
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	match := noncePattern.FindSubmatch(body)
	if len(match) != 2 {
		return "", errPreviewNotReady
	}
	return string(match[1]), nil
}

func signAPK(t *testing.T, work, keys, unsigned string) string {
	t.Helper()
	res := runHotbox(t, work, keys, nil, "sign", unsigned)
	if res.code != 0 {
		t.Fatalf("hotbox sign %s exit %d\n%s", unsigned, res.code, res.stderr)
	}
	out := strings.TrimSuffix(unsigned, "-unsigned.apk") + ".apk"
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("signed apk %s: %v\n%s", out, err, res.stdout)
	}
	return out
}

func admitPublisher(t *testing.T, pubkey string) {
	t.Helper()
	conn, err := net.Dial("unix", policySocket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(struct {
		Op   string `json:"op"`
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{Op: "allow", Kind: "nostr", ID: pubkey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("admit %s: %s", pubkey, resp.Error)
	}
}

func writeProject(t *testing.T, work, apk string, g grant) {
	t.Helper()
	writeDotEnv(t, work, g)
	body := fmt.Sprintf("release_source: %q\nname: Release Test\nsummary: Release test build\nmetadata_sources: []\n", apk)
	if err := os.WriteFile(filepath.Join(work, "zapstore.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDotEnv(t *testing.T, work string, g grant) {
	t.Helper()
	body := fmt.Sprintf("SIGN_WITH='%s'\nHOTBOX_PIN='%s'\nHOTBOX_SOCKET='%s'\n", g.signWith, g.pin, g.socket)
	path := filepath.Join(work, ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func parseGrant(t *testing.T, text string) grant {
	t.Helper()
	g := grant{
		signWith: envLine(text, "SIGN_WITH"),
		pin:      envLine(text, "HOTBOX_PIN"),
		socket:   envLine(text, "HOTBOX_SOCKET"),
	}
	if g.signWith == "" || len(g.pin) != 32 || g.socket == "" {
		t.Fatalf("hotbox grant sign=%t pin=%d socket=%t\n%s", g.signWith != "", len(g.pin), g.socket != "", tail(text))
	}
	parsed, err := url.Parse(g.signWith)
	if err != nil || parsed.Scheme != "bunker" || !nostr.IsValidPublicKey(parsed.Host) {
		t.Fatalf("SIGN_WITH is not a bunker URL: %v", err)
	}
	g.pubkey = parsed.Host
	return g
}

func envLine(text, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		value, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		}
		return value
	}
	return ""
}

type checkDoc struct {
	Listed               bool    `json:"listed"`
	Authorized           bool    `json:"authorized"`
	PublishedVersionCode int64   `json:"published_version_code"`
	Signer               *string `json:"signer"`
	APK                  struct {
		CertificateHash string `json:"certificate_hash"`
	} `json:"apk"`
	Proof *struct {
		Owner string `json:"owner"`
	} `json:"proof"`
	NextStep *struct {
		Command string `json:"command"`
	} `json:"next_step"`
}

func zspCheck(t *testing.T, work string) checkDoc {
	t.Helper()
	res := runZSP(t, work, []string{"RELAYS=" + relayURL, "BLOSSOM_URL=" + blossomURL}, "check", "--json")
	if res.code != 0 {
		t.Fatalf("zsp check exit %d\n%s", res.code, res.stderr)
	}
	var doc checkDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("check json: %v\n%s", err, res.stdout)
	}
	return doc
}

func publishOK(t *testing.T, work, apk string) {
	t.Helper()
	res := runZSP(t, work, []string{"RELAYS=" + relayURL, "BLOSSOM_URL=" + blossomURL},
		"publish", "-q", "--json", "--skip-preview", "-s", apk)
	if res.code != 0 {
		t.Fatalf("zsp publish %s exit %d\n%s", apk, res.code, res.stderr)
	}
	var doc struct {
		OK     bool   `json:"ok"`
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("publish json: %v\n%s", err, res.stdout)
	}
	if !doc.OK || doc.ID == "" || (doc.Status != "published" && doc.Status != "pending") {
		t.Fatalf("publish result = %+v", doc)
	}
}

func publishErr(t *testing.T, work, apk string) string {
	t.Helper()
	res := runZSP(t, work, []string{"RELAYS=" + relayURL, "BLOSSOM_URL=" + blossomURL},
		"publish", "-q", "--json", "--skip-preview", "-s", apk)
	if res.code == 0 {
		t.Fatalf("zsp publish %s succeeded\n%s", apk, res.stdout)
	}
	return errorCode(t, res.stderr)
}

func errorCode(t *testing.T, stderr string) string {
	t.Helper()
	var doc struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &doc); err != nil {
		t.Fatalf("error json: %v\n%s", err, stderr)
	}
	return doc.Error.Code
}

type cmdResult struct {
	stdout string
	stderr string
	code   int
}

func runZSP(t *testing.T, dir string, extra []string, args ...string) cmdResult {
	t.Helper()
	return runCmd(t, dir, extra, zspBin, args...)
}

func runHotbox(t *testing.T, dir, keys string, extra []string, args ...string) cmdResult {
	t.Helper()
	base := []string{"HOTBOX_KEYS=" + keys}
	return runCmd(t, dir, append(base, extra...), hotboxBin, args...)
}

func runCmd(t *testing.T, dir string, extra []string, bin string, args ...string) cmdResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = childEnv(extra)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return cmdResult{stdout: stdout.String(), stderr: stderr.String()}
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return cmdResult{stdout: stdout.String(), stderr: stderr.String(), code: exit.ExitCode()}
}

func childEnv(extra []string) []string {
	drop := map[string]bool{
		"SIGN_WITH": true, "HOTBOX_PIN": true, "HOTBOX_SOCKET": true,
		"HOTBOX_KEYS": true, "RELAYS": true, "BLOSSOM_URL": true,
		"KEYSTORE_PASSWORD": true, "KEYSTORE_KEY_PASSWORD": true,
	}
	out := make([]string, 0, len(os.Environ())+len(extra))
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok && drop[key] {
			continue
		}
		out = append(out, item)
	}
	return append(out, extra...)
}

func assertCatalog(t *testing.T, pubkey string, apps, releases, assets int) (proofID, certHash string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var applications, releaseEvents, assetEvents, proofs []*nostr.Event
	for {
		applications = query(t, nostr.Filter{Kinds: []int{kindApp}, Tags: nostr.TagMap{"d": []string{appID}}})
		releaseEvents = query(t, nostr.Filter{Kinds: []int{kindRelease}, Authors: []string{pubkey}, Tags: nostr.TagMap{"i": []string{appID}}})
		assetEvents = query(t, nostr.Filter{Kinds: []int{kindAsset}, Authors: []string{pubkey}, Tags: nostr.TagMap{"i": []string{appID}}})
		proofs = query(t, nostr.Filter{Kinds: []int{kindProof}, Authors: []string{pubkey}})
		if len(applications) == apps && len(releaseEvents) == releases && len(assetEvents) == assets && len(proofs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("catalog apps=%d releases=%d assets=%d proofs=%d, want %d %d %d 1",
				len(applications), len(releaseEvents), len(assetEvents), len(proofs), apps, releases, assets)
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, event := range append(append(append([]*nostr.Event{}, applications...), releaseEvents...), append(assetEvents, proofs...)...) {
		ok, err := event.CheckSignature()
		if err != nil || !ok {
			t.Fatalf("event %s kind %d signature: %v", event.ID, event.Kind, err)
		}
	}
	proof := proofs[0]
	if proof.PubKey != pubkey {
		t.Fatalf("proof pubkey %s, want %s", proof.PubKey, pubkey)
	}
	for _, tag := range proof.Tags {
		if len(tag) > 0 && tag[0] == "cert" {
			t.Fatalf("proof has a cert tag: %v", tag)
		}
	}
	d := proof.Tags.GetFirst([]string{"d"})
	if d == nil || len(*d) < 2 || (*d)[1] == "" {
		t.Fatal("proof has no d tag")
	}
	return proof.ID, (*d)[1]
}

// queryUntil polls until the relay serves want events. Promotion follows
// the APK read, so a publish that just returned can still be absent.
func queryUntil(t *testing.T, filter nostr.Filter, want int) []*nostr.Event {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var events []*nostr.Event
	for {
		events = query(t, filter)
		if len(events) == want {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay events = %d, want %d", len(events), want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func query(t *testing.T, filter nostr.Filter) []*nostr.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	events, err := relay.QuerySync(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func promptCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	t.Cleanup(cancel)
	return ctx
}

func mustWait(t *testing.T, box *console, ctx context.Context, needle string) {
	t.Helper()
	if err := box.wait(ctx, needle); err != nil {
		logs := ""
		if relayLogs != nil {
			logs = tail(relayLogs.String())
		}
		t.Fatalf("%v\n%s\nrelay:\n%s", err, tail(box.text()), logs)
	}
}

func mustWaitMore(t *testing.T, box *console, ctx context.Context, needle string, seen int) {
	t.Helper()
	if err := box.waitMore(ctx, needle, seen); err != nil {
		logs := ""
		if relayLogs != nil {
			logs = tail(relayLogs.String())
		}
		t.Fatalf("%v\n%s\nrelay:\n%s", err, tail(box.text()), logs)
	}
}

func mustWaitAny(t *testing.T, box *console, ctx context.Context, needles ...string) string {
	t.Helper()
	got, err := box.waitAny(ctx, needles...)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustSend(t *testing.T, box *console, keys string) {
	t.Helper()
	if err := box.send(keys); err != nil {
		t.Fatal(err)
	}
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
