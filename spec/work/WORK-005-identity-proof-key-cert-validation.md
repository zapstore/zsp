# WORK-005 — Identity Proof Key/Cert Validation

**Feature:** (bugfix — no FEAT; NIP-C1 correctness)
**Status:** Complete

## Tasks

- [x] 1. Validate private key matches certificate before signing
  - Files: `internal/identity/x509.go`
  - `ValidateKeyCertPair` + call from `GenerateIdentityProof` and `LoadPEM`
- [x] 2. Self-verify generated proof against cert before return
  - Files: `internal/identity/x509.go`
- [x] 3. Derive cert hash from the signing certificate (not a separately passed APK hash)
  - Files: `internal/identity/x509.go`, `main.go`, `internal/workflow/workflow.go`
  - Fixes publish-time linking that discarded keystore cert and stamped APK cert hash
- [x] 4. During publish, verify existing 30509 against APK cert; warn and re-link if broken
  - Files: `internal/workflow/workflow.go`
- [x] 5. Reject keystore whose cert hash ≠ APK signing cert hash when linking during publish
- [x] 6. Table-driven tests for match/mismatch/PEM/self-verify
  - Files: `internal/identity/x509_test.go`
- [x] 7. Self-review against INVARIANTS.md

## Test Coverage

| Scenario | Expected | Status |
|----------|----------|--------|
| Matching RSA/ECDSA/Ed25519 key+cert | Proof generated; self-verify Valid | [x] |
| Mismatched RSA key+cert | `ErrKeyCertMismatch`, no proof | [x] |
| Nil certificate | Error, no proof | [x] |
| LoadPEM with wrong key file | `ErrKeyCertMismatch` | [x] |
| LoadPEM with matching pair | Loads successfully | [x] |
| Cert hash derived from cert | Equals `ComputeCertHash(cert)` | [x] |

## Decisions

### 2026-07-27 — Derive cert hash inside GenerateIdentityProof

**Context:** Workflow passed APK `certHash` separately while signing with keystore `privateKey`, discarding keystore `cert`. Wrong alias → proof stamped with APK hash but signed by unrelated key.
**Options:** Keep separate hash arg + validate elsewhere; take `*x509.Certificate` and derive hash.
**Decision:** Take certificate; derive hash; validate pair; self-verify.
**Rationale:** Makes invalid states unrepresentable at the API boundary.

## Spec Issues

_None_

## Progress Notes

**2026-07-27:** Root cause confirmed in `checkAndLinkCertificate` (`_ = cert` + APK hash). Implemented validation, self-verify, publish-time proof health check.

## On Merge

Delete this work packet. Promote decision above to `spec/knowledge/DEC-XXX-identity-proof-key-cert.md` if useful.
