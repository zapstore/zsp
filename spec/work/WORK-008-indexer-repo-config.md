# WORK-008 — Indexer Mode Repo Config

**Feature:** `--indexer-mode` prefer repo-root `zapstore.yaml`
**Status:** Complete

## Tasks

- [x] 1. Fetch `zapstore.yaml` from repository root on supported forges (GitHub, GitLab, Gitea/Codeberg).
  - Default branch only (`HEAD` / contents API default).
  - Filename exactly `zapstore.yaml` (no variants).
- [x] 2. Full replace of the passed indexer YAML when the repo file exists — no field merge.
- [x] 3. Keep indexer YAML when forge unsupported or file absent (404).
- [x] 4. Keep indexer YAML on present-but-invalid repo YAML; hard-fail on non-404 API errors.
- [x] 5. Wire into publish path after `loadConfig`, before `Validate`.
- [x] 6. Tests with mocked HTTP; help text update.
- [x] 7. Self-review against INVARIANTS.md; `gofmt`, `go test`, `go vet`.

## Test Coverage

| Scenario | Expected | Status |
|----------|----------|--------|
| GitHub repo file present | Full replace with repo config | [x] |
| GitHub 404 | Keep indexer config | [x] |
| Unsupported forge | Keep indexer config, no HTTP | [x] |
| GitLab / Gitea present | Full replace | [x] |
| Malformed repo YAML | Keep indexer config | [x] |
| Cancelled context | Error | [x] |

## Decisions

### 2026-07-29 — Full replace, not merge

**Context:** Indexer YAML vs repo `zapstore.yaml` priority.
**Options:** Field-wise merge (repo wins non-empty), full replace, repo metadata-only overlay.
**Decision:** Full replace when the file exists.
**Rationale:** The repo owns the complete publish config; indexer YAML is a fallback seed (needs `repository` for discovery).

### 2026-07-29 — Skip pubkey mismatch on repo fetch

**Context:** `config.Load` rejects `pubkey` ≠ `SIGN_WITH`. Indexer signs with its own key.
**Decision:** Parse via `config.Parse` only; do not apply Load's pubkey check.
**Rationale:** Indexer mode intentionally publishes under a different identity than the repo config's `pubkey` field.

## Spec Issues

_None_

## Progress Notes

**2026-07-29:** Implemented `source.ResolveIndexerConfig` and wired into `runPublishCommand`.

**2026-08-02:** Malformed repository `zapstore.yaml` now falls back to the indexer-provided config instead of hard-failing.
