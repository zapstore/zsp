# WORK-002 — Keystore UX Improvement

**Feature:** Certificate linking inline prompt (during `zsp publish`)
**Status:** In Progress

## Background

When `zsp publish` detects no certificate link, it prompts the user inline.
The previous UX had two problems:

1. A `─── Link Signing Certificate ───` section header that didn't match the
   surrounding step-tracker visual style and added noise.
2. A free-text path prompt that accepted `.jks`/`.keystore` only to immediately
   error with a keytool conversion command — a dead end mid-publish.

## Tasks

- [x] 1. Remove the `PrintSectionHeader("Link Signing Certificate")` call
  - Files: `internal/workflow/workflow.go`
  - Notes: The `Dim(...)` description line and blank line are enough context.

- [x] 2. Replace free-text path prompt with `SelectOption` asking key format
  - Files: `internal/workflow/workflow.go`
  - Options: "Android Keystore (.jks / .keystore)", "PKCS12 (.p12 / .pfx)", "PEM certificate + key files"
  - Default cursor: 0 (Android Keystore — most common)

- [x] 3. JKS branch: detect `keytool` in PATH
  - Files: `internal/workflow/workflow.go`, `internal/identity/x509.go`
  - If `keytool` not found: print the manual `keytool -importkeystore` command and return nil (non-fatal, same as quiet-mode warning).
  - If found: prompt for JKS path and alias (blank = first alias), then warn user that keystore password is about to be requested (Ctrl+C to skip), prompt password, run conversion to a temp `.p12` with a random internal password, load it, delete temp file.

- [x] 4. PKCS12 branch: same as before but now reached via selector, not extension sniff
  - Warn before password prompt (Ctrl+C to skip).

- [x] 5. PEM branch: unchanged logic, reached via selector

- [x] 6. `loadKeystoreFile` in workflow.go: add JKS auto-conversion path
  - Files: `internal/workflow/workflow.go`

- [x] 7. `JKSConversionHelp` in identity/x509.go: keep for the "no keytool" fallback path
  - Update to not say "Error:" at the start (already done in previous session).

- [x] 8. Self-review against INVARIANTS.md

## Decisions

### 2026-03-09 — Temp PKCS12 password

**Context:** Converting JKS → PKCS12 requires a destination password for keytool.
**Options:** Prompt user, use empty string, use random bytes.
**Decision:** Random 16-byte hex string, never shown to user.
**Rationale:** The file is deleted immediately after loading. A random password
prevents accidental reuse and avoids any prompt. The user only ever enters their
*source* keystore password.

### 2026-03-09 — Non-fatal on skip / no keytool

**Context:** Certificate linking is optional — publish must not be blocked.
**Decision:** Both "no keytool" and "user Ctrl+C on password" return `nil` (non-fatal),
printing the manual command so the user can link separately via `zsp identity --link-key`.

### 2026-03-09 — Selector default

**Context:** Most Android developers use `.jks` or `.keystore` files.
**Decision:** Default cursor on "Android Keystore" option.

## Test Coverage

| Scenario | Expected | Status |
|----------|----------|--------|
| User selects JKS, keytool present, correct password | Proof published | [ ] |
| User selects JKS, keytool missing | Manual command printed, publish continues | [ ] |
| User Ctrl+C on password prompt | Non-fatal, publish continues | [ ] |
| User selects PKCS12, correct password | Proof published (existing behavior) | [ ] |
| User selects PEM | Proof published (existing behavior) | [ ] |

## On Merge

Promote the temp-password and non-fatal decisions to `spec/knowledge/` if not obvious from code.
