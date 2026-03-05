---
description: Architecture — package layout, internal boundaries, publish workflow, source/nostr patterns
alwaysApply: true
---

# zsp — Architecture

## Core Principle

`zsp` is a single-binary CLI. All logic lives in `internal/`. `main.go` dispatches subcommands and handles exit codes — nothing else.

## Package Layout

```
main.go                  Entry point, subcommand dispatch (publish, identity, apk)
internal/
  source/                APK acquisition — one file per source type
  apk/                   APK parsing, certificate extraction, icon extraction
  nostr/                 Signing (nsec, NIP-46, NIP-07), relay publishing, event builders
  workflow/              Publish flow orchestration (the only place that wires sources → nostr)
  config/                YAML config loading, validation, wizard
  ui/                    Prompts, spinners, selection, color output
  picker/                APK ranking and selection (ML model + heuristics)
  identity/              X.509 loading, NIP-C1 proof generation and verification
  cli/                   Flag parsing, signal handling, options structs
  help/                  Help text rendering
  blossom/               Blossom CDN client (upload)
testdata/
  configs/               Example YAML configs (one per source type)
  fixtures/              Test fixtures
```

## Dependency Rules

- `workflow` is the only package that imports across domains (source + nostr + apk + picker)
- `main.go` imports `workflow`, `config`, `cli`, `ui`, `help`, `identity`, `apk`, `picker`, `source`
- `nostr`, `source`, `apk`, `picker`, `identity` must not import each other
- `ui` must not import business logic packages

## Adding a New Source

Reference `internal/source/github.go`. Each source implements the `Source` interface:
- `FetchLatestRelease(ctx) (*Release, error)`
- `Download(ctx, asset, destDir, progress) (path, error)`

Auto-detection from URL is in `source.go`. Add detection logic there after implementing the source.

## Signing Flow

`internal/nostr/signer.go` — `NewSignerWithOptions(ctx, signWith, opts)` returns a `Signer`.
`signWith` is the `SIGN_WITH` env var: `nsec1...`, `npub1...`, hex key, `bunker://...`, or `browser`.

## Event Building

`internal/nostr/events.go` — builds NIP-82 events (32267, 30063, 3063) and NIP-C1 (30509).
Event structure must match the NIP-82 spec exactly (see `README.md` for links).
