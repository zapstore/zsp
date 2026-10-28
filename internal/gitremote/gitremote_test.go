package gitremote

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr/nip19"
)

// sampleNaddr is a kind 30617 repository announcement.
const sampleNaddr = "naddr1qvzqqqrhnypzqg88e9f588nx7wyz4wt0ura23w3yd4hmztqrkr95utwyvgachjepqyd8wumn8ghj7emfwsh8x6rpddjhxur9v9ex2tnyd9uj7qq22d4kzar92dcx7arnhyhdwn"

// TestParseResolvesNIP34Repository confirms an naddr names the same remote
// wherever it is written: no viewer serves git at the address it shows.
func TestParseResolvesNIP34Repository(t *testing.T) {
	for _, value := range []string{
		sampleNaddr,
		"nostr://" + sampleNaddr,
		"https://nostrhub.io/" + sampleNaddr,
		"https://nostrhub.io/" + sampleNaddr + "/info/refs",
		"https://njump.me/" + strings.ToUpper(sampleNaddr),
	} {
		got, err := Parse(value)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", value, err)
		}
		if !got.Nostr || got.Repository != sampleNaddr || got.URL != "nostr://"+sampleNaddr {
			t.Errorf("Parse(%q) = %+v", value, got)
		}
	}
}

// TestParseReadsNIP34PointerFromAnyForm confirms a NIP-34 repository is read
// for its pointer however it is written, and reduced to one naddr. The host
// is never consulted, so a viewer this code has never heard of works.
func TestParseReadsNIP34PointerFromAnyForm(t *testing.T) {
	const npub = "npub1w4uswmv6lu9yel005l3qgheysmr7tk9uvwluddznju3nuxalevvs2d0jr5"
	// The pointer with its relay hint, and the same pointer without one.
	const withRelay = "naddr1qqxxummnw3ez6ct8v4h8gucpz3mhxue69uhhyetvv9ujumn8d96zuer9wcpzqateqake4lc2fn77lflzq30jfpk8uhvtccalc66989er8cdmljceqvzqqqrhnyvqrmeh"
	const bare = "naddr1qqxxummnw3ez6ct8v4h8guczyp6hjpmdntls5n8aa7n7ypzlyjrv0ewch33ml3452wtjx0smhl93jqcyqqq80xgsnrcqx"
	tests := []struct {
		value string
		want  string
	}{
		{"https://gitworkshop.dev/" + npub + "/relay.ngit.dev/nostr-agents", withRelay},
		{"https://gitworkshop.dev/" + npub + "/relay.ngit.dev/nostr-agents/issues", withRelay},
		{"nostr://" + npub + "/relay.ngit.dev/nostr-agents", withRelay},
		{"nostr://" + npub + "/nostr-agents", bare},
		{npub + "/nostr-agents", bare},
		{"https://example.invalid/anything/" + npub + "/nostr-agents", bare},
	}
	for _, test := range tests {
		got, err := Parse(test.value)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", test.value, err)
		}
		if !got.Nostr || got.Repository != test.want || got.URL != "nostr://"+test.want {
			t.Errorf("Parse(%q) = %+v, want %s", test.value, got, test.want)
		}
	}
}

// TestParseRejectsPointerWithoutRepository confirms an npub that names no
// repository says what is missing. A viewer's own short link, which only that
// viewer can resolve, is not a pointer at all.
func TestParseRejectsPointerWithoutRepository(t *testing.T) {
	const npub = "npub1w4uswmv6lu9yel005l3qgheysmr7tk9uvwluddznju3nuxalevvs2d0jr5"
	_, err := Parse("https://gitworkshop.dev/" + npub)
	if !errors.Is(err, ErrNotRepository) || !strings.Contains(err.Error(), "identifier") {
		t.Errorf("Parse() error = %v, want the missing identifier named", err)
	}
	got, err := Parse("https://gitworkshop.dev/r/xm-arcade")
	if err != nil || got.Nostr {
		t.Errorf("Parse() = %+v, %v, want a plain URL", got, err)
	}
}

// TestParseCanonicalizesForgeURL confirms a releases page and a mixed-case
// path reduce to the repository root git is asked about.
func TestParseCanonicalizesForgeURL(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"https://github.com/GreenArt7c3/Amber/releases", "https://github.com/greenart7c3/amber"},
		{"https://github.com/owner/app", "https://github.com/owner/app"},
		{"https://github.com/owner/app/tree/main/app", "https://github.com/owner/app"},
		{"https://codeberg.org/tkuenneth/mintime.git", "https://codeberg.org/tkuenneth/mintime"},
		{"https://gitlab.com/group/subgroup/app/-/releases", "https://gitlab.com/group/subgroup/app"},
		{"https://framagit.org/android1/etagere", "https://framagit.org/android1/etagere"},
		{"https://example.com/owner/app", "https://example.com/owner/app"},
	}
	for _, test := range tests {
		got, err := Parse(test.value)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", test.value, err)
		}
		if got.Nostr || got.Repository != test.want || got.URL != test.want {
			t.Errorf("Parse(%q) = %+v, want %q", test.value, got, test.want)
		}
	}
}

