# WORK-006 — Direct JKS Loading

**Feature:** JKS support for identity linking and publish-time certificate linking  
**Status:** Complete

## Tasks

- [x] Decode Java KeyStore files with a pure-Go dependency.
- [x] Select a private-key entry by alias, defaulting only when exactly one exists.
- [x] Support a distinct `KEYSTORE_KEY_PASSWORD`, falling back to `KEYSTORE_PASSWORD`.
- [x] Validate the extracted private key and certificate before returning them.
- [x] Route `identity --link-key` and publish-time linking through the direct loader.
- [x] Remove `keytool` process invocation and conversion-only help.
- [x] Add JKS tests for supported key types, aliases, passwords, malformed stores, and mismatched pairs.

## Decisions

### 2026-07-27 — Decode JKS directly

**Context:** JKS is a standard Android signing-keystore format, but linking required a Java `keytool` conversion.

**Decision:** Use `github.com/pavlo-v-chernykh/keystore-go/v4` to decode JKS in-process.

**Rationale:** Identity linking works in CI and on developer machines without a Java installation, while retaining private-key/certificate validation.

### 2026-07-27 — Require aliases for ambiguous stores

**Decision:** Select the sole private-key entry automatically; require `--key-alias` or an interactive selection for stores with multiple private keys.

**Rationale:** Choosing the first key can create an invalid proof for a different APK certificate.

## On Merge

Promote the JKS-loading decision to `spec/knowledge/` if it remains relevant.
