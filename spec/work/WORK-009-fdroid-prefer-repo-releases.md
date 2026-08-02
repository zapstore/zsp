# WORK-009 — Prefer Repository Releases over F-Droid/Izzy

**Feature:** (inline — APK source selection for fdroid/izzy release_source)
**Status:** Complete

## Tasks

- [x] 1. When `release_source` is F-Droid/Izzy and `repository` is a forge (GitHub/GitLab/Gitea), wrap with a source that tries forge releases first.
  - Files: `internal/source/prefer_repo.go`, `internal/source/source.go`
- [x] 2. Fall back to F-Droid/Izzy when the repository has no releases or no APK assets.
- [x] 3. Preserve `ErrNotModified` from the repository source (already-published gate).
- [x] 4. Delegate cache/download interfaces to the selected active source.
- [x] 5. Table-driven tests with stub sources; `gofmt`, `go test`, `go vet`.
- [x] 6. Self-review against INVARIANTS.md.

## Test Coverage

| Scenario | Expected | Status |
|----------|----------|--------|
| F-Droid + GitHub repo with APK releases | Use GitHub release | [x] |
| F-Droid + GitHub repo with no APKs | Fall back to F-Droid | [x] |
| F-Droid + GitHub repo with no releases (error) | Fall back to F-Droid | [x] |
| Repository returns ErrNotModified | Propagate; do not fall back | [x] |
| F-Droid without repository | Plain F-Droid source | [x] |
| Cancelled context on repo fetch | Propagate cancellation | [x] |

## Decisions

### 2026-08-02 — Composite source, not GetSourceType change

**Context:** Configs often set `release_source` to F-Droid while `repository` points at GitHub.
**Options:** Change GetSourceType precedence; rewrite configs; composite source that probes at fetch time.
**Decision:** Composite `preferRepoSource` created in `NewWithOptions`.
**Rationale:** Preference depends on whether the forge actually has APK releases — a static type cannot know.

### 2026-08-02 — Fall back on forge errors (except cancel / NotModified)

**Context:** GitHub may 404, rate-limit, or return desktop-only releases.
**Decision:** Any unsuccessful forge fetch (error other than ErrNotModified/cancel, or no valid APKs) falls back to F-Droid.
**Rationale:** F-Droid remains a reliable APK source; indexer should not fail when the forge is empty.

## Spec Issues

_None_

## Progress Notes

**2026-08-02:** Implemented `preferRepoSource`; promoted DEC-001.
**2026-08-02:** Generalized via `IsForgeReleaseSource` — prefer any forge repo (GitHub/GitLab/Gitea) over any non-forge release_source (F-Droid, web, …).
**2026-08-02:** Fall back when forge release parse/selection/download fails (`FallbackRelease` + workflow/`--check` retry).
