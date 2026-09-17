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

ZSP discovers the app, verifies its signing certificate, and guides proof
creation and publication. The proof is published to `RELAYS`. Add a
`zapstore.yaml` to the repository for finer metadata control.

## Publish

Publish a local APK:

```sh
zsp publish app-release.apk
```

Publish from `zapstore.yaml`:

```sh
zsp publish
```

Running `zsp` without arguments starts the interactive identity and publish
wizard. Commands with arguments produce one JSON document for automation.

Check source resolution and every matching APK without publishing:

```sh
zsp publish --check zapstore.yaml
```

When several APKs verify, interactive use presents a selection. Automation
must select an exact hash:

```sh
zsp publish --quiet --skip-preview \
  --apk-hash <sha256> zapstore.yaml
```

Data goes to stdout, errors to stderr, and callers branch on `error.code`.

## APK metadata

```sh
zsp utils extract-apk app-release.apk
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
in a loaded file resolve from that file's directory.

Explicit YAML values win over gathered metadata. Without `metadata_sources`,
GitHub and GitLab try Fastlane then native repository metadata; Gitea tries
Fastlane. F-Droid and Play Store metadata are opt-in. Use `--skip-metadata` to
disable external metadata.

## Signing and environment

`SIGN_WITH` accepts an nsec, lowercase hexadecimal private key, or `bunker://`
URL. An npub cannot publish. Prefer a short-lived NIP-46 bunker over exposing
a private key.

| Variable | Purpose |
| --- | --- |
| `SIGN_WITH` | Nostr signer |
| `RELAYS` | Comma-separated relays; defaults to `wss://relay.zapstore.dev`. Source suggestions use the first host. |
| `BLOSSOM_URL` | Blossom server; defaults to `https://cdn.zapstore.dev` |
| `KEYSTORE_PASSWORD` | JKS/PKCS#12 store password |
| `KEYSTORE_KEY_PASSWORD` | Optional JKS private-key password |
| `GITHUB_TOKEN` | Optional GitHub API token |

Values resolve from the process environment, then `.env` in the exact current
working directory.

## Command surface

```text
zsp publish [options] [zapstore.yaml | app.apk]
zsp utils extract-apk <app.apk>
```

Run `zsp <command> --help` for flags. Offline output, indexer mode, local
caches, and config migration are not supported.

## Go API

The non-interactive public package is `github.com/zapstore/zsp`:

```go
cfg, err := zsp.LoadConfig("zapstore.yaml")
apks, err := zsp.Fetch(ctx, cfg.FetchConfig, zsp.FetchOptions{})
result, err := zsp.Publish(ctx, cfg.PublishConfig, apks[0], zsp.PublishOptions{})
```

Call `APK.Close` for candidates that will not be published. `Publish` closes a
successfully published candidate. Use `errors.Is` with exported sentinels and
`errors.As` to inspect `zsp.Error.Retryable()`. `Fetch` returns `ErrNoNewAPK`
when a stored ETag shows the source is unchanged; set `FetchConfig.SkipETag` to
always download.

## License

MIT
