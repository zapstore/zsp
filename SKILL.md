---
name: zapstore-publish
description: >-
  Builds a release, signs it when the build left it unsigned, and publishes
  it with zsp. Use when the user asks to build, sign, or publish an APK, or
  mentions zapstore.yaml or zsp publish.
---

# Publish

Do these steps and nothing else.

`zsp check` and `zsp publish` take a signed APK. Do not publish a debug APK.

From the project directory:

1. Build. When `./gradlew` exists, run `./gradlew assembleRelease`. Otherwise run the project's release build.

2. Choose the APK. This prints the unsigned `arm64-v8a` file, or else the unsigned universal file:

```bash
zsp utils find-unsigned . | grep -m1 arm64-v8a || zsp utils find-unsigned . | grep -m1 -E 'universal|app-release\.apk'
```

When that prints a path, sign it the way this project signs. When it uses hotbox, `hotbox sign <apk>` and use the path it prints. Do not rename it. When it prints nothing, the build already signed the APK. Publish no other ABI.

3. Publish that signed APK. Leave `zapstore.yaml` when it already has `repository`, `release_source`, and `release_notes`.

```bash
zsp publish -q -s <signed-apk>
```

`SIGN_WITH` must already be set. An npub cannot publish. When `HOTBOX_SOCKET` and `HOTBOX_PIN` are set, this run creates or renews the C1 proof. A `version_code` not greater than the published one will not publish unless they asked for `--overwrite-release`. If several APKs verify, add `--apk-hash <hash>` from `error.context.candidates`. When `error.next_step.requires_human` is true, tell them to run `zsp` themselves and stop. When there is nothing newer to publish, say so and stop.

The wizard may already have written `repository` or `release_source` and `metadata_sources`. Keep `metadata_sources` when the file sets it, including `[]`. Otherwise you write `zapstore.yaml`, and only when the file is missing or they asked to change it. Follow the format below. Do not invent fields. A local APK goes in `release_source` only when the file has no public release source. When `repository` is the release source and they are uploading a local APK, tell them to attach this APK to the forge release and keep the local path out of the committed file. Until then, this run may pass `-s <apk>`. When `release_source` is already another URL, leave it.

When they asked only to check a signed APK, run `zsp check` on it once.

Stdout is one JSON document when it isn't a terminal; pass `--json` to be sure. Branch on `error.code`.

After signing is the last step, reply with the signed path and the certificate SHA-256, then stop:

```
Signed. The APK is `<path>`.

Certificate SHA-256: `<digest>`
```

After a publish, reply with the release event `id`, add the push reminder, and stop. After a build or a check with nothing else to report, reply with the path, `version_name`, and `version_code`, then stop.

## zapstore.yaml

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
