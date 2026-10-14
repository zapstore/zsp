# zsp

ZSP verifies Android releases and publishes NIP-82 application, asset, and
release events for Zapstore.

## Install

```sh
go install github.com/zapstore/zsp/cmd/zsp@latest
```

## Developer onboarding

Identity creation proves control of the Android signing certificate and a
Nostr key. Run the interactive wizard yourself—do not give an agent your
keystore, password, private key, `nsec`, or `.env`:

```sh
zsp
```

ZSP finds your APK, checks that your keystore signed it, and publishes the
proof that the app is yours: alone when the app is already listed,
otherwise with the first release.

The wizard does not write `zapstore.yaml`. Ask your coding agent to run
`zsp skill` and write one, then commit and push it so Zapstore keeps your
listing up to date.

## Check

`zsp check` finds and verifies your APK, then reports whether the app is
listed, which proof is active, whether `SIGN_WITH` can publish, and what to
do next. It signs and publishes nothing.

```sh
zsp check                    # from zapstore.yaml
zsp check app-release.apk    # one local APK
```

## Publish

`zsp publish` reads `zapstore.yaml`. A local build sets `release_source` to
the signed APK:

```yaml
release_source: ./app-release.apk
```

```sh
zsp publish
```

On a terminal, `check` and `publish` print readable text. Piped, or with
`--json`, they print one JSON document: data on stdout, errors on stderr.
Callers branch on `error.code`. `-q` turns off progress and the preview.

When several APKs verify, interactive use presents a selection. Automation
must select an exact hash:

```sh
zsp publish -q --apk-hash <hash>
```

## Configuration

Minimal `zapstore.yaml`:

```yaml
repository: https://github.com/example/app
```

With an independent release source and explicit metadata:

```yaml
repository: https://github.com/example/app
release_source: https://f-droid.org/packages/com.example.app
release_filter: '^v[0-9]+\.[0-9]+\.[0-9]+$'
match: 'arm64.*\.apk$'

name: Example
summary: A short plain-text summary
description: |
  Longer **Markdown** description.
license: MIT
website: https://example.com
tags: [productivity]

icon: ./assets/icon.png
images:
  - ./assets/screenshot.png
release_notes: ./CHANGELOG.md

supported_nips: ["01", "46"]
min_allowed_version: 1.0.0
min_allowed_version_code: 100
metadata_sources: [fastlane, github]
```

`release_source` also supports GitLab, Gitea/Forgejo, direct APK URLs,
structured HTML/JSON/header extractors, local APK paths and globs. Local paths
in a loaded file resolve from that file's directory. Local `icon`, `images`,
`release_notes`, and Fastlane files must sit inside that directory and must
not be symlinks. Unknown fields in `zapstore.yaml` are an error.

Explicit YAML values win over gathered metadata. The app name falls back to
the APK label, then metadata, then the package ID. Without `metadata_sources`,
GitHub and GitLab try Fastlane first, then native repository metadata when
Fastlane is absent, has no full description, or the lookup fails. Fastlane
values that are present still win. Gitea tries Fastlane only. F-Droid and Play
Store metadata are opt-in; F-Droid descriptions come from the package page when
the fdroiddata YAML does not include them. Use `--skip-metadata` to disable
external metadata.

## Signing and environment

`SIGN_WITH` accepts an `nsec1…` or a `bunker://` URL. Hex keys and npubs are
refused. Prefer a short-lived NIP-46 bunker over exposing a private key.

| Variable | Purpose |
| --- | --- |
| `SIGN_WITH` | Nostr signer |
| `RELAYS` | Comma-separated relays; defaults to `wss://relay.zapstore.dev`. |
| `BLOSSOM_URL` | Blossom server; defaults to `https://cdn.zapstore.dev` |
| `APK_CACHE` | When set, copy each accepted APK into this directory, named by its SHA-256 |
| `KEYSTORE_PASSWORD` | JKS/PKCS#12 store password |
| `KEYSTORE_KEY_PASSWORD` | Optional JKS private-key password |
| `GITHUB_TOKEN` | Optional GitHub API token, sent only to GitHub hosts |
| `GITEA_TOKEN`, `GITEA_HOST` | Optional Gitea/Forgejo token, sent only to `GITEA_HOST` (default `codeberg.org`) |
| `GITLAB_TOKEN`, `GITLAB_HOST` | Optional GitLab token, sent only to `GITLAB_HOST` (default `gitlab.com`) |
| `HOTBOX_SOCKET`, `HOTBOX_PIN` | Hotbox socket and PIN; `publish` creates or renews the C1 proof |

Values resolve from the process environment, then `.env` in the exact current
working directory. A `.env` that holds `SIGN_WITH` or `HOTBOX_PIN` must be
mode 0600 (`chmod 600 .env`); zsp refuses one that other users can read.

With [hotbox](../hotbox), paste the block it prints into `.env`. `zsp publish`
then publishes the release and, after it, the certificate proof (kind 30509)
when none is active, or a renewal of your own proof within 30 days of expiry.
The relay checks the proof against the uploaded APK. A proof owned by another
key is never replaced.

## Command surface

```text
zsp                                         # interactive wizard
zsp check [options] [zapstore.yaml | app.apk]
zsp publish [options] [zapstore.yaml]
zsp skill                                   # agent skill
```

Run `zsp <command> --help` for flags. Offline output, indexer mode, local
caches, and config migration are not supported.

## Go API

The non-interactive public package is `github.com/zapstore/zsp`:

```go
cfg, err := zsp.LoadConfig("zapstore.yaml")
apks, err := zsp.Fetch(ctx, cfg.Fetch, zsp.FetchOptions{})
status, err := zsp.Check(ctx, cfg.Publish, apks[0], zsp.CheckOptions{})
result, err := zsp.Publish(ctx, cfg.Publish, apks[0], zsp.PublishOptions{})
```

Call `APK.Close` for candidates that will not be published. `Publish` closes a
successfully published candidate. Use `errors.Is` with exported sentinels and
`errors.As` to inspect `zsp.Error.Retryable()`. `Fetch` returns `ErrNoNewAPK`
when a stored HTTP cache shows the source is unchanged; set
`FetchOptions.SkipHTTPCache` to always download. `FetchOptions.MaxReleases`
raises how many recent forge releases are scanned; zero keeps 10, and the
maximum is 100. `Publish` deletes that cache
only when the failure is `Retryable()`, so the next `Fetch` can retry the same
source. Permanent outcomes keep the cache.

## License

MIT
