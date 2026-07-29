# WORK-007 — Indexer Mode Completion Output

**Feature:** `--indexer-mode` publish mode
**Status:** In Progress

## Tasks

- [x] 1. Define indexer mode as a complete online publish only.
  - Reject `--check`, `--offline`, and npub external-signing mode.
- [x] 2. Delay machine-readable success output until relay publishing and Blossom uploads succeed.
  - Files: `internal/workflow/workflow.go`
- [x] 3. Keep indexer mode output terminal and singular.
  - Success writes one app-ID JSON object to stdout.
  - Failure writes one sanitized error JSON object to stderr.
  - Non-fatal relay diagnostics are suppressed in CI.
- [x] 4. Add focused flag and output tests.
- [x] 5. Run formatting, tests, vet, and build.

## Test Coverage

| Scenario | Expected | Status |
|----------|----------|--------|
| CI online publish | App ID is emitted only after the full workflow completes | [x] |
| CI plus check/offline | Rejected before work begins | [x] |
| CI plus npub signer | Rejected before relay publishing | [x] |
| Deferred Blob upload failure | No CI success payload | [ ] |

## Decisions

### 2026-07-29 — Terminal CI output

**Context:** Relay events reference deterministic Blossom URLs, but the actual Blob uploads occur after relay publishing.
**Options:** Emit a success payload after relay publication, stream progress records, or buffer the terminal result.
**Decision:** Emit exactly one terminal result.
**Rationale:** CI consumers can treat stdout as a successful, complete publish signal and stderr as a failed, complete publish signal.

## Spec Issues

_None_

## Progress Notes

**2026-07-29:** Moved machine-readable publish output to after deferred Blossom uploads and constrained CI to complete online publishing.
