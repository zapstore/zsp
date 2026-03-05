---
description: Quality expectations — when to spec, testing, anti-patterns, AI workflow
alwaysApply: true
---

# zsp — Quality Bar

## When to Create a Feature Spec

Create a spec if the work:

- Adds a new APK source or metadata source
- Changes signing or event publishing behavior
- Modifies the publish workflow orchestration
- Adds a new subcommand or significant flag
- Touches APK selection/ranking logic
- Could affect correctness of published events

**Skip the spec** if:

- UI copy or color changes
- Adding a flag alias
- Bug fix with obvious cause and fix
- Dependency update with no API changes

## Testing

- Table-driven tests. Use `testdata/` for fixtures and example configs.
- Test APK parsing against real fixture APKs where possible.
- Source tests should mock HTTP — no real network calls in tests.
- Test the happy path AND: missing files, bad URLs, cancelled context, malformed APKs.

## Implementation Expectations

- Reference the nearest existing source/pattern before writing new code.
- `internal/source/github.go` is the reference implementation for new sources.
- Keep `main.go` thin — dispatch only, no business logic.
- Prefer extending existing packages over creating new ones.

## Anti-Patterns

- Logging or printing private keys or passwords
- Network calls in `--offline` mode
- Blocking on context without a cancellation path
- Swallowing errors with `_ = err`
- Hardcoding relay or Blossom URLs (use env vars with defaults)

## Working With AI

- Spec-first for new sources, workflow changes, and event format changes.
- Work packets in `spec/work/` for non-trivial tasks.
- If a spec conflicts with the NIP-82/NIP-C1 protocol specs, stop and report — do not guess.
- Never modify `spec/guidelines/` without explicit permission.

## Knowledge Entries

After a work packet merges, promote non-obvious decisions to `spec/knowledge/DEC-XXX-*.md`. See `spec/knowledge/_TEMPLATE.md` for format and criteria.