// TestParseRejectsValuesThatNameNoRemote keeps a value git cannot be asked
// about out of zapstore.yaml.
func TestParseRejectsValuesThatNameNoRemote(t *testing.T) {
	otherKind, err := nip19.EncodeEntity(strings.Repeat("a", 64), 30023, "article", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"",
		"   ",
		"github.com/owner/app",
		"http://github.com/owner/app",
		"https://user:pass@github.com/owner/app",
		"https://github.com/owner/app#readme",
		"https://localhost/owner/app",
		"ext::sh -c id",
		otherKind,
	} {
		if _, err := Parse(value); !errors.Is(err, ErrNotRepository) {
			t.Errorf("Parse(%q) error = %v, want ErrNotRepository", value, err)
		}
	}
}

// TestReachableListsRefs confirms the probe asks git about the canonical
// remote and accepts a repository that answers.
func TestReachableListsRefs(t *testing.T) {
	record := filepath.Join(t.TempDir(), "args")
	fakeGit(t, "printf '%s\\n' \"$*\" >"+record+"\nexit 0\n")
	if err := Reachable(t.Context(), "https://github.com/Owner/App/releases"); err != nil {
		t.Fatalf("Reachable() error = %v", err)
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); got != "ls-remote --quiet --heads --tags -- https://github.com/owner/app" {
		t.Errorf("git args = %q", got)
	}
}

// TestReachableAsksNgitForNIP34 confirms a viewer URL is probed as the ngit
// remote, not as the page a reader opens, and that the listing runs inside a
// repository: git runs the helper with a GIT_DIR, and the helper refuses to
// start without one.
func TestReachableAsksNgitForNIP34(t *testing.T) {
	record := filepath.Join(t.TempDir(), "args")
	dir := fakeGit(t, "printf '%s\\n' \"$*\" >>"+record+"\nexit 0\n")
	writeScript(t, filepath.Join(dir, "git-remote-nostr"), "exit 0\n")
	if err := Reachable(t.Context(), "https://nostrhub.io/"+sampleNaddr); err != nil {
		t.Fatalf("Reachable() error = %v", err)
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "init --quiet ") {
		t.Fatalf("git calls = %q, want a repository for the helper", args)
	}
	if !strings.HasPrefix(calls[1], "-C ") || !strings.HasSuffix(calls[1], "-- nostr://"+sampleNaddr) {
		t.Errorf("git args = %q", calls[1])
	}
}

// TestReachableReportsRefusal keeps the message git wrote, which is what
// tells a developer whether the URL is wrong or the repository is private.
func TestReachableReportsRefusal(t *testing.T) {
	fakeGit(t, "echo \"remote: Repository not found.\" >&2\nexit 128\n")
	err := Reachable(t.Context(), "https://github.com/owner/app")
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Reachable() error = %v, want ErrUnreachable", err)
	}
	if !strings.Contains(err.Error(), "Repository not found") {
		t.Errorf("Reachable() error = %v, want git's message", err)
	}
}

// TestReachableReadsACredentialPromptAsRefusal confirms the message a forge
// that hides a private repository produces is reported as what it means. The
// probe carries no credentials, so this is also what a private repository of
// the developer's own looks like.
func TestReachableReadsACredentialPromptAsRefusal(t *testing.T) {
	fakeGit(t, "echo \"fatal: could not read Username for 'https://github.com': terminal prompts disabled\" >&2\nexit 128\n")
	err := Reachable(t.Context(), "https://github.com/owner/app")
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Reachable() error = %v, want ErrUnreachable", err)
	}
	if !strings.Contains(err.Error(), "private") {
		t.Errorf("Reachable() error = %v, want the refusal in plain words", err)
	}
}

// TestReachableWithoutGitSaysNothingWasAsked separates a probe that could not
// run from a repository that refused.
func TestReachableWithoutGitSaysNothingWasAsked(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := Reachable(t.Context(), "https://github.com/owner/app"); !errors.Is(err, ErrGitMissing) {
		t.Errorf("Reachable() error = %v, want ErrGitMissing", err)
	}
}

// TestReachableWithoutNgitSaysNothingWasAsked confirms a NIP-34 repository is
// not called unreachable when the helper that serves it is absent.
func TestReachableWithoutNgitSaysNothingWasAsked(t *testing.T) {
	fakeGit(t, "exit 0\n")
	err := Reachable(t.Context(), sampleNaddr)
	if !errors.Is(err, ErrGitMissing) {
		t.Fatalf("Reachable() error = %v, want ErrGitMissing", err)
	}
	if !strings.Contains(err.Error(), "ngit") {
		t.Errorf("Reachable() error = %v, want ngit named", err)
	}
}

// fakeGit puts a git on PATH that runs body, and returns its directory.
func fakeGit(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeScript(t, filepath.Join(dir, "git"), body)
	t.Setenv("PATH", dir)
	return dir
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
}
