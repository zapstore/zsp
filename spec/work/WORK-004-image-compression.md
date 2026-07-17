# WORK-004 — Default Image Compression

**Feature:** Image compression for published icons and screenshots
**Status:** In Progress

## Tasks

- [x] 1. Add centralized, format-preserving image processing.
- [x] 2. Apply 512px icon and 1440px screenshot width limits before hashing.
- [x] 3. Add `--no-compress` and document normal status output.
- [x] 4. Cover upload, offline URL resolution, preview, and batch-signing paths.
- [x] 5. Add table-driven media tests.
- [x] 6. Self-review against INVARIANTS.md.

## Test Coverage

| Scenario | Expected | Status |
|----------|----------|--------|
| PNG icon wider than 512px | Resized and remains PNG | [x] |
| JPEG screenshot wider than 1440px | Resized/re-encoded and remains JPEG | [x] |
| Unsupported image format | Preserved without format conversion | [x] |
| `--no-compress` | Original bytes and MIME type retained | [x] |
| Offline and upload processing | Identical transformed bytes and hash | [x] |

## Decisions

### 2026-07-17 — Preserve source image format

**Context:** Icons and screenshots are referenced by content hash in Blossom URLs.
**Options:** Convert all assets to WebP, preserve source format, or make WebP opt-in.
**Decision:** Preserve PNG/JPEG/WebP and use format-specific optimization; unsupported formats remain unchanged.
**Rationale:** NIP-82 does not mandate WebP, and format conversion would reduce compatibility while changing content hashes.

### 2026-07-17 — Width limits

**Context:** Store-compatible assets can be much larger than clients need.
**Decision:** Cap icon width at 512px and screenshot width at 1440px without upscaling or changing aspect ratio.

## Spec Issues

_None_

## Progress Notes

**2026-07-17:** Implemented media processing, CLI flag, upload/offline/preview integration, and tests.

## On Merge

Delete this work packet. Promote non-obvious decisions to `spec/knowledge/DEC-XXX-short-title.md` if they remain useful.
