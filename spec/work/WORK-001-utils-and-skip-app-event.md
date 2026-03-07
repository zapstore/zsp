# WORK-001 — Utils Subcommand and `--skip-app-event` Flag

**Feature:** FEAT-002-utils-and-skip-app-event.md
**Status:** Not Started

## Tasks

- [ ] 1. Create `zsp utils` subcommand infrastructure
  - Files: `internal/cli/options.go`, `main.go`
  - Add `CommandUtils Command = "utils"` constant
  - Add `UtilsOptions` struct (initially empty, subcommand dispatched by args)
  - Add `case "utils":` to the main switch in `ParseCommand`
  - Parse first positional arg as the utils operation: `extract-apk`, `check-releases`
  - Add `runUtilsCommand` to `main.go` that dispatches to the appropriate handler

- [ ] 2. Move `extract-apk` from `zsp apk` to `zsp utils extract-apk`
  - Files: `main.go`, `internal/cli/options.go`
  - `zsp utils extract-apk <file.apk>` calls the existing `extractAPKMetadata` function
  - Same behavior, just a new entry point
  - Keep `zsp apk --extract` working but print a deprecation warning to stderr:
    `WARNING: 'zsp apk' is deprecated, use 'zsp utils extract-apk' instead`

- [ ] 3. Implement `zsp utils check-releases`
  - Files: `main.go` (new `checkReleases` function)
  - Takes a config file path as positional arg (same config format as `publish`)
  - Load and validate config (reuse `loadConfig` logic, minus wizard/interactive)
  - Create source via `source.NewWithOptions`
  - Call `src.FetchLatestRelease(ctx)` — ETag cache is used and committed
  - Select best APK from release assets (reuse `picker.FilterAPKs` + `picker.DefaultModel.RankAssets`)
  - Extract version from release + selected asset
  - Check if version already exists on relay: reuse `publisher.CheckExistingAsset(ctx, packageID, version)`
    - For `check-releases`, the package ID may need to come from the config (app_id or derived from repository URL) since we don't download/parse the APK
    - If relay check fails (unreachable, no auth) → conservative: report `NEW`
  - Output to stdout:
    - `NEW <version>` if new version found
    - `UP_TO_DATE` if version already on relay or ETag 304
  - Exit 0 on success, exit 1 on source errors
  - No APK download, no metadata fetch, no signing, no publishing

- [ ] 4. Add `--skip-app-event` flag to publish options
  - Files: `internal/cli/options.go`
  - Add `SkipAppEvent bool` to `PublishOptions` struct
  - Register flag: `fs.BoolVar(&opts.Publish.SkipAppEvent, "skip-app-event", false, "Publish only release events, skip app metadata (kind 32267)")`

- [ ] 5. Implement `--skip-app-event` in workflow
  - Files: `internal/workflow/workflow.go`, `internal/nostr/events.go`
  - In `Execute()`, when `SkipAppEvent` is true:
    - Run the full pipeline (fetch, download, parse, sign, upload) as normal
    - In the event building step (`BuildEventSet` or `uploadAndBuildEvents`): skip building the kind 32267 event
    - Only build and publish kind 30063 (Release) and kind 3063 (Asset) events
    - The `EventSet` struct already has separate fields for app metadata vs releases — set the metadata event to nil when `SkipAppEvent` is true
    - The signing and publishing code should handle nil gracefully (skip nil events)

- [ ] 6. Ensure ETag cache is committed in `check-releases`
  - Files: `main.go` (or wherever cache management lives)
  - `FetchLatestRelease` uses the ETag cache internally. Verify the cache is committed after `check-releases` exits (call `commitCache()` equivalent).
  - For ETag 304 (not modified), `FetchLatestRelease` may return a cached release or a "not modified" indicator — handle both cases and report `UP_TO_DATE`.

- [ ] 7. Update help text
  - Files: `internal/help/help.go`
  - Add `zsp utils` help with subcommands: `extract-apk`, `check-releases`
  - Update root help to show `utils` instead of `apk`
  - Keep `apk` in help briefly (with deprecation note) during transition

