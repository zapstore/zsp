---
date: 2026-08-02
tags: [source, fdroid, izzy, github, gitlab, gitea, release-selection]
problem: Non-forge release_source ignored APKs already published on the forge repository
---

# DEC-001 — Prefer forge repository releases over non-forge release_source

## Problem

Indexer configs often set `release_source` to F-Droid, IzzyOnDroid, or a web URL while `repository` points at GitHub/GitLab/Codeberg. When the forge already publishes APKs, the configured release_source can lag upstream or serve rebuilds instead of upstream artifacts.

## Context

`GetSourceType` prefers `release_source` over `repository`, so a non-forge URL always selected that client. Changing static precedence would break apps that only ship APKs via F-Droid/web.

## Decision

When `release_source` is not a forge API source (`IsForgeReleaseSource`) and `repository` is GitHub, GitLab, or Gitea/Forgejo/Codeberg, wrap the primary source in `preferRepoSource`. At fetch time, try forge releases first; if they include selectable APKs (or return `ErrNotModified`), use the forge. Otherwise fall back to the configured release_source. If the forge path later fails during selection, download, or APK parsing, call `FallbackRelease` and retry with F-Droid/Izzy/web.

## Options Considered

- **Change GetSourceType precedence** — cannot know whether the forge has APKs without a network check
- **Rewrite all indexer YAML to drop release_source** — large operational cost; non-forge sources remain needed as fallback
- **F-Droid-only wrapping** — too narrow; web release sources have the same issue
- **Composite source at fetch time for all non-forge primaries (chosen)** — probes any forge release source, falls back cleanly

## Rationale

Preference is a runtime property of the repository, not a config type. `IsForgeReleaseSource` is the single list of forges with native release APIs. Keeping the configured release_source as fallback preserves coverage for apps without forge APK assets. Cancellation and `ErrNotModified` are not treated as “no releases” so offline/cancel and already-published gates stay correct.

## How to Avoid This Problem Next Time

- Do not assume `release_source` URL alone defines the best APK origin when `repository` is also a forge
- Use `config.IsForgeReleaseSource` instead of hardcoding GitHub/GitLab/Gitea switches for release preference
- New dual-origin sources should follow `preferRepoSource` in `internal/source/prefer_repo.go`
- Fall back on forge miss/error; propagate `context.Canceled` / `ErrNotModified` without falling back
