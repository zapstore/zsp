---
date: 2026-08-03
tags: [indexer, soft-fail, optional, metadata, changelog, repo-config]
problem: Optional non-APK fetches (zapstore.yaml, metadata, changelog) aborted publishes that have fallbacks
---

# DEC-002 — Soft-fail optional non-APK fetches

## Problem

Indexer publishes hard-failed on optional overlays: GitHub API 403 probing for repo `zapstore.yaml`, missing `CHANGELOG.md` via `release_notes`, and similar non-APK fetches — even though each has a fallback (indexer YAML, forge release body / empty, APK-extracted fields).

## Context

`--indexer-mode` and normal publish both prefer optional enrichments when present. APK acquisition and integrity remain mandatory. Metadata sources were already non-fatal; repo config and configured `release_notes` were not.

## Decision

Soft-fail anything that is not the APK when a fallback exists:

1. **Repo `zapstore.yaml`** — fetch via `raw.githubusercontent.com` (no API quota) through `DoWithTorFallback`. Any fetch failure except context cancel/deadline → keep indexer YAML.
2. **Metadata sources** — already non-fatal; warn and continue with APK / config fields.
3. **Changelog / `release_notes`** — on fetch or local-file failure, warn and keep forge release changelog (or empty).

## Options Considered

- **Hard-fail non-404 errors** — rejected; aborts batch indexing for optional enrichments
- **Soft-fail only 403/429 for repo config** — incomplete; 5xx / network / missing CHANGELOG still abort
- **Soft-fail all optional non-APK fetches (chosen)** — APK path stays strict; overlays degrade gracefully

## Rationale

Prefer-when-present overlays must not block publish. Context cancellation stays fatal so Ctrl+C still works.

## How to Avoid This Problem Next Time

- If a resource is not the APK and has a fallback, soft-fail with a warning — do not return an error up the publish path
- Only context cancel/deadline should abort optional fetches
- Reference: `internal/source/repo_config.go`, `gatherMetadata` in `internal/workflow/workflow.go`