- [ ] 8. Tests
  - Files: `internal/workflow/workflow_test.go`, integration tests
  - `check-releases` with new version available → stdout contains `NEW X.Y.Z`
  - `check-releases` with version already on relay → stdout contains `UP_TO_DATE`
  - `check-releases` with source error → exit 1
  - `check-releases` does not download APK or create events
  - ETag cache is updated after `check-releases`
  - `extract-apk` produces same output as old `zsp apk --extract`
  - `--skip-app-event` publishes kind 30063 and 3063 but not kind 32267
  - `zsp apk --extract` still works (with deprecation warning)

- [ ] 9. Update project documentation
  - Files: `README.md`
  - Update CLI Reference: add `zsp utils` subcommand with `extract-apk` and `check-releases`
  - Update Flags table: add `--skip-app-event`
  - Add deprecation note for `zsp apk`
  - Document that `--app-created-at-release` has no effect when `--skip-app-event` is used (kind 32267 is not built)

- [ ] 10. Self-review against INVARIANTS.md
  - No events published in `check-releases` (event integrity — no partial publishes)
  - No private keys needed for `check-releases` (security — doesn't sign)
  - `--skip-app-event` still signs and publishes (just fewer events)
  - Exit codes: 0 for success (both NEW and UP_TO_DATE), 1 for errors (CLI behavior invariant)
  - stdout is machine-parseable in `check-releases` (CLI behavior — data to stdout, status to stderr)

## Test Coverage

| Scenario | Expected | Status |
|----------|----------|--------|
| `check-releases`: new upstream version | stdout: `NEW 2.0.0`, exit 0 | [ ] |
| `check-releases`: version already on relay | stdout: `UP_TO_DATE`, exit 0 | [ ] |
| `check-releases`: source has no releases | stderr: error, exit 1 | [ ] |
| `check-releases`: repo doesn't exist | stderr: error, exit 1 | [ ] |
| `check-releases`: ETag 304 (unchanged) | stdout: `UP_TO_DATE`, exit 0 | [ ] |
| `check-releases`: relay unreachable | stdout: `NEW <version>`, exit 0 (conservative) | [ ] |
| `check-releases`: no APK downloaded | No temp files created | [ ] |
| `extract-apk`: valid APK | JSON metadata on stdout | [ ] |
| `extract-apk`: not an APK | stderr: error, exit 1 | [ ] |
| `zsp apk --extract` (deprecated) | Works with deprecation warning | [ ] |
| `--skip-app-event`: normal publish | kind 30063 + 3063 published, no kind 32267 | [ ] |
| `--skip-app-event`: APK uploaded | Blossom upload succeeds as normal | [ ] |

## Decisions

### 2026-03-06 — Utils subcommand instead of publish flag

**Context:** Needed a way for zindex to check for upstream versions without publishing. Originally considered `--dry-run` on publish.
**Decision:** Create `zsp utils check-releases` as a standalone operation under a new `utils` subcommand. Move existing `zsp apk --extract` there too.
**Rationale:** Checking releases isn't publishing — it shouldn't live under `zsp publish`. `utils` provides a clean home for operational tools. `--dry-run` on publish would be confusing since publish already has `--check`, `--offline`, etc.

### 2026-03-06 — `--skip-app-event` naming

**Context:** Need a flag to skip kind 32267 during takeover publishing.
**Decision:** Name it `--skip-app-event` rather than `--releases-only`.
**Rationale:** More explicit about what it does (skips the app event). `--releases-only` could be misread as "only check releases" vs "only publish releases."

### 2026-03-06 — Machine-parseable stdout for check-releases

**Context:** zindex needs to parse the output to extract version strings.
**Decision:** Exactly `NEW <version>` or `UP_TO_DATE` on stdout. Nothing else.
**Rationale:** Simple, unambiguous, easy to parse. No JSON wrapper needed for two possible outputs.

## Spec Issues

- **Open:** How does `check-releases` determine the package ID for the relay existing-asset check without downloading the APK? It currently uses `apkInfo.PackageID` (from APK parsing). For `check-releases`, the package ID may need to come from the config's d-tag or be derived from the repository URL. Need to verify this works for all source types.

## Progress Notes

_Not started_
