# FEAT-002 — Utils Subcommand and `--skip-app-event` Flag

## Goal

Two additions to support the demand-driven indexing fallback strategy:

1. **`zsp utils` subcommand**: Repurpose the existing `zsp apk` subcommand into a broader `zsp utils` namespace for operational tooling. Move the existing `zsp apk --extract` to `zsp utils extract-apk`, and add `zsp utils check-releases` for version detection without publishing.
2. **`--skip-app-event` flag on `zsp publish`**: Publish only release events (kind 30063/3063), skip kind 32267 app metadata. Used by zindex after takeover, when the 32267 has already been copied and re-signed from the developer's event.

## Non-Goals

- Changing the existing `--check` flag behavior on publish (validates APK arm64-v8a compatibility)
- Downloading APKs or building events in `check-releases` — stops after version detection
- Any interactive prompts in `check-releases`
- Publishing or uploading anything in `check-releases`

## User-Visible Behavior

### `zsp utils extract-apk <file.apk>`

Replaces `zsp apk --extract <file.apk>`. Same behavior: parses APK, outputs metadata as JSON to stdout.

### `zsp utils check-releases <config.yaml>`

- Checks the upstream source for the latest release
- If a new version is available (not already on the relay): prints `NEW <version>` to stdout, exits 0
- If the latest version already exists on the relay: prints `UP_TO_DATE` to stdout, exits 0
- If the source check fails (repo not found, no releases, network error): prints error to stderr, exits 1
- No APK download, no metadata fetch, no signing, no relay publishing
- ETag cache is still used and updated (so subsequent checks are cheap)

### `zsp publish <config> --skip-app-event`

- Runs the full publish pipeline but skips building and publishing the kind 32267 app metadata event
- Only kind 30063 and 3063 events are built, signed, and published
- APK is downloaded, parsed, uploaded to Blossom as usual
- Used after the indexer has already published a re-signed copy of the developer's 32267

## Edge Cases

- **`check-releases`: Repository has no releases:** Exit 1 with descriptive error
- **`check-releases`: GitHub API rate limited:** Exit 1, ETag cache helps avoid this in practice
- **`check-releases`: ETag cache hit (304):** `UP_TO_DATE` — fastest path, single API call
- **`check-releases`: Config file missing or invalid:** Exit 1 with error
- **`check-releases`: Relay unreachable for existing-asset check:** Treat as "can't confirm existing" — report `NEW <version>` (conservative: assume it needs publishing)
- **`check-releases`: No `SIGN_WITH` set:** `check-releases` doesn't need a signer, but does need relay access for the existing-asset check. If relay check is impossible without auth, skip it and report `NEW <version>`.
- **`zsp apk` (old name):** Show deprecation message pointing to `zsp utils extract-apk`. Remove in a future release.

## Acceptance Criteria

### `zsp utils check-releases`

- [ ] `zsp utils check-releases <config>` exits 0 with `NEW <version>` when a new upstream version exists
- [ ] `zsp utils check-releases <config>` exits 0 with `UP_TO_DATE` when no new version exists
- [ ] `zsp utils check-releases <config>` exits 1 on source errors
- [ ] No APK is downloaded
- [ ] No events are built, signed, or published
- [ ] No Blossom uploads occur
- [ ] ETag cache is used and updated
- [ ] Output is machine-parseable (stdout has exactly `NEW <version>` or `UP_TO_DATE`)

### `zsp utils extract-apk`

- [ ] `zsp utils extract-apk <file.apk>` produces the same output as the old `zsp apk --extract <file.apk>`
- [ ] `zsp apk` shows a deprecation message

### `--skip-app-event`

- [ ] `zsp publish <config> --skip-app-event` publishes kind 30063 and 3063 events only
- [ ] No kind 32267 event is built or published
- [ ] APK is downloaded, parsed, and uploaded to Blossom normally
- [ ] Event signing and relay publishing work as usual for release events
- [ ] Combinable with other flags (`-q`, etc.)
- [ ] `--app-created-at-release` has no effect when `--skip-app-event` is used (kind 32267 is not built)

## Notes

- The `check-releases` workflow reuses `fetchRelease` → `selectAPK` (metadata only, no download) → `checkExistingAsset` (relay query) → report. It reuses the existing source pipeline.
- zindex parses stdout to get the version string and decide whether to record `upstream_version` / `upstream_detected_at` for the fallback grace period.
- The existing `--check` flag on publish is different: it validates that a config can successfully fetch an arm64-v8a APK (downloads and parses it). `check-releases` is about version availability without downloading.
- `--skip-app-event` is used during takeover after zindex has copied the developer's kind 32267 and re-signed it with the indexer key. zsp only handles the new release (APK download, upload, 30063/3063 events). The 32267 is already on the relay.
