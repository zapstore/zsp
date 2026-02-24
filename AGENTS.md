# zsp — Agent Instructions

CLI for publishing Android apps to Nostr relays. Used by [Zapstore](https://zapstore.dev).

## Project Structure

- `main.go` — Entry point, subcommand dispatch (publish, identity, apk)
- `internal/source/` — APK acquisition (GitHub, GitLab, F-Droid, web, local)
- `internal/apk/` — APK parsing and metadata extraction
- `internal/nostr/` — Signing (NIP-07, NIP-46, nsec), relay publishing, NIP-82 events
- `internal/workflow/` — Publish flow orchestration
- `internal/config/` — YAML config and wizard
- `internal/ui/` — Prompts, spinners, selection
- `internal/picker/` — APK selection/ranking
- `internal/identity/` — X.509 and NIP-C1 identity proofs

## Conventions

- Go 1.24+, standard library style. Use `internal/` for private packages.
- Config: `zapstore.yaml` (see `testdata/configs/` for examples).
- Reference existing patterns when adding sources or features (e.g. `internal/source/github.go`).
