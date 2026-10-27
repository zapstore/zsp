# zsp — Agent Instructions

CLI and Go library for publishing Android apps to the Zapstore catalog.

[`SPEC.md`](SPEC.md) is the authority for behavior. If this file conflicts,
`SPEC.md` wins. The contracts it depends on are in
[`../product/specs/`](../product/specs/).

## Quick Reference

| What | Where |
|------|-------|
| Wizard, commands, `zapstore.yaml`, events, Go library | `SPEC.md` |
| Go conventions | `.cursor/rules/go.mdc` |
| Catalog listing rules | `../product/specs/catalog.md` |
| Admission and bans | `../product/specs/publisher-policy.md` |

## File Ownership

| Path | Owner | AI May Modify |
|------|-------|---------------|
| `SPEC.md` | Human | No (unless asked) |
| `zsp.go`, `internal/**`, `cmd/zsp/**` | Shared | Yes |

## Key Commands

```bash
go build -o zsp ./cmd/zsp # Build
go test ./...             # Tests
go vet ./...              # Lint
go mod tidy               # After dependency changes
```

## Project Rules

- Reference `internal/source/github.go` when adding new sources.
- `zsp.go` is the only root Go file: it re-exports `internal/release` so the
  import path stays `github.com/zapstore/zsp/v2`. Put library code in
  `internal/release`, and add a forwarder in `zsp.go` for any new public name.
- Outbound HTTP uses `internal/netpolicy`; local media paths use
  `internal/localpath`.
