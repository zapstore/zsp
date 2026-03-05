---
description: Non-negotiable invariants — correctness, security, CLI behavior, event integrity
alwaysApply: true
---

# zsp — Invariants

These are non-negotiable. Violating any invariant is a bug.

## Event Integrity

- Published events MUST conform to NIP-82 (kinds 32267, 30063, 3063) exactly.
- APK SHA-256 in the asset event MUST match the actual downloaded file — computed locally, never trusted from remote.
- `apk_certificate_hash` MUST be the SHA-256 of the DER-encoded signing certificate.
- Events MUST be signed before publishing. Unsigned events must never reach a relay.

## Security

- Private keys (`SIGN_WITH=nsec1...`) must never be logged, printed, or included in error messages.
- Keystore passwords must never be logged.
- `ui.SanitizeErrorMessage` must wrap all errors before printing to stderr.

## CLI Behavior

- Status output goes to stderr. Data output (JSON, event JSON) goes to stdout.
- Exit 0 = success. Exit 1 = error. Exit 130 = Ctrl+C / context cancelled.
- `--quiet` mode must produce no interactive prompts and no status output.
- `--offline` mode must not make any network calls (no relay publish, no Blossom upload).
- `--check` mode must exit 0 and print the package ID on success, exit 1 on failure.

## APK Selection

- The selected APK MUST support `arm64-v8a` architecture (verified by `apkInfo.IsArm64()`).
- `--check` must fail if the selected APK is not arm64-v8a.

## Context Cancellation

- All long-running operations must respect `context.Context`.
- Ctrl+C must cleanly cancel in-progress downloads, relay connections, and browser sessions.
- No goroutine leaks on cancellation.

## Error Handling

- All errors must be wrapped with context: `fmt.Errorf("doing X: %w", err)`.
- Errors must propagate up; never swallowed silently.
- User-facing error messages must be actionable, not raw Go error strings.
