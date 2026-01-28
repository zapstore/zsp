# zsp

A fast CLI tool for publishing Android apps to Nostr relays. Used by [Zapstore](https://zapstore.dev).

## Features

- **APK acquisition** from GitHub, GitLab, Codeberg, F-Droid, web pages, or local files
- **APK parsing** to extract package info, version, certificate fingerprint, icon, and permissions
- **Metadata enrichment** from GitHub, GitLab, F-Droid, or Google Play Store
- **Blossom uploads** for icons, screenshots, APKs
- **Nostr event signing** via private key, NIP-46 bunker, or browser extension (NIP-07)
- **Relay publishing** of compliant software events

## Installation

### From Source

```bash
go install github.com/zapstore/zsp@latest
```

### Pre-built Binaries

Download from [releases](https://github.com/zapstore/zsp/releases).

## Quick Start

```bash
zsp publish --wizard
```

The interactive wizard guides you through the setup process and helps determine the best options for your app.

---

## APK Sources

zsp supports multiple sources for fetching APKs. The source type is auto-detected from URLs.

### GitHub Releases

Fetches APKs from GitHub release assets. Automatically selects the best arm64-v8a APK.

```yaml
repository: https://github.com/AeonBTC/mempal
```

```bash
zsp publish -r github.com/AeonBTC/mempal
```

### GitLab Releases

Fetches APKs from GitLab release links.

```yaml
repository: https://gitlab.com/AuroraOSS/AuroraStore
```

For self-hosted GitLab without "gitlab" in the domain:

```yaml
release_source:
  url: https://git.mycompany.com/team/app
  type: gitlab
```

### Codeberg / Gitea / Forgejo

Fetches APKs from Gitea-compatible forges.

```yaml
repository: https://codeberg.org/Freeyourgadget/Gadgetbridge
```

### F-Droid

Fetches APKs from F-Droid or IzzyOnDroid repositories.

```yaml
# APKs from F-Droid, source code from GitHub
repository: https://github.com/AntennaPod/AntennaPod
release_source: https://f-droid.org/packages/de.danoeh.antennapod
```

### Web Sources

Fetch APKs from any URL with version extraction via CSS selectors, JSON APIs, or HTTP headers.

```yaml
# Extract version from HTML using CSS selector
repository: https://github.com/AntennaPod/AntennaPod
release_source:
  version:
    url: https://f-droid.org/packages/de.danoeh.antennapod/
    selector: ".package-version-header"
    match: "([0-9.]+)"
  asset_url: https://f-droid.org/repo/de.danoeh.antennapod_{version}.apk
```

Direct APK URL (no scraping):

```yaml
release_source: https://example.com/downloads/app.apk
```

### Local Files

Publish a local APK file.

```yaml
release_source: ./build/outputs/apk/release/app-release.apk
repository: https://github.com/user/app
```

```bash
zsp publish app.apk -r github.com/user/app
```

---

## Metadata Enrichment

zsp can fetch app metadata from external sources to enrich your publication.

### Sources

| Source | Data Retrieved |
|--------|----------------|
| `github` | Name, description, topics, license, website, README |
| `gitlab` | Name, description, topics, license |
| `fdroid` | Name, summary, description, categories, icon, screenshots |
| `playstore` | Name, description, icon, screenshots |

### Priority

When multiple sources are used, metadata is merged with this priority:
1. YAML config (always wins)
2. APK metadata (app label)
3. Play Store
4. Others

### Usage

```bash
# CLI flags (can be repeated)
zsp publish -m github -m playstore

# Or in YAML
metadata_sources:
  - playstore
  - github
```

---

## Configuration Reference

### Minimal Config

```yaml
repository: https://github.com/user/app
```

### Full Config

```yaml
# ═══════════════════════════════════════════════════════════════════
# SOURCE CONFIGURATION
# ═══════════════════════════════════════════════════════════════════

# Source code repository URL or NIP-34 naddr (for display in app store)
repository: https://github.com/user/app

# Where to fetch APKs (if different from repository)
# Can be URL string, local path, or object with version extractor
# Local paths: ./build/app-release.apk, ../builds/*.apk
release_source: https://f-droid.org/packages/com.example.app

# Regex pattern to filter APK assets from releases
# (rarely needed - system auto-selects best arm64-v8a APK)
match: ".*arm64.*\\.apk$"

# ═══════════════════════════════════════════════════════════════════
# APP METADATA
# ═══════════════════════════════════════════════════════════════════

# App name (overrides APK label)
name: My App

# Short one-line description
summary: A wonderful app for doing things

# Full description (supports markdown)
description: |
  My App is a powerful tool that helps you accomplish your goals.
  
  Features:
  - Feature one
  - Feature two

# Category tags
tags:
  - productivity
  - tools
  - nostr

# SPDX license identifier
license: MIT

# App homepage
website: https://myapp.example.com

# ═══════════════════════════════════════════════════════════════════
# MEDIA
# ═══════════════════════════════════════════════════════════════════

# App icon (local path or URL, otherwise extracted from APK)
icon: ./assets/icon.png

# Screenshots (local paths or URLs)
images:
  - ./screenshots/screen1.png
  - https://example.com/screenshot2.png

# ═══════════════════════════════════════════════════════════════════
# RELEASE CONFIGURATION
# ═══════════════════════════════════════════════════════════════════

# Release notes file or URL (extracts section matching version if Keep a Changelog format)
release_notes: ./CHANGELOG.md

# ═══════════════════════════════════════════════════════════════════
# NOSTR-SPECIFIC
# ═══════════════════════════════════════════════════════════════════

# Nostr NIPs supported by this app (for Nostr clients)
supported_nips:
  - "01"
  - "07"
  - "46"

# Minimum version code users should update to
min_allowed_version_code: 100

# ═══════════════════════════════════════════════════════════════════
# VARIANTS
# ═══════════════════════════════════════════════════════════════════

# APK variant patterns (for apps with multiple builds)
variants:
  fdroid: ".*-fdroid-.*\\.apk$"
  google: ".*-google-.*\\.apk$"

# ═══════════════════════════════════════════════════════════════════
# METADATA SOURCES
# ═══════════════════════════════════════════════════════════════════

# External sources for metadata enrichment
# Note: metadata is fetched automatically for new releases (use --skip-metadata to disable)
metadata_sources:
  - playstore
  - fdroid
```

---

## CLI Reference

### Usage Patterns

```bash
zsp publish [config.yaml]           # Config file (default: ./zapstore.yaml)
zsp publish <app.apk> [-r <repo>]   # Local APK with optional source repo
zsp publish -r <repo>               # Fetch latest release from repo
zsp publish --wizard                # Interactive wizard
zsp apk --extract <app.apk>         # Extract APK metadata as JSON
zsp identity --link-key <cert>      # Link signing key to Nostr identity
```

### Flags

| Flag | Description |
|------|-------------|
| `-r <url>` | Source code repository URL (GitHub/GitLab/Codeberg). Also fetches releases from here unless `-s` is specified. |
| `-s <url>` | Release/download source URL (F-Droid, web page, etc). Use alone for closed-source apps. |
| `-m <source>` | Fetch metadata from source (repeatable). Fetched automatically for new releases. |
| `-y` | Auto-confirm all prompts |
| `--offline` | Sign events without uploading/publishing (outputs JSON to stdout) |
| `-h`, `--help` | Show help |
| `-v`, `--version` | Print version |

| Flag | Description |
|------|-------------|
| `--wizard` | Run interactive wizard (recommended for first-time setup) |
| `--match <pattern>` | Regex pattern to filter APK assets (rarely needed - system auto-selects best APK) |
| `--commit <hash>` | Git commit hash for reproducible builds |
| `--channel <name>` | Release channel: main (default), beta, nightly, dev |
| `--check` | Verify config fetches arm64-v8a APK (exit 0=success) |
| `--skip-preview` | Skip the browser preview prompt |
| `--port <port>` | Custom port for browser preview/signing |
| `--overwrite-release` | Bypass cache, re-publish unchanged release |
| `--skip-metadata` | Skip fetching metadata from external sources (useful for frequent releases) |
| `--quiet` | Minimal output, no prompts (implies -y) |
| `--verbose` | Debug output |
| `--no-color` | Disable colored output |

---

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `SIGN_WITH` | Yes | Signing method (see below) |
| `GITHUB_TOKEN` | No | GitHub API token (avoids rate limits) |
| `RELAY_URLS` | No | Comma-separated relay URLs |
| `BLOSSOM_URL` | No | Custom Blossom CDN server |

### Defaults

- **RELAY_URLS**: `wss://relay.zapstore.dev`
- **BLOSSOM_URL**: `https://cdn.zapstore.dev`

---

## Signing Methods

### Private Key (nsec)

Direct signing with a Nostr private key.

```bash
SIGN_WITH=nsec1... zsp publish zapstore.yaml
```

> ⚠️ **Security**: Private keys in environment variables can be exposed via `/proc/*/environ` on Linux or shell history. For production, prefer bunker or browser signing.

### Hex Private Key

64-character hex private key (converted to nsec internally).

```bash
SIGN_WITH=0123456789abcdef... zsp publish zapstore.yaml
```

### Public Key (npub) - Unsigned Output

Output unsigned events for external signing workflows.

```bash
SIGN_WITH=npub1... zsp publish zapstore.yaml > unsigned-events.json
```

### NIP-46 Bunker (Remote Signing)

Sign via a remote signer like nsecBunker.

```bash
SIGN_WITH="bunker://pubkey?relay=wss://relay.example.com&secret=..." zsp publish
```

### Browser Extension (NIP-07)

Sign using your browser's Nostr extension (Alby, nos2x, Flamingo, etc.).

```bash
SIGN_WITH=browser zsp publish
```

This opens a browser window where you approve signing. Supports batch signing for efficiency.

---

## Nostr Events

zsp publishes three NIP-82 compliant event types:

### Kind 32267 - Software Application

App metadata (name, description, icon, screenshots, platforms).

```json
{
  "kind": 32267,
  "tags": [
    ["d", "com.example.app"],
    ["name", "My App"],
    ["summary", "A wonderful app"],
    ["icon", "https://cdn.zapstore.dev/abc123..."],
    ["image", "https://cdn.zapstore.dev/def456..."],
    ["t", "productivity"],
    ["f", "android-arm64-v8a"],
    ["license", "MIT"],
    ["repository", "https://github.com/user/app"]
  ],
  "content": "Full app description..."
}
```

### Kind 30063 - Software Release

Version information and references to assets.

```json
{
  "kind": 30063,
  "tags": [
    ["d", "com.example.app@1.2.3"],
    ["i", "com.example.app"],
    ["version", "1.2.3"],
    ["c", "main"],
    ["e", "<asset-event-id>", "wss://relay.zapstore.dev"]
  ],
  "content": "Release notes..."
}
```

### Kind 3063 - Software Asset

Binary metadata (hash, size, certificate, URLs).

```json
{
  "kind": 3063,
  "tags": [
    ["i", "com.example.app"],
    ["x", "sha256hash..."],
    ["version", "1.2.3"],
    ["version_code", "123"],
    ["url", "https://github.com/.../app.apk"],
    ["m", "application/vnd.android.package-archive"],
    ["size", "12345678"],
    ["f", "android-arm64-v8a"],
    ["apk_certificate_hash", "certsha256..."],
    ["min_platform_version", "21"],
    ["target_platform_version", "34"]
  ]
}
```

---

## APK Selection

When a release contains multiple APKs, zsp uses smart ranking to select the best one:

1. **Architecture filtering**: Removes x86, x86_64, armeabi-v7a (prefers arm64-v8a)
2. **Pattern matching**: Applies `match` regex if configured
3. **ML-based ranking**: Scores APKs by filename patterns (universal, arm64, etc.)
4. **Interactive selection**: In interactive mode, presents ranked options

### Match Patterns

```yaml
# Only mainnet builds
match: ".*-mainnet\\.apk$"

# Only arm64 builds  
match: ".*arm64.*\\.apk$"

# Exclude debug builds
match: "^(?!.*debug).*\\.apk$"
```

---

## CI/CD Integration

### GitHub Actions

```yaml
- name: Publish to Zapstore
  env:
    SIGN_WITH: ${{ secrets.BUNKER_URL_OR_NSEC }}
    GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
  run: |
    zsp publish -y zapstore.yaml
```

### Check Mode

Verify your config fetches a valid APK without publishing:

```bash
zsp publish --check zapstore.yaml
# Exit 0 = success, prints package ID
# Exit 1 = failure
```

### Offline Mode

Sign events without uploading to Blossom or publishing to relays. Events are output to stdout (pipeable to `nak`), and an upload manifest is printed to stderr:

```bash
# Save signed events for later
zsp publish -q --offline zapstore.yaml > events.json

# Pipe directly to nak for publishing (use -q for clean output)
zsp publish -q --offline zapstore.yaml | nak event wss://relay.zapstore.dev

# With npub (outputs unsigned events)
SIGN_WITH=npub1... zsp publish -q --offline zapstore.yaml > unsigned-events.json
```

The manifest (on stderr) shows which files must be uploaded to Blossom before the events become valid:

```
Make sure to upload these files to https://cdn.zapstore.dev before publishing events:

APK:
  Path:   /path/to/app-release.apk
  SHA256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
  URL:    https://cdn.zapstore.dev/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855

Icon:
  Path:   /tmp/zsp_icon_a1b2c3d4e5f67890
  SHA256: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
  URL:    https://cdn.zapstore.dev/a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

For extra security, block all network access:

```bash
# Linux (built-in)
unshare --net --map-root-user zsp publish app.apk --offline > events.json

# Linux (firejail)
firejail --net=none zsp publish app.apk --offline > events.json

# macOS (sandbox-exec, or use LuLu/Little Snitch firewalls)
echo '(version 1)(allow default)(deny network*)' > /tmp/no-net.sb
sandbox-exec -f /tmp/no-net.sb zsp publish app.apk --offline > events.json
```

---

## Advanced Examples

### F-Droid APK with Play Store Metadata

```yaml
repository: https://github.com/AntennaPod/AntennaPod
release_source: https://f-droid.org/packages/de.danoeh.antennapod
metadata_sources:
  - playstore
```

### Multi-variant App (e.g., F-Droid + Google Play builds)

```yaml
repository: https://github.com/niccokunzmann/mundraub-android
match: ".*-fdroid-.*\\.apk$"
variants:
  fdroid: ".*-fdroid-.*\\.apk$"
  google: ".*-google-.*\\.apk$"
```

### Self-hosted GitLab

```yaml
release_source:
  url: https://git.mycompany.com/mobile/app
  type: gitlab
```

### Web Source with JSON API

```yaml
release_source:
  version:
    url: https://api.example.com/releases/latest
    path: "$.tag_name"
    match: "v([0-9.]+)"
  asset_url: https://cdn.example.com/releases/app-v{version}.apk
```

### Reproducible Build with Commit Hash

```bash
zsp publish --commit a1b2c3d4e5f6 zapstore.yaml
```

### NIP-34 Repository Reference

```yaml
repository: naddr1qqxnzd3exsmnjd3exqunjv...
```

### Extract APK Metadata

```bash
zsp apk --extract app.apk
```

Outputs JSON with package ID, version, certificate hash, architectures, permissions, and extracts icon to disk.

---

## License

MIT
