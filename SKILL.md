---
name: zapstore-publish
description: >-
  Builds a release APK, signs it with hotbox, and publishes it with zsp.
  Use only when the user asks to build, sign, or publish an APK, or to run
  zsp publish. Do not use it to bump a version or edit a changelog.
---

# Publish

Run each command, then the next, in the same turn. Send no text until the sequence ends. Do not say a command finished, name the next one, or preview it. "The release build finished. Next I'll pick the arm64 APK and sign it." is a broken turn: the find command, then `hotbox sign`, should already be running. If a command fails, stop and report that error. Otherwise the only message is the final reply below.

Do not discover how to do this.

Do not search the repo, old chats, help text, or this skill's directory. Do not run `--help`. Do not check that `gradlew` or `hotbox` exists. Do not read `.env` or list its keys. Do not look up signing, keystores, or `apksigner`. Do not fetch tool schemas. Do not hardcode a path to `hotbox`.

`zsp check` takes a signed APK. `zsp publish` reads `zapstore.yaml`. Do not pass an APK to `zsp publish`. Do not publish a debug APK.

From the project directory, run this and continue:

```bash
./gradlew assembleRelease
```

Then this. It prints the unsigned `arm64-v8a` file, or else the unsigned universal file:

```bash
zsp utils find-unsigned . | grep -m1 arm64-v8a || zsp utils find-unsigned . | grep -m1 -E 'universal|app-release\.apk'
```

When that prints a path, the next command signs that path. Do not rename it. When it prints nothing, the build already signed the APK. Publish no other ABI.

```bash
hotbox sign <apk>
```

Read `release_source` in `zapstore.yaml` before publishing. Publish from that file. Do not pass `-s`.

When `release_source` is a URL or a mapping, leave it:

```bash
zsp publish -q
```

When it is a local path or glob, point it at the signed APK when it does not already name that file, then run the same command.

When it is missing and `repository` is set, tell them to attach this APK to the forge release, keep the local path out of the committed file, and stop. When the file has no public release source, set `release_source` to the signed APK path, then publish. Only then does zsp upload the APK, to `BLOSSOM_URL`.

When the result has `"status": "pending"`, the relay holds the release until it has read the APK. Wait 1 minute and run `zsp publish -q --json` again, then after 2, 3, and 4 more minutes, about 10 minutes in all. Stop at the first run that answers:

- `"status": "published"`, or `error.code` `release_already_published`: it is listed. Use the published reply.
- `error.code` `publish_rejected`: the relay refused it. Use the refused reply with `error.detail`.
- `"status": "pending"` after the last run: use the pending reply.

Each run signs the events again; that is expected. Do not wait for anything else between runs.

Leave the rest of `zapstore.yaml` when it already has `repository`, `release_source`, and `release_notes`. Do not read the yaml reference below in that case.

`SIGN_WITH` must already be set. An npub cannot publish. When `HOTBOX_SOCKET` and `HOTBOX_PIN` are set, this run creates or renews the C1 proof. A `version_code` not greater than the published one will not publish unless they asked for `--overwrite-release`. If several APKs verify, add `--apk-hash <hash>` from `error.context.candidates`. When `error.next_step.requires_human` is true, tell them to run `zsp` themselves and stop. When there is nothing newer to publish, say so and stop.

The wizard may already have written `repository` or `release_source` and `metadata_sources`. Keep `metadata_sources` when the file sets it, including `[]`. Otherwise you write `zapstore.yaml`, and only when the file is missing or they asked to change it. Follow the format below. Do not invent fields.

When they asked only to check a signed APK, run `zsp check` on it once.

Stdout is one JSON document when it isn't a terminal; pass `--json` to be sure. Branch on `error.code`.

When signing is the last command, this is the whole reply. Do not publish.

```
Signed. The APK is `<path>`.

Certificate SHA-256: `<digest>`
```

When publish is the last command, this is the whole reply:

```
Published <version_name> (version code <version_code>).

Release event id: `<id>`

Push the version bump and changelog so the repository matches this release.
```

When the relay refused it, this is the whole reply:

```
Zapstore refused <version_name> (version code <version_code>): <error.detail>
```

When it is still pending, this is the whole reply:

```
<version_name> (version code <version_code>) is waiting on the Zapstore relay, which reads the APK before listing it. Run `zsp publish` again later to see whether it was listed.

Release event id: `<id>`
```

When a build or a check is the last command and there is nothing else to report, the whole reply is the path, `version_name`, and `version_code`.

## zapstore.yaml

Read this section only when `zapstore.yaml` is missing or the user asked to change it.

Canonical file in the project directory. Relative paths resolve from the file's directory. Explicit values win over gathered metadata. Do not write `changelog`, `pubkey`, or `communities`; they have no effect.

`repository` or `release_source` is required. A local build sets `release_source` to the APK path `zsp check` verified. `repository` is the source forge URL, or a NIP-34 naddr.

```yaml
repository: https://github.com/example/app
release_source: ./app-release.apk
```

```yaml
repository: https://github.com/example/app
release_source: https://f-droid.org/packages/com.example.app
release_filter: '^v[0-9]+\.[0-9]+\.[0-9]+$'
match: 'arm64.*\.apk$'
prerelease_channel: beta
channel: main

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

`release_filter` filters source release tags. `match` filters APK asset names. `prerelease_channel` selects prerelease source releases. `channel` is the NIP-82 channel; omit it for `main`.

`release_notes` is a local path or URL. A Keep a Changelog file contributes only this release's section.

`metadata_sources`: omit for the default, `[]` to fetch none, or a list of `fastlane`, `github`, `gitlab`, `gitea`, `fdroid`, `playstore`. GitHub and GitLab try Fastlane, then native repository metadata. Gitea tries Fastlane only. F-Droid and Play Store are opt-in.

A `release_source` string is a forge URL, an F-Droid package URL, a direct APK URL, or a local APK path or glob.

```yaml
release_source: https://github.com/example/app
release_source: ./dist/*.apk
```

A mapping is for self-hosted forges or page extraction. `type` is `github`, `gitlab`, `gitea`, `fdroid`, or `local`. `asset_url` may contain `{version}`. `asset_url` and `asset` are mutually exclusive. The extractor sets one of `selector`, `path`, or `header`.

```yaml
release_source:
  url: https://git.example.com/example/app
  type: gitea
  asset_url: https://git.example.com/example/app/releases/download/{version}/app.apk
  version:
    url: https://git.example.com/api/v1/repos/example/app/releases/latest
    path: $.tag_name
    match: 'v([0-9.]+)'
```

HTML uses `selector` and optional `attribute` (omit `attribute` for text). Header mode uses `header`, usually `location`. `asset` uses the same extractor fields to pull the download URL.
