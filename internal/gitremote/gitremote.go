// Package gitremote resolves a declared repository to the git remote that
// serves it, and asks that remote whether it is there.
package gitremote

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/zapstore/zsp/v2/internal/config"
	"github.com/zapstore/zsp/v2/internal/netpolicy"
	"github.com/zapstore/zsp/v2/internal/sanitize"
)

var (
	// ErrNotRepository marks a value that names no git remote.
	ErrNotRepository = errors.New("not a repository")

	// ErrGitMissing marks a probe that could not run: git itself, or the ngit
	// remote helper a NIP-34 repository needs, is not installed. The remote
	// was never asked, so nothing is known about it.
	ErrGitMissing = errors.New("git is not installed")

	// ErrUnreachable marks a remote that refused or did not answer a ref
	// listing.
	ErrUnreachable = errors.New("repository is unreachable")
)

// nostrScheme is the ngit remote helper's scheme: git runs
// git-remote-nostr for it.
const nostrScheme = "nostr://"

// Remote is the git remote behind a declared repository.
type Remote struct {
	// Repository is the canonical value for zapstore.yaml's repository: a
	// bare naddr for a NIP-34 repository, or a forge repository root.
	Repository string
	// URL is what git is asked about.
	URL string
	// Nostr is true for a NIP-34 repository, which only ngit can serve.
	Nostr bool
}

// Parse resolves value to the remote that serves it.
//
// A NIP-34 repository is a pubkey and an identifier, with optional relay
// hints. Every way of writing one is read for that pointer and reduced to its
// naddr: the naddr itself, ngit's `nostr://<npub>/<identifier>` with or
// without relay hints, and any viewer link carrying an naddr or an npub. The
// host is never consulted, so a viewer this code has never heard of works,
// and none of them serve git where they show a page. Any other value must be
// an HTTPS forge URL, reduced to its repository root, where the page a reader
// opens is also the remote.
func Parse(value string) (Remote, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Remote{}, fmt.Errorf("%w: no repository", ErrNotRepository)
	}
	if naddr, _, ok, err := config.ParseNostrRepository(value); ok {
		if err != nil {
			return Remote{}, fmt.Errorf("%w: %w", ErrNotRepository, err)
		}
		return Remote{Repository: naddr, URL: nostrScheme + naddr, Nostr: true}, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return Remote{}, fmt.Errorf("%w: %s is not an HTTPS repository URL", ErrNotRepository, sanitize.URL(value))
	}
	if err := config.ValidateURL(value); err != nil {
		return Remote{}, fmt.Errorf("%w: %w", ErrNotRepository, err)
	}
	// A listing's repository is read by everyone who sees the app, so a
	// loopback remote is never the repository it claims to be.
	if netpolicy.IsLoopbackHost(parsed.Hostname()) {
		return Remote{}, fmt.Errorf("%w: %s is not reachable by anyone else", ErrNotRepository, parsed.Host)
	}
	repository, _ := config.CanonicalForgeRepositoryURL(value, "")
	return Remote{Repository: repository, URL: repository}, nil
}

// Reachable reports whether the remote behind repository answers a ref
// listing. A reader's browser accepts any URL; this is the question git
// answers, and the only one that says a declared repository exists.
func Reachable(ctx context.Context, repository string) error {
	remote, err := Parse(repository)
	if err != nil {
		return err
	}
	return remote.Reachable(ctx)
}

// Timeout is how long a listing of r may take. ngit asks relays for the
// announcement and then the servers it names, and a relay that never answers
// is spent waiting, so it is given longer than a forge.
func (r Remote) Timeout() time.Duration {
	if r.Nostr {
		return 90 * time.Second
	}
	return 20 * time.Second
}

// Reachable lists r's refs. A repository with no branches or tags is
// reachable: it answered.
func (r Remote) Reachable(ctx context.Context) error {
	if _, err := exec.LookPath("git"); err != nil {
		return ErrGitMissing
	}
	dir := ""
	if r.Nostr {
		if _, err := exec.LookPath("git-remote-nostr"); err != nil {
			return fmt.Errorf("%w: ngit serves a NIP-34 repository", ErrGitMissing)
		}
		// git runs the ngit helper with the repository's GIT_DIR, and the
		// helper refuses to start without one, so the listing needs a
		// repository to run in even though it reads nothing from it.
		temporary, err := scratchRepository(ctx)
		if err != nil {
			return err
		}
		defer os.RemoveAll(temporary)
		dir = temporary
	}
	args := []string{"ls-remote", "--quiet", "--heads", "--tags", "--", r.URL}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Env = probeEnv(r.Nostr)
	out, err := command.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w: %w", ErrUnreachable, ctxErr)
		}
		return fmt.Errorf("%w: %s", ErrUnreachable, gitMessage(out, err))
	}
	return nil
}

// scratchRepository is an empty repository for a helper that needs a GIT_DIR.
func scratchRepository(ctx context.Context) (string, error) {
	dir, err := os.MkdirTemp("", "zsp-lsremote-*")
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, "git", "init", "--quiet", dir)
	command.Env = probeEnv(false)
	if out, err := command.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("%w: %s", ErrUnreachable, gitMessage(out, err))
	}
	return dir, nil
}

// probeEnv asks the question the listing needs answered: whether a stranger
// can read the repository. The developer's credentials are left out, because
// a repository only they can clone is not one a reader can. No prompt blocks
// the run, and git speaks only the protocols the remote needs, so a pasted
// value cannot turn a remote helper into a command.
func probeEnv(nostr bool) []string {
	protocols := "https"
	if nostr {
		protocols = "https:nostr"
	}
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_ALLOW_PROTOCOL="+protocols,
		"GIT_SSH_COMMAND=ssh -oBatchMode=yes",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
}

// gitMessage is what git said, in words a developer can act on. A forge that
// hides a private repository asks for a username rather than answering, and
// git reports the prompt it could not show; that is the refusal, so it is
// reported as what it means.
func gitMessage(out []byte, err error) string {
	line := ""
	for _, candidate := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(candidate); line != "" {
			break
		}
	}
	switch lowered := strings.ToLower(line); {
	case strings.Contains(lowered, "could not read username"),
		strings.Contains(lowered, "terminal prompts disabled"),
		strings.Contains(lowered, "authentication failed"):
		return "it asked for credentials, so it is private or does not exist"
	case line != "":
		return sanitize.Detail(line)
	default:
		return sanitize.ErrMessage(err)
	}
}
