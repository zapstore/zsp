---
name: zapstore-publish
description: >-
  Builds, checks, signs, and publishes an Android release to Zapstore. Use
  when the user asks to build, sign, or publish an APK, or mentions
  zapstore.yaml or zsp publish. Uses hotbox only when it is on PATH or the
  user asks for it.
---

# Publish

Run only the steps they asked for, in this order. Do not bump a version or add a step they did not ask for.

Never read or print secrets. Never ask for a keystore password, private key, `nsec`, or `.env` contents. Never speak relay or Blossom protocols. A `.env` that holds `SIGN_WITH` or `HOTBOX_PIN` must be mode 0600. Do not print `.env`.

Do not inspect git, and do not ask about commits. If `zapstore.yaml` was created or changed, remind them to push it. If the project uses Fastlane, remind them to push that too.

From the project directory:

1. Build. Read the project's build instructions. A Makefile is the usual place. Run the release target it defines and take the APK path from those instructions. Do not assume Gradle or a fixed output directory. If they named an ABI, the unsigned file is `app-<abi>-release-unsigned.apk` in that release output.

2. Check. `zsp utils extract-apk <apk>`. Require `package_id`, `version_name`, and `version_code` > 0. Stop if they do not match the release they asked to ship. There is no separate relay query. `zsp publish` compares `version_code` with the highest release on `RELAYS` for that package, certificate, and channel. A lower code fails. An equal code fails unless they asked for `--overwrite-release`.

3. Sign. Skip when `zsp utils extract-apk` succeeds. It returns `certificate_hash` only after the signature verifies. If verification fails, the APK is unsigned. When it is unsigned and `hotbox` is on `PATH`, or they asked for hotbox, run `hotbox sign <unsigned.apk>`. It prints the signed path and the certificate SHA-256. A file named `app-release-unsigned.apk` is written as `app-release.apk` beside it. Any other name is written as `<name>-signed.apk`. If they asked for hotbox and it is not on `PATH`, stop. Otherwise follow the project's own signing instructions. Do not start hotbox or paste its `.env` block.

4. `zapstore.yaml`. Make sure the project has a great one. Follow the format below. Use its real name, summary, description, and the license, website, tags, icon, images, and release notes it has. Do not invent them. A local APK goes in `release_source` only when the file has no public release source. When `repository` is the release source (`release_source` is absent or is that same repository) and they are uploading a local APK, tell them to attach this APK to the forge release and keep the local path out of the committed file. Publish from the repository once that asset is there. Until then, this run may pass `-s <apk>`. Do not publish an older forge asset instead. When `release_source` is already another URL, leave it.

5. Publish. `zsp publish --quiet --skip-preview` loads `zapstore.yaml`, or the one config path you pass. If several APKs verify, add `--apk-hash <sha256>` from the extract output. `SIGN_WITH` must already be set. An npub cannot publish. When `HOTBOX_SOCKET` and `HOTBOX_PIN` are set, this run creates or renews the C1 proof. On `proof_required` without hotbox, guide them through `zsp`. They confirm they are the author, choose the keystore that signed the APK, and give the signer to `zsp`. The password and the key stay in that prompt. Then publish again.

Stdout is one JSON document. Branch on `error.code`. After signing, reply with the signed path and the certificate SHA-256, then stop:

```
Signed. The APK is `<path>`.

Certificate SHA-256: `<digest>`
```

After a publish, reply with the release event `id`, add the push reminder, and stop. After a build or a check with nothing else to report, reply with the path, `version_name`, and `version_code`, then stop.

## zapstore.yaml

Canonical file in the project directory. Relative paths resolve from the file's directory. Explicit values win over gathered metadata. Do not write `changelog`, `pubkey`, or `communities`; they have no effect.

`repository` or `release_source` is required. A local build sets `release_source` to the signed APK. `repository` is the source forge URL, or a NIP-34 naddr.

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
