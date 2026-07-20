# WORK-003 — Fastlane Metadata Source

**Feature:** Repository-backed Fastlane metadata enrichment
**Status:** Complete

## Tasks

- [x] 1. Add Fastlane metadata retrieval for GitHub, GitLab, and Gitea/Codeberg repositories.
  - Files: `internal/source/metadata.go`, `internal/source/fastlane.go`
  - Read the Android Fastlane metadata layout and expose text and image fields.
- [x] 2. Make automatic metadata selection Fastlane-first.
  - Files: `internal/source/metadata.go`
  - When `metadata_sources` is unset, use the repository-native API only if Fastlane is absent.
- [x] 3. Document and test the source.
  - Files: `internal/source/metadata_test.go`, `internal/config/config.go`, `internal/config/wizard.go`, `README.md`

## Test Coverage

| Scenario | Expected | Status |
|----------|----------|--------|
| GitHub Fastlane metadata | Text, icon, and screenshots are imported | [x] |
| GitLab Fastlane metadata | Text, icon, and screenshots are imported | [x] |
| Codeberg/Gitea Fastlane metadata | Text, icon, and screenshots are imported | [x] |
| Missing `en-US` locale | Deterministic locale fallback is used | [x] |
| Absent Fastlane directory | Native repository metadata is used | [x] |
| Missing optional Fastlane files | Available fields still import | [x] |
| Cancelled context | HTTP requests stop cleanly | [x] |

## Decisions

### 2026-07-17 — Automatic metadata fallback

**Context:** Repository README/API metadata is less accurate than Fastlane but remains useful when Fastlane is absent.
**Decision:** With no explicitly configured metadata sources, try Fastlane first and call only the configured repository's native source (`github` or `gitlab`) when the Fastlane directory does not exist.
**Rationale:** This prefers publisher-maintained store metadata without performing unnecessary fallback API calls.

### 2026-07-17 — Initial host coverage

**Context:** Fastlane is stored inside the source repository.
**Decision:** Support GitHub and GitLab initially.
**Rationale:** They are existing repository metadata hosts and cover the requested repository-backed use case.

### 2026-07-17 — Gitea/Codeberg host coverage

**Context:** Codeberg (and other Gitea/Forgejo forges) already ship APKs via `SourceGitea`; Fastlane lives in the same tree.
**Decision:** Reuse the shared Fastlane layout parser with the Gitea contents API (`/api/v1/repos/{owner}/{repo}/contents/...`). Auto-detect Codeberg plus hostnames containing `gitea` or `forgejo`; allow explicit `release_source.type: gitea` for opaque self-hosted hosts. Automatic metadata is Fastlane-only (no native Gitea repo-metadata source yet).
**Rationale:** Gitea's contents response matches GitHub's shape, so one adapter covers Codeberg and self-hosted Gitea/Forgejo.

## Spec Issues

_None_

## Progress Notes

**2026-07-17:** Work packet created.
**2026-07-17:** Implemented Fastlane discovery, automatic fallback, documentation, and mocked HTTP tests.

## On Merge

Delete this work packet. Promote the automatic-fallback decision to `spec/knowledge/` if it remains non-obvious.
