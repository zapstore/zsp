# ZSP

`zsp` is a CLI and a public Go library that fetches, verifies, and publishes
Android app releases to a catalog. It follows [NIP-82](../product/nips/82.md)
and [NIP-C1](../product/nips/C1.md). It uploads files to Blossom and publishes
signed events to relays. [Catalog](../product/specs/catalog.md) defines the
listing it publishes. The [indexer](../indexer/SPEC.md) uses the library.
The [enricher](../steroid/SPEC.md), not `zsp`, writes about, security, and
vector.

This is a clean implementation. Except for `zapstore.yaml`, it keeps nothing
from the previous CLI. Not included: offline or unsigned output,
`has-new-release`, a human `identity` command, proof revocation,
configuration migration, `--indexer-mode`,
`--skip-certificate-linking`, `--app-created-at-release`, browser or npub
signing. HTTP validators are kept between runs; see [Sources](#sources).

A **delegate key** is the optional NIP-C1 delegate. It has exactly the
authority NIP-C1 gives it.

## Configuration and secrets

Settings come from the process environment, then `.env` in the current
working directory, never a parent directory.

| Variable | Meaning | Default |
| --- | --- | --- |
| `SIGN_WITH` | `nsec1…` or `bunker://`; hex keys are refused | required to sign |
| `RELAYS` | comma-separated WebSocket URLs or Unix socket paths | `wss://relay.zapstore.dev` |
| `BLOSSOM_URL` | Blossom server for media, and for an APK with no public source | `https://cdn.zapstore.dev` |
| `KEYSTORE_PASSWORD`, `KEYSTORE_KEY_PASSWORD` | keystore passwords for the wizard | — |
| `HOTBOX_SOCKET`, `HOTBOX_PIN` | hotbox socket and PIN; `publish` creates or renews the C1 proof through them | — |
| `GITHUB_TOKEN` | GitHub API token, sent only to `github.com`, `api.github.com`, and `raw.githubusercontent.com` | — |
| `GITEA_TOKEN`, `GITEA_HOST` | Gitea or Forgejo token, sent only to that exact host | host `codeberg.org` |
| `GITLAB_TOKEN`, `GITLAB_HOST` | GitLab token (`PRIVATE-TOKEN`), sent only to that exact host | host `gitlab.com` |

A token is never sent to a host because its name looks like a forge, and
never through the Tor retry.

A `.env` in the working directory that holds `SIGN_WITH` or `HOTBOX_PIN` and
is readable by group or others stops every command with `insecure_env`. A
`.env` that exists but cannot be read or parsed stops every command with
`invalid_env`.

`SIGN_WITH` is the only way to pass a signer; there are no signer flags and
no browser flow.

Private keys, passwords, `HOTBOX_PIN`, `nsec` values, bunker URLs, and
authorization events never appear in arguments, logs, errors, JSON, or summaries. Error
messages never expose private temporary paths.

For display only, `zsp` treats the `pubkey` in a relay's NIP-11 document as
that catalog's key. It hardcodes no key. This grants no authority.

## Commands

```text
zsp
zsp check [options] [zapstore.yaml | app.apk]
zsp publish [options] [zapstore.yaml]
zsp skill
```

Bare `zsp` is the [wizard](#wizard), the only interactive command. The others
never prompt for input. They take input from arguments, configuration, and
the variables above. When stdout is a terminal they print readable text;
when it is not, or with `--json`, they emit one complete JSON document:
success on stdout, failure on stderr. Agents and CI get JSON without asking.
Help and version output stay human-readable. Exit codes are `0`, `1`, and
`130` (cancelled). Long-running work respects cancellation: the first
interrupt cancels and cleans up; a second one exits at once. Flags may follow
positional arguments; `--` ends flags.

`check` is read-only: what an agent runs first, and what anyone runs to
learn why an app is or isn't on Zapstore. It loads `./zapstore.yaml`, the
one config path it is given, or, given a path ending in `.apk`, that APK
alone as `release_source`. It finds and verifies the APK like `publish`,
then asks `RELAYS` whether the app is listed, which proof is active for its
certificate, and the greatest published `version_code` by the proof's owner
or delegate on the channel. With `SIGN_WITH` set it resolves the signer's
public key and says whether the proof authorizes it; it signs nothing. It
uploads nothing, publishes nothing, and fetches no metadata. Its result
ends with at most one next step.

`publish` loads `./zapstore.yaml`, or the one config path it is given. An
APK is not an argument. A local build sets `release_source` to that file.

Source flags, for `check` and `publish`, override fields in the loaded
file; they do not replace it:

- `-r`, `-s`, `--match`, `--release-filter`, `--channel`,
  `--prerelease-channel`: override `repository`, `release_source`, `match`,
  `release_filter`, `channel`, and `prerelease_channel`, with the same
  validation. `--prerelease-channel` without `channel` uses the prerelease
  channel.
- `--apk-hash`: select one APK when several verified ones remain.

`publish` options:

- repeated `-m`: override `metadata_sources`.
- `-q`, `--quiet`: no progress output and no preview.
- `--skip-preview`: no browser preview. Without a terminal there is none.
- `--overwrite-release`: allow an equal `version_code`.
- `--overwrite-app-event`: publish the kind `32267` even when unchanged.
- `--app-created-at-release`: set kind `32267` `created_at` to the source
  release time. Otherwise it is the publication time.
- `--skip-metadata`: no remote metadata; configuration and APK facts still
  fill the app event.
- `--skip-app-event`: omit kind `32267` and its media.
- `--no-compress`: upload media bytes unchanged.

`publish` requires an active NIP-C1 proof that authorizes the signer. With
`HOTBOX_SOCKET` and `HOTBOX_PIN` set, it creates or renews the signer's own
proof in the same run (see Proof check). Otherwise it never creates one; the
wizard does.

`skill` prints the [agent skill](#agent-publishing-skill).

### Output

JSON holds only what a caller acts on. Hashes are named `hash` (the APK
file) and `certificate_hash`, lowercase hex. Public keys are npubs. Times
are RFC 3339.

```json
{
  "ok": true,
  "operation": "check",
  "apk": {
    "app_id": "com.example.app",
    "name": "Example",
    "version_name": "1.2.0",
    "version_code": 120,
    "certificate_hash": "<hex>",
    "hash": "<hex>"
  },
  "listed": true,
  "proof": { "owner": "npub1…", "delegate": "npub1…", "expires_at": "2028-10-12T00:00:00Z" },
  "published_version_code": 110,
  "signer": "npub1…",
  "authorized": true,
  "next_step": { "command": "zsp publish", "reason": "Publish version 1.2.0 (120).", "requires_human": false },
  "warnings": []
}
```

`proof` is null when no proof is active; `delegate` is omitted without one.
`signer` is null without `SIGN_WITH`, and `authorized` is then false.
`published_version_code` is 0 when nothing is published. `next_step` is
null when there is nothing to do. The first that applies:

1. No active proof, or the signer's own proof expires within 30 days: with
   hotbox and `SIGN_WITH`, `zsp publish`, which creates or renews it in the
   run; otherwise `zsp`, requiring a human.
2. The APK's `version_code` is not greater than the published one: null.
3. No `SIGN_WITH`, or a signer the proof doesn't authorize: `zsp`,
   requiring a human, who sets `SIGN_WITH` or names the key as delegate.
   When the app is listed and `RELAYS` includes relay.zapstore.dev, the
   reason adds that Zapstore publishes the release on its next visit.
4. Otherwise `zsp publish`.

```json
{
  "ok": true,
  "operation": "publish",
  "id": "<release-event-id>",
  "status": "published",
  "app_id": "com.example.app",
  "version_name": "1.2.0",
  "version_code": 120,
  "warnings": []
}
```

Failure has `ok: false` and a stable error object. Callers branch on
`error.code`, never on `summary`.

```json
{
  "ok": false,
  "operation": "publish",
  "error": {
    "code": "proof_required",
    "summary": "No active proof was found for this certificate.",
    "retryable": false,
    "context": { "certificate_hash": "<hex>" },
    "next_step": { "command": "zsp", "reason": "Prove the app is yours.", "requires_human": true }
  }
}
```

| Sentinel | Code |
| --- | --- |
| `ErrInvalidConfig` | `invalid_arguments` |
| `ErrSourceFailed` | `source_failed` |
| `ErrTooManyCandidates` | `too_many_candidates` |
| `ErrNoAPK` | `no_apk` |
| `ErrNoNewAPK` | `no_new_apk` |
| `ErrInvalidAPK` | `invalid_apk` |
| `ErrAPKNotChecked` | `apk_not_checked` |
| `ErrAPKSelectionRequired` | `apk_selection_required` |
| `ErrProofRequired` | `proof_required` |
| `ErrProofUnauthorized` | `proof_unauthorized` |
| `ErrSigner` | `signer_unavailable` |
| `ErrAlreadyPublished` | `release_already_published` |
| `ErrReleaseDowngrade` | `release_downgrade` |
| `ErrUploadRejected` | `upload_rejected` |
| `ErrPublishRejected` | `publish_rejected` |
| `ErrRateLimited` | `rate_limited` |
| `ErrTemporaryFailure` | `temporary_failure` |

`cancelled` is CLI-only, exit `130`. `insecure_env` and `invalid_env` are
CLI-only, exit `1`.
`apk_selection_required` lists each candidate's `filename`, `hash`,
`version_code`, and `certificate_hash` in `context.candidates`, and the
`--apk-hash` remedy. If publication fails after an upload or relay request
completed, the error document also has `result` with `status` `partial` or
`failed` and the release `id` when it was signed.

## Wizard

The wizard takes a developer from an APK to a claimed, published app. It
does the two things an agent must never do: open the signing keystore and
hold the Nostr signer. It keeps source configuration, verified APKs,
certificate data, relay results, and a resolved signer for one run. It
writes a basic `./zapstore.yaml`: the repository or release source, and
`metadata_sources` when the developer chooses them. It does not write
gathered name, description, or media. An agent can improve the file
afterward. It may also save a bunker URL in `.env`, after an explicit prompt.

It opens with `Publish your Android app on Zapstore and prove it's yours.`

```text
📦 Add your app → 🔐 Prove it's yours → 🚀 Publish
```

It starts with the app, not a keystore, a Nostr key, or a proof. The
verified APK certificate is the only identity anchor. The wizard checks
before it asks: it asks for a keystore and a signer only to create, renew,
or delegate a proof, or to publish. It repeats no work unless the source,
APK, certificate, or signer changes. Its words are for a developer uploading
a first app; it names NIP-C1 nowhere. The proof is the goal. Publishing
from the wizard happens only when an unlisted app needs its first release
to carry the proof, and CI/CD comes last, for advanced users.

The wizard sends no suggestion: everyone in it is a developer, a listed
app is already tracked, and an unlisted app's first release carries its
`repository`.

At startup, when some relays in `RELAYS` are unreachable, the wizard names
the unreachable ones and the ones it continues with.

Esc on a prompt reached from another prompt in the same step returns to
that prompt. Esc on any other prompt does nothing. Ctrl-C quits.

### Add your app

```text
Where is your APK?
Paste a releases page (GitHub, GitLab, Codeberg, F-Droid…) or the path to an APK file.
```

A forge repository or releases page, an F-Droid package URL, a direct APK
URL, and a local APK file or directory are all accepted. A forge URL becomes
`repository`; anything else becomes `release_source`.

When `./zapstore.yaml` exists, the wizard loads it, prefills the answer from
its source, and uses it for the run; a local file wins over a committed
one. Changing the answer replaces the source and keeps `release_filter`,
`match`, and `prerelease_channel`. Otherwise, for a forge repository, it
fetches a valid repository-root `zapstore.yaml` from the default branch and
uses it for the run. In that fetched file, relative media and release-note
paths resolve from the repository root on the default branch, and a local
`release_source` makes the file invalid. A missing or invalid file changes
nothing. The wizard says which file it uses, or that the repository has
none:

```text
Using the local zapstore.yaml (<path>)
Using the committed zapstore.yaml in <repository> (default branch)
No zapstore.yaml committed in <repository> yet
```

`zsp` resolves the source, downloads and verifies the APKs, and asks the user
to choose when several remain. If nothing verifies it stops with
`No verified APK release was found at <source>`. If the user cancels it says
`No changes were made.` On success:

```text
Found <name> (v<version>) at <source>
```

It then asks `RELAYS` for app events for the verified app id and says what
it found: `<name> is already on <relay>.` or `<name> isn't on <relay> yet.`

When `metadata_sources` is already set, including `[]`, the wizard keeps it
and does not ask. Otherwise, after the APK is verified, it looks for sources
that can fill the listing, in this order: Fastlane (a local
`fastlane/metadata/android` tree, or the same path in the repository), the
repository's GitHub, GitLab, or Gitea metadata, an F-Droid package page for
the APK's package id, and a Play Store listing for that id. A missing source
is left out. A lookup that fails for a reason other than absence still
offers Fastlane when a forge or a local tree can host it, and F-Droid when
the APK came from F-Droid.

When at least one source exists:

```text
Where should the listing text and screenshots come from?
Found Fastlane, GitHub, and a Play Store listing.
```

Every found source starts selected. The developer can clear any of them.
Esc returns to "Where is your APK?". The kept names are written in the order
above. Clearing every source writes `metadata_sources: []`. An explicit list
is fetched in full for the preview and the first release; it does not stop
after Fastlane.

When none exist, it does not ask. The name and icon come from the APK.

It writes `./zapstore.yaml` when the run has no file yet. A local file that
omitted `metadata_sources` keeps its other fields and gains that list. A
committed file with no local copy is written locally from its original text
plus that list, so relative paths stay as committed. This run publishes from
the resolved configuration with those sources. Changing the APK location in
a local file updates `repository` and `release_source` and keeps the other
fields.

### Prove it's yours

The wizard first looks up active proofs for the APK certificate hash on
`RELAYS`, without a keystore or a signer. A proof that expires in more than
30 days is shown with its owner npub and expiry, and the step ends with
`<name> is claimed by <npub>`. There is no "are you the developer" question.

Otherwise the wizard needs the certificate key:

- With `HOTBOX_SOCKET` and `HOTBOX_PIN` set, hotbox signs the proof and the
  wizard asks for no keystore. The certificate hash hotbox returns must
  equal the APK's, and its npub must equal the signer's.
- Without hotbox, it asks for the keystore:

  ```text
  Signing keystore
  Zapstore checks that the key that signed <name> belongs to you.
  Not the developer? Press Ctrl-C to quit. Nothing was changed.
  ```

  It accepts JKS, PKCS#12, and PEM, validates the key and certificate pair,
  and matches its certificate hash to the APK. A mismatch stops with
  `This keystore does not match the APK. No proof was changed.` The key
  stays in memory for the run, so a later step never asks for it again.

It then needs a signer: a working `SIGN_WITH`, else a choice between a
remote signer (`bunker://`, recommended) and an `nsec`. The wizard never
takes a pasted `nsec`. That choice explains how to add
`SIGN_WITH=<nsec>` to `.env` in the working directory, make it
owner-readable, keep it out of Git, and run `zsp` again, and says a bunker
is safer. Esc on the bunker prompt returns to the choice. The wizard may
save a bunker URL to `.env` only after an explicit prompt; the file is
owner-readable and added to `.gitignore`.

A new or renewed proof keeps any existing delegate key and lasts two years.
It has no `cert` tag. The relay checks a proof against the listed app's
APK, so the wizard publishes a proof alone only when the app is already
listed; at least one relay in `RELAYS` must accept it. For an app not yet
listed, the wizard signs nothing here: the proof goes with the first
release.

For a listed app the step ends with:

```text
You're the verified developer of <name>
That's all Zapstore needs. New releases from <source> keep appearing,
now linked to your Nostr profile.
```

For an unlisted app it ends with
`Your proof goes out with the first release`. Cancelling says
`No proof was changed.`

### Publish

A listed app publishes nothing here; its proof is enough.

An unlisted app needs its first release to carry the proof:

```text
Publish <name> v<version> now?
Zapstore lists <name> once its first release is published. Your proof goes with it.
```

The choices are **Publish now** and **Quit**. Publish now prints the preview
address, opens that page, and waits for approval before signing and upload,
then follows
[Publication](#publication). It uses the run's `zapstore.yaml` when there
is one, else the defaults: name and icon from the APK and the default
metadata sources. The pending proof is signed with the keystore or hotbox
from Prove it's yours and published after the listing events. The indexer
then tracks the release's `repository` like any other source. Quit says
`Nothing was published.`

Every run that ends with the developer verified or a release published
then asks one last question:

```text
How should new releases of <name> reach Zapstore?
> Let Zapstore publish them for you (recommended)
  Set up CI/CD publishing (advanced)
```

The first choice appears only when `RELAYS` includes `relay.zapstore.dev`,
because only Zapstore's indexer publishes for developers. It shows:

```text
✓ Zapstore will keep <name> up to date
  Your proof lets Zapstore publish each new release, shown as developed by you.
  Wrote zapstore.yaml. Commit it and push it. An agent can improve it.
```

Otherwise the first choice is `Publish them yourself with zsp publish`,
which shows:

```text
✓ You'll publish new releases of <name>
  Your proof lets you publish each release with zsp publish, shown as developed by you.
  Wrote zapstore.yaml. Commit it, then run zsp publish after each release. An agent can improve it.
```

When the run used a `zapstore.yaml` it did not change, the note asks an
agent to improve that file instead of saying one was written.

CI/CD asks for the npub or hex public key of a separate Nostr key the CI
holds; Esc returns to the question. The prompt says the proof names the key
as a delegate that can publish releases but can't change ownership, that CI
should prefer that delegate as `SIGN_WITH` (its `nsec`, or a bunker for that
key), that the main key also works the same two ways, and that rotating the
delegate means running `zsp` again with a new key, whose proof replaces the
old one. The wizard never asks for the `nsec` or the bunker URL. It publishes
a new proof, signed by the owner, that names the delegate. It reuses the
keystore or hotbox from Prove it's yours and asks for the keystore only when
that step did not. The signer must be the owner. It ends with
`CI/CD publishing enabled`. That panel repeats the same `SIGN_WITH` choice,
says a bunker keeps working on the machine that still has zsp's `bunker-keys`
directory while an `nsec` needs only `SIGN_WITH`, and says to run
`zsp publish` after each release.

The step ends with `Release published`, the hand-off,
`CI/CD publishing enabled`, or a short error that says whether anything was
published.

## `zapstore.yaml`

Deprecated fields are accepted, ignored, and not kept: `changelog`,
`identifier`, `version`, `assets`, `executables`, `release_repository`,
`homepage`, `remote_metadata`, `fetch_metadata`, `blossom_server`,
`communities`, and `pubkey`. Any other unknown field is an error, so a typo
such as `release_sorce` fails. An empty file is an error. Transport settings
do not belong here.

| Field | Meaning |
| --- | --- |
| `repository` | HTTPS forge URL or NIP-34 `naddr`; source and metadata origin |
| `release_source` | URL, local APK path or glob, or structured source |
| `release_filter` | Regex on release names or tags |
| `match` | Regex on APK filenames |
| `name` | App name |
| `summary` | Plain-text short description |
| `description` | Markdown description |
| `tags` | List of category strings |
| `license` | SPDX identifier |
| `website` | URL |
| `icon` | Local path or HTTPS URL |
| `images` | List of local paths or HTTPS URLs |
| `release_notes` | Local path or HTTPS URL |
| `supported_nips` | List of NIP identifiers |
| `min_allowed_version` | Minimum accepted version string |
| `min_allowed_version_code` | Minimum accepted version code |
| `metadata_sources` | Ordered source names |
| `channel` | NIP-82 release channel; empty means `main` |
| `prerelease_channel` | Include prereleases and publish them on this channel |

`release_source` and `match` are the source configuration; `repository` is
not part of its identity. Without `release_source`, a supported forge
`repository` supplies releases. F-Droid and IzzyOnDroid are `release_source`
type `fdroid`, never inferred from `repository`. An omitted
`prerelease_channel` excludes prereleases; a set value includes them and
names their channel, which may be `main`.

A structured `release_source`:

```yaml
release_source:
  url: https://git.example.com/team/app
  type: gitlab
  version:
    url: https://example.com/releases/latest
    selector: .version
    attribute: data-version
    match: 'v([0-9.]+)'
  asset_url: https://example.com/app-{version}.apk
```

`type` is `github`, `gitlab`, `gitea`, `fdroid`, or `local`; omitted, it is
inferred from the URL or the source is a structured web source. A local
source is `type: local` with a path or glob in `url`. A URL of no recognized
forge, as a string or as `url:`, is a direct download and needs no type; the
parsed config keeps the URL as written. `version` and `asset` extractors need a URL and exactly one
of CSS `selector`, JSON `path`, or HTTP `header`; `attribute` applies to
selectors and `match` extracts a capture. `asset_url` and `asset` conflict.
Play Store supplies metadata, not releases.

Local paths and globs resolve from the file's directory. Local `icon`,
`images`, `release_notes`, and Fastlane files must be regular files inside
that directory (the working directory for a library caller); symlinks,
symlinked directories, and paths outside it are rejected. This does not
apply to a local `release_source`. Remote URLs require HTTPS except
loopback, and carry no userinfo or fragment. Every connection is checked
after DNS resolution: private, link-local, CGNAT, unspecified, and multicast
addresses are refused, and loopback is reached only through a URL that
names a loopback host. A redirect from another host to loopback is refused.
Forge URLs may end in `.git`. Every regex must compile; an invalid
`release_filter` fails when the source is built. `LoadConfig` never rewrites
local paths into forge URLs.

### Sources

- GitHub: the latest stable release as a fast path, else the newest
  releases, ten by default. With `prerelease_channel`, scan the list and
  include prereleases. Skip drafts, releases rejected by `release_filter`,
  and releases without APKs.
- GitLab: the newest releases, ten by default, after `release_filter`;
  release assets and APK upload links in the description, including upload
  paths and the external-download interstitial.
- Gitea, Forgejo, Codeberg: the newest releases, ten by default, skipping
  drafts, excluded prereleases, filtered releases, and releases without APKs.
- F-Droid and IzzyOnDroid: one complete APK per ABI, each with its own
  `versionCode`. The version choice below applies here too.
- Direct APK URL: the URL after redirects; the final target may name the
  file.
- Structured web: the configured selector, JSONPath, header, capture regex,
  version template, and asset extractor.
- Local: only `.apk` files.

Adapters return candidates; they neither filter architectures nor download.
One downloader handles remote APKs: a 600 MiB limit per file, redirect
validation, stall detection, bounded retries, and temporary-file cleanup. A
`403` on a source or media request is retried once through a local Tor
SOCKS5 proxy at `127.0.0.1:9050`, with the same headers minus credentials.
When F-Droid or GitHub publishes an APK's SHA-256, the download must match
it. `Fetch` stores HTTP validators between runs. GitHub stores an ETag and a
fingerprint of the release fields `zsp` reads. F-Droid stores the index
validators and a fingerprint of the package's entries. A direct URL stores
ETag or Last-Modified; only `200` and `304` answers count, and a server
that sends neither is always treated as changed. An unchanged source
returns `ErrNoNewAPK`. `Publish`
deletes those validators only when it fails with a retryable error.
`SkipHTTPCache` fetches unconditionally and stores nothing. The downloaded
APK is a temporary file, removed by a successful `Publish` or by `Close`.

### Metadata

Explicit YAML wins. For the name, the APK label comes next, then the first
source with a name, then the package ID. For every other field, the first
source with a non-empty value wins, in `metadata_sources` order. A source
`website` must pass the URL policy. Fastlane is read from
`fastlane/metadata/android` in the `zapstore.yaml` directory. A library
caller that leaves that directory empty reads it from the working directory.
Without the field, GitHub and GitLab try Fastlane, then native metadata when
Fastlane is missing, has no description, or fails; a local Fastlane tree is
used with no repository. Gitea tries only Fastlane; F-Droid and Play Store
are opt-in. A license from any source, or from `license`, is kept when it
is an SPDX identifier or license name (https://spdx.org/licenses/, list
3.27.0), and stored as the canonical identifier. Several recognized
licenses stay, separated by commas. Anything else from a source is dropped;
an explicit value that is not recognized is kept as written.
Valid values are `fastlane`, `github`, `gitlab`, `gitea`, `fdroid`, and
`playstore`; anything else fails validation. Remote sources supply only
URLs; only the local Fastlane tree may supply local files.

The icon comes from the APK unless `icon` is set. If the APK has no
usable icon, metadata icon URLs are tried in source order. A remote icon
or screenshot that is missing, is not a decodable image, or does not
finish within 15 seconds is skipped, and the next source is tried. That
does not fail publication; a discovered local Fastlane file that is a
symlink is skipped the same way. An explicit local icon, screenshot, or
release notes path that cannot be read, or that is a symlink or outside the
directory, fails with `ErrInvalidConfig`. Icons are at most 512 pixels wide,
screenshots 1440. PNG, JPEG, and GIF are encoded as WebP quality 80. A GIF
uses its first frame. A WebP already within that width stays unchanged; a
wider one is resized and encoded again. SVG stays unchanged only when it has
an `<svg>` root and no script, event handler, `foreignObject`, or
`javascript:` URL; otherwise it is rejected. The format is always detected
from the bytes and sets the media type. `--no-compress` keeps original
bytes.

## APK candidates

In order:

1. Resolve releases from the source and apply `release_filter`.
2. Keep APK assets and apply `match`.
3. Drop filenames that clearly name an unsupported ABI (`x86`, `x86_64`,
   `armeabi`, `armeabi-v7a`), an unsigned APK, or a split or config APK.
   Matching is case-insensitive and token-aware. A split set is never an
   asset; see [Catalog](../product/specs/catalog.md#one-complete-apk).
4. More than ten left: fail and ask for a narrower `match`.
5. Download the rest.
6. Verify signatures v1, v2, and v3. Reject malformed signatures, multiple
   current signers, Wear OS APKs, and APKs with native code but no
   `arm64-v8a`. Reject a missing, non-numeric, or non-positive
   `versionCode` and a negative SDK level. A label that is still an
   unresolved `@` resource reference counts as no label.
7. Drop rejected candidates and record safe warnings.
8. Deduplicate by lowercase SHA-256, keeping one managed file.

The current certificate is separate from its lineage. Validated v3
ancestors are listed oldest to newest only when a rotation exists and never
include the current certificate.

Publication selects one result. For every source, keep the greatest
`version_code` whose native code includes `arm64-v8a`, or, when none does,
an APK with no native code. At that `version_code`, a fat APK is dropped
when an arm64-only APK is present. Several arm64-only files, or more than
one app, fail with `apk_selection_required`; the wizard asks the user
instead.

A fetched `*APK` stays open until the caller closes it. `Close`
removes only `zsp`-owned temporary files, never a user's local APK, and is
idempotent. `Publish` closes the `*APK` on success. The temporary file does
not survive the process. HTTP validators do.

## Publication

`zsp publish`, and `Publish` in the library:

1. Load and validate configuration.
2. Fetch, verify, and select one APK.
3. Resolve `SIGN_WITH` into one signer.
4. Unless `SkipProofCheck`: the proof check and the release guard below.
5. Gather metadata and media.
6. Build the unsigned events. Without `SkipAppEvent`, fetch the signer's
   current kind `32267`; if the new one matches after dropping
   `created_at`, `id`, and `sig` and sorting tags, drop it and warn
   `application event unchanged`, unless `OverwriteAppEvent`. A failed
   lookup warns and publishes.
7. With `Preview`, report the preview address, show the events and files in a
   browser, and wait. The address is the progress target for phase `preview`.
   Rejection stops before signing.
8. Sign, including a new or renewed proof when one is needed.
9. Upload media to Blossom, and the APK only when it has no public source
   URL, a local file. Every required blob must succeed before any event is published.
   With `APKCacheDir`, copy the APK to `<APKCacheDir>/<sha256>.apk`; a
   failed copy warns.
10. Publish the app event when present, then the assets, then the release.
    Each event needs one accepting relay; other rejections are warnings.
    After a relay accepts the release, `zsp` reads it back by id and
    author. A relay that does not serve it marks it `pending`.
11. Publish the new or renewed proof last. The relay keeps the listing in
    its buffer zone until it has read the APK, from its Blossom, the APK
    cache, or the asset `url`, and checks the proof against it.

### Proof check and release guard

Before signing, `zsp` queries `RELAYS` for current kind `30509` events whose
`d` is the APK's current signer and selects the latest valid one: greatest
`created_at`, then lowest event id. A proof for an ancestor does not count.
The version floor still includes a release whose asset signer is that
certificate or one of its ancestors. The proof must be active and the signer
its owner or delegate; otherwise `proof_required` or `proof_unauthorized`.
The relay's own policy still applies.

With `HOTBOX_SOCKET` and `HOTBOX_PIN` set, `zsp` creates a proof when none is
active, and renews the signer's own proof when it expires within 30 days. It
never replaces a proof owned by another key. Hotbox signs the C1 message for
its own npub with the certificate key, for `created_at` now and the default
two-year expiry. `zsp` requires the returned public key to equal the
`SIGN_WITH` key and the returned certificate hash to equal the APK's, keeps
the `delegation` tag of a renewed proof, signs the kind `30509` event with
`SIGN_WITH`, and validates it against the APK certificate. It has no `cert`
tag. `zsp` publishes it to `RELAYS` after the listing events. One relay
must accept it, else `publish_rejected`. A hotbox failure is
`signer_unavailable`. A declined preview publishes nothing.

It then queries `RELAYS` for kind `30063` releases by that owner or delegate
for the app id in the selected channel, resolves their kind `3063` assets,
and takes the greatest valid `version_code` across the relays that
answered. A greater APK `versionCode` proceeds. An equal one stops with
`release_already_published` unless `--overwrite-release`. A lower one is
`release_downgrade`, always. `versionName` and source versions do not
count. If some relays fail it warns; if all fail the error is retryable.

The channel is `PublishOptions.Channel`, else `PublishConfig.Channel`, else
`main`. The CLI always runs these checks. Library callers that leave
authorization to the catalog relay, such as the indexer, set
`SkipProofCheck`.

### Blossom

Each file: a five-minute BUD-11 kind `24242` authorization with `t=upload`,
the file's `x`, and a `server` tag for the lowercase `BLOSSOM_URL` host;
`HEAD /upload` with `X-SHA-256`, `X-Content-Type`, and `X-Content-Length`;
one `PUT /upload` with `Content-Type`, `Content-Length`, and
`Content-Digest` set to that same 64-character hex hash; verify the
descriptor. Uploads are not retried. A descriptor must return the URL
`zsp` computed, or publication stops.

### Events

App id, version code, APK hash, size, MIME type, certificate, lineage, SDK
levels, and native architectures come from the verified APK. Nothing
overrides them.

- Kind `32267` uses the app id as `d`. `created_at` is the publication
  time unless `AppCreatedAtRelease`, which uses the source release time
  when that time is known. Its icon and image tags are
  `<BLOSSOM_URL>/<sha256>.<ext>`, with `ext` one of `apk`, `png`, `jpg`,
  `webp`, `gif`, `svg`. URLs are computed before preview and signing.
- Kind `30063` carries release notes in `content`, `c` for the channel, and
  `a` = `32267:<release-signer-pubkey>:<app-id>`, always the release
  signer's own coordinate. `zsp` never points a release at another key's
  app event. If that event does not exist, the catalog relay rejects the
  release. `created_at` is the source release time when known, else now.
- Kind `3063` has empty `content`, `url`, `x`, and one
  `apk_certificate_hash` for the current certificate. `url` is the public
  source URL the APK was fetched from, such as a GitHub release asset's
  `browser_download_url`; a local APK's is `<BLOSSOM_URL>/<sha256>.apk`.
  Both are computed before preview and signing. Each validated ancestor is
  an `apk_certificate_ancestor` tag, oldest first.
- `f` tags name the APK's architectures (`android-<abi>`). ABIs come from
  `lib/<abi>/`. When a `.so` is present but that path names no ABI, the
  ABI is the ELF machine. When no ABI can be detected, the tag is
  `android-arm64-v8a`. `f` tags are never empty. Release `f` tags are
  the sorted union of its assets'.

NIP-82 channels are `main`, `beta`, `nightly`, `dev`, or custom. The
indexer, the bundle, and the phone keep only `main` until
[release channels](../product/issues/release-channels.md). Another channel
may be published to the relay and is not in the phone catalog. A
`variant` tag does not exclude an asset from the bundle or the phone.

### Partial failure

Blobs upload before events, so a rejected event may leave a blob
unreferenced. When any upload or relay request completed, the result lists
every outcome with status `partial` or `failed`, and the error is returned
too. A caller may retry with the same open `*APK`; `zsp` reuses the files
and makes fresh authorization events.

## Go library

The CLI and the indexer use one public package; the indexer does not run
the CLI. The package discovers identity from verified APKs and takes no
expected app id or certificate; catalog comparison and certificate
promotion are the indexer's. It does not print or prompt. It can serve the
browser preview.

### Shape

```go
type Config struct {
    FetchConfig
    PublishConfig
}

type FetchConfig struct {
    Repository            string
    ReleaseSource         *ReleaseSource
    ReleaseFilter         string
    Match                 string
    PrereleaseChannel     string
}

type PublishConfig struct {
    Name                  string
    Summary               string
    Description           string
    Tags                  []string
    License               string
    Website               string
    Icon                  string
    Images                []string
    ReleaseNotes          string
    SupportedNIPs         []string
    MinAllowedVersion     string
    MinAllowedVersionCode int64
    MetadataSources       []string
    Channel               string
}

type ReleaseSource struct {
    URL              string
    LocalPath        string
    Type             string
    AssetURL         string
    VersionExtractor *Extractor
    AssetExtractor   *Extractor
}

type Extractor struct {
    URL       string
    Selector  string
    Attribute string
    Path      string
    Header    string
    Match     string
}

type FetchOptions struct {
    OnProgress    func(Progress)
    SkipHTTPCache bool
    MaxReleases   int
}

type CheckOptions struct {
    Relays  []string
    Channel string
}

type CheckResult struct {
    AppID                string
    CertificateHash      string
    Listed               bool
    Proof                *ProofStatus // nil when no proof is active
    PublishedVersionCode int64
    Signer               string // SIGN_WITH public key, lowercase hex; empty when unset
    Authorized           bool
    Warnings             []string
}

type ProofStatus struct {
    Owner     string
    Delegate  string
    ExpiresAt time.Time
}

type APK struct {
    Hash            string
    Filename        string
    SourceURL       string
    Size            int64
    AppID           string
    VersionName     string
    VersionCode     int64
    MinSDK          int32
    TargetSDK       int32
    Name            string
    CertificateHash string
    LineageHashes   []string
    Architectures   []string
}

type Progress struct {
    Operation string
    Phase     string
    Target    string
    Completed int64
    Total     int64
}

type PublishOptions struct {
    BlossomURL           string
    Relays               []string
    Channel              string
    Commit               string
    SkipAppEvent         bool
    SkipMediaCompression bool
    SkipProofCheck       bool
    OverwriteRelease     bool
    OverwriteAppEvent    bool
    AppCreatedAtRelease  bool
    Preview              bool
    BrowserPort          int
    OnProgress           func(Progress)
    ProofSigner          ProofSigner
}

type ProofSigner interface {
    SignProof(ctx context.Context, createdAt, expiry int64) (ProofSignature, error)
}

type ProofSignature struct {
    PublicKey       string // Nostr key named in the C1 message, lowercase hex
    CertificateHash string // SHA-256 of the certificate DER, lowercase hex
    Signature       []byte // ASN.1 DER ECDSA or PKCS#1 v1.5, over SHA-256
}

type EventIDs struct {
    Application string
    Release     string
    Assets      []string
}

type PublishResult struct {
    ID              string
    Status          string
    AppID           string
    CertificateHash string
    LineageHashes   []string
    Events          EventIDs
    Uploads         []BlobResult
    Relays          []RelayResult
    Warnings        []string
}

type Error interface {
    error
    Retryable() bool
}

var (
    ErrInvalidConfig        = errors.New("config is invalid")
    ErrSourceFailed         = errors.New("source failed")
    ErrTooManyCandidates    = errors.New("too many APK candidates")
    ErrNoAPK                = errors.New("no APK found")
    ErrNoNewAPK             = errors.New("no new APKs")
    ErrInvalidAPK           = errors.New("APK is invalid")
    ErrAPKNotChecked        = errors.New("APK was not returned by Fetch")
    ErrAPKSelectionRequired = errors.New("APK selection required")
    ErrProofRequired        = errors.New("active proof required")
    ErrProofUnauthorized    = errors.New("signer is not authorized by the proof")
    ErrSigner               = errors.New("signer is unavailable")
    ErrAlreadyPublished     = errors.New("release already published")
    ErrReleaseDowngrade     = errors.New("release version code is a downgrade")
    ErrUploadRejected       = errors.New("blob upload rejected")
    ErrPublishRejected      = errors.New("relay publication rejected")
    ErrRateLimited          = errors.New("rate limited")
    ErrTemporaryFailure     = errors.New("temporary failure")
)

type BlobResult struct {
    URL      string
    Hash     string
    Size     int64
    Type     string
    Uploaded int64
    Accepted bool
    Message  string
}

type RelayResult struct {
    RelayURL  string
    EventID   string
    Accepted  bool
    Duplicate bool
    Message   string
}

func LoadConfig(path string) (Config, error)
func Fetch(ctx context.Context, config FetchConfig, options FetchOptions) ([]*APK, error)
func (apk *APK) Close() error
func Check(ctx context.Context, config PublishConfig, apk *APK, options CheckOptions) (*CheckResult, error)
func Publish(ctx context.Context, config PublishConfig, apk *APK, options PublishOptions) (*PublishResult, error)
```

### Rules

- `Config` holds every `zapstore.yaml` field. `release_source` as a string
  or an object both become `ReleaseSource`; YAML `version` and `asset` map
  to `VersionExtractor` and `AssetExtractor`. `LocalPath` is for APKs built
  on the developer's machine.
- `LoadConfig` validates without network and resolves local paths from the
  file's directory. A caller that builds `Config` directly uses absolute
  paths. The library never fetches repository configuration.
- `Fetch` takes `FetchConfig`, `Publish` takes `PublishConfig`. Each
  validates its argument and does not change it. Both need an options
  value: `FetchOptions{}` verifies with no progress; `PublishOptions{}`
  includes the app event, compresses media, sets no commit, and runs the
  proof check and release guard.
- `PrereleaseChannel` is read by `Fetch` only. A caller that includes
  prereleases sets `PublishOptions.Channel` to that value.
- `MetadataSources` is tri-state: nil means defaults, an empty slice means
  no remote metadata, a non-empty slice names sources.
- `BlossomURL` and `Relays` default to `BLOSSOM_URL` and `RELAYS`, then the
  defaults above. `SIGN_WITH` is read from the environment; it cannot be
  passed as an argument. `Commit` sets the asset `commit` tag.
- `ProofSigner` is nil by default: `Publish` never creates a proof. When set,
  `Publish` creates or renews the signer's own C1 proof as described in
  Proof check. The CLI sets it from `HOTBOX_SOCKET` and `HOTBOX_PIN`.
  `SkipMediaCompression` keeps media bytes so hashes match the source.
- `SkipHTTPCache` fetches unconditionally and stores no validators.
  Otherwise `Fetch` sends the stored validators and returns `ErrNoNewAPK`
  when the source reports no change. `Publish` deletes them only when its
  error is retryable.
- `MaxReleases` is how many recent GitHub, GitLab, or Gitea releases to scan
  for a usable APK. Zero uses 10. A value below 0 or above 100 is
  `ErrInvalidConfig`. Other sources ignore it. The CLI does not set it.
- `Fetch` applies the `version_code` choice above. It returns that one APK,
  or the remaining files when the choice is not unique, in stable order,
  sorted by normalized source URL and filename; duplicates share one `APK`
  and the first duplicate names it. `SourceURL` is the public download URL
  used in the asset event, empty for local files. `Name` is the APK's name
  and the fallback for `PublishConfig.Name`. `LineageHashes` is nil without
  a rotation. A probe before `Fetch` is only a hint; `Fetch` resolves again.
- `Publish` accepts only an open `*APK` from `Fetch`; anything else is
  `ErrAPKNotChecked`. On success `Status` is `published`, or `pending`
  when an accepting relay does not serve the release yet, and `ID` is the
  release event id. That relay result has `Pending`. `APKCacheDir` is for a
  caller on the relay's host, such as the indexer, which prunes it. The downloader retries transient transfer failures;
  signing, Blossom, and relay requests are not retried.
- `Check` accepts an open `*APK` from `Fetch` and leaves it open. It runs
  the proof lookup and release query of Proof check for the APK
  certificate and channel, without failing on a missing or unauthorizing
  proof, and asks for an app event with the APK's app id from any author.
  It resolves `SIGN_WITH` only for its public key. Relay failures are
  warnings unless every relay fails, which is a retryable error.
- `OnProgress` is called synchronously for source requests, downloads,
  verification, uploads, and publication, and must return quickly. A
  candidate-rejection diagnostic has `Phase` `"warning"`, the text in
  `Target`, and zero counts.
- Errors implement `Error` and wrap a sentinel. Use `errors.Is` to classify
  and `errors.As` for `Retryable`, which is true only for `ErrRateLimited`
  and `ErrTemporaryFailure`. Context cancellation is detectable with
  `errors.Is`.

## Agent publishing skill

`zsp skill` prints the skill in `SKILL.md`, embedded in the binary, so an
agent reads the version that matches the installed `zsp`. It takes no
arguments, writes Markdown to stdout, and reads no `.env`.

The wizard writes a basic `zapstore.yaml` for the first listing. The skill
writes and improves that file, finds the APK, runs
`zsp check` and follows its `next_step`, runs `zsp publish` with complete
arguments when that is the step, and reports the release event id. It keeps
`metadata_sources` when the file already sets it. It never reads or prints secrets,
never asks for keystore passwords, private keys, `nsec` values, or `.env`
contents, and never speaks relay or Blossom protocols. It does not build a
C1 event itself. With hotbox configured, that one publish run creates or
renews the proof; the certificate key stays in hotbox. On `proof_required`
without hotbox, it guides the developer through `zsp`, where they claim the
app. A human provides the keystore and signer to `zsp`. It runs `hotbox sign`
only when `hotbox` is on `PATH` or the user asks for it. When `repository`
is the release source and the developer is uploading a local APK, the skill
tells them to attach that APK to the forge release and keeps the local path
out of the committed file. A one-shot publish may pass `-s`. It does not
publish an older forge asset in its place. An existing non-forge
`release_source` is left unchanged.

## Verification

Tests cover:

- proofs with and without a delegate key;
- proof lookup by certificate hash and CLI authorization preflight;
- proof creation through `ProofSigner`: a new proof is active, a renewal
  keeps the delegate, a different Nostr key or certificate is refused, and
  renewal applies only to the signer's own proof within 30 days of expiry;
- a new or renewed proof has no `cert` tag and is published after the
  listing events;
- the hotbox socket client frame and its PIN-free errors;
- refusal of a group- or other-readable `.env` holding secrets;
- developer, delegate, and indexer publication;
- v1, v2, and v3 signatures, rotations, and multiple-current-signer
  rejection;
- no candidate, rejected candidate, duplicate hash, the ten-candidate limit,
  one result, and several results;
- greater, equal, and lower `version_code`, with `--overwrite-release`
  allowing equality and never a downgrade, per channel;
- `prerelease_channel` omitted and set, including `main`;
- wizard and non-interactive behavior;
- nsec and bunker signing;
- skipped app events reusing only the signer's own `32267`, unchanged
  `32267` skipped unless `--overwrite-app-event`, and relay rejection when
  it is missing;
- preview matching the signed events and uploaded files;
- wizard-fetched repository configuration;
- explicit release sources and `match` filtering;
- the library without CLI or UI state;
- Blossom-before-relay ordering and partial failures;
- `*APK` ownership and cleanup after success and retry;
- JSON success, warnings, remediation, and stable error codes.

### Test layout

Process tests in `tests/` drive the compiled `zsp` binary and import no
`zsp` packages. They cover the JSON envelopes and start no relay. Wizard
tests sit next to `main.go` and fake HTTP in-process with `httptest`, with
no PTY. Library publish tests
(`tests/integration/publish`) start `../relay` with `VERTEX_SECRET_KEY`
and Bunny unset and assert through REQ and Blossom GET. The release test
(`tests/integration/release`) uses the hotbox binary in `hotbox/dist`, opens a
passphrase-only vault with an empty password, signs an APK, and drives the
wizard on a terminal against that temporary relay. The provider walk
(`tests/integration/providers`) fetches real forges and queries
`wss://relay.zapstore.dev`.

```sh
go test ./tests -count=1
go test ./...
go test -tags providers -count=1 -timeout=20m ./tests/integration/providers
go test -tags publish -count=1 -timeout=15m ./tests/integration/publish
go test -tags release -count=1 -timeout=20m ./tests/integration/release
go vet ./...
```
