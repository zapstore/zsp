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
# Set your signing key
export SIGN_WITH=nsec1...

# Publish from GitHub
zsp -r github.com/user/app

# Publish from GitHub pulling metadata from Play Store
zsp -r github.com/user/app -m playstore

# Publish local APK
zsp app.apk -r github.com/user/app

# Need more options? The interactive wizard helps you create a config file
zsp

# Publish from config file
zsp zapstore.yaml
```

At any point, run the (actually helpful) help command:

```bash
zsp --help
```

---

## APK Sources

zsp supports multiple sources for fetching APKs. The source type is auto-detected from URLs.

### GitHub Releases

Fetches APKs from GitHub release assets. Automatically selects the best arm64-v8a APK.

```yaml
repository: https://github.com/AeonBTC/mempal
```

```bash
zsp -r github.com/AeonBTC/mempal
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

### Web Scraping

Fetch APKs from any web page using regex patterns.

```yaml
repository: https://github.com/AntennaPod/AntennaPod
release_source:
  url: https://f-droid.org/packages/de.danoeh.antennapod/
  asset_url: https://f-droid\.org/repo/de\.danoeh\.antennapod_[0-9]+\.apk
```

Direct APK URL (no scraping):

```yaml
release_source: https://example.com/downloads/app.apk
```

### Local Files

Publish a local APK file.

```yaml
local: ./build/outputs/apk/release/app-release.apk
repository: https://github.com/user/app
```

```bash
zsp app.apk -r github.com/user/app
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
zsp -m github -m playstore

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
# Can be URL string or object with type/asset_url for web scraping
release_source: https://f-droid.org/packages/com.example.app

# Local APK path (takes priority over remote sources)
local: ./build/app-release.apk

# Regex pattern to filter APK assets from releases
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

# Release channel: main (default), beta, nightly, dev
release_channel: main

# Git commit hash for reproducible builds
commit: abc123def456

# ═══════════════════════════════════════════════════════════════════
# NOSTR-SPECIFIC
# ═══════════════════════════════════════════════════════════════════

# Nostr NIPs supported by this app (for Nostr clients)
supported_nips:
  - "01"
  - "07"
  - "46"

# Minimum version users should update to
min_allowed_version: "1.0.0"
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
metadata_sources:
  - playstore
  - fdroid
```

---

## CLI Reference

### Usage Patterns

```bash
zsp [config.yaml]              # Config file (default: ./zapstore.yaml)
zsp <app.apk> [-r <repo>]      # Local APK with optional source repo
zsp -r <repo>                  # Fetch latest release from repo
zsp <app.apk> --extract-apk    # Extract APK metadata as JSON
zsp                            # Interactive wizard
zsp --wizard                   # Wizard with existing config as defaults
```

### Flags

| Flag | Description |
|------|-------------|
| `-r <url>` | Repository URL (GitHub/GitLab/F-Droid/Codeberg) |
| `-s <url>` | Release source URL (defaults to -r) |
| `-m <source>` | Fetch metadata from source (repeatable) |
| `-y` | Auto-confirm all prompts |
| `-n`, `--dry-run` | Parse & build events without publishing |
| `-h`, `--help` | Show help |
| `-v`, `--version` | Print version |

| Flag | Description |
|------|-------------|
| `--wizard` | Run interactive wizard |
| `--match <pattern>` | Regex pattern to filter APK assets |
| `--commit <hash>` | Git commit hash for reproducible builds |
| `--extract-apk` | Extract APK metadata as JSON |
| `--check-apk` | Verify config fetches arm64-v8a APK (exit 0=success) |
| `--skip-preview` | Skip the browser preview prompt |
| `--port <port>` | Custom port for browser preview/signing |
| `--overwrite-release` | Bypass cache, re-publish unchanged release |
| `--overwrite-app` | Re-fetch metadata for existing app |
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
SIGN_WITH=nsec1... zsp zapstore.yaml
```

> ⚠️ **Security**: Private keys in environment variables can be exposed via `/proc/*/environ` on Linux or shell history. For production, prefer bunker or browser signing.

### Hex Private Key

64-character hex private key (converted to nsec internally).

```bash
SIGN_WITH=0123456789abcdef... zsp zapstore.yaml
```

### Public Key (npub) - Unsigned Output

Output unsigned events for external signing workflows.

```bash
SIGN_WITH=npub1... zsp zapstore.yaml > unsigned-events.json
```

### NIP-46 Bunker (Remote Signing)

Sign via a remote signer like nsecBunker.

```bash
SIGN_WITH="bunker://pubkey?relay=wss://relay.example.com&secret=..." zsp
```

### Browser Extension (NIP-07)

Sign using your browser's Nostr extension (Alby, nos2x, Flamingo, etc.).

```bash
SIGN_WITH=browser zsp
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
    SIGN_WITH: ${{ secrets.NOSTR_NSEC }}
    GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
  run: |
    zsp -y zapstore.yaml
```

### Check Mode

Verify your config fetches a valid APK without publishing:

```bash
zsp --check-apk zapstore.yaml
# Exit 0 = success, prints package ID
# Exit 1 = failure
```

### Dry Run

Build events without publishing:

```bash
zsp --dry-run zapstore.yaml
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

### Web Page with Dynamic APK URL

```yaml
release_source:
  url: https://example.com/downloads
  asset_url: https://cdn\.example\.com/releases/app-v[0-9.]+\.apk
```

### Reproducible Build with Commit Hash

```yaml
repository: https://github.com/AeonBTC/mempal
commit: a1b2c3d4e5f6
```

Or via CLI:

```bash
zsp --commit a1b2c3d4e5f6 zapstore.yaml
```

### NIP-34 Repository Reference

```yaml
repository: naddr1qqxnzd3exsmnjd3exqunjv...
```

### Extract APK Metadata

```bash
zsp --extract-apk app.apk
```

Outputs JSON with package ID, version, certificate hash, architectures, permissions, and extracts icon to disk.

---

## Cryptographic Identity (NIP-C1)

Link your code-signing key (e.g., your Android app signing key) to your Nostr identity. This allows users to verify that the same key that signs your APKs also controls your Nostr pubkey.

### Publishing an Identity Proof

```bash
# Using PKCS12 keystore (e.g., from Android Studio)
zsp --link-identity signing.p12

# Using PEM certificate (will prompt for private key path)
zsp --link-identity cert.pem

# Custom validity period (default: 1 year)
zsp --link-identity signing.p12 --identity-expiry 2y

# Dry run (preview the kind 30509 event without publishing)
zsp --link-identity signing.p12 --dry-run
```

The command:
1. Prompts for password (PKCS12) or private key path (PEM)
2. Extracts the public key and computes its SPKIFP (Subject Public Key Info Fingerprint)
3. Signs a timestamped verification message with your signing key
4. Creates a kind 30509 identity proof event
5. Signs the event with your Nostr key (SIGN_WITH)
6. Publishes to relays

### Verifying an Identity Proof

```bash
# Verify your identity proof against a certificate
zsp --verify-identity signing.p12
```

This fetches the kind 30509 event from relays and verifies:
- SPKIFP matches the certificate
- Signature is valid
- Proof is not expired or revoked

### Identity Expiry

Identity proofs include an expiry timestamp (default: 1 year). The expiry is:
- Embedded in the signed message (cryptographically bound)
- Published in the `expiry` tag

Clients should show warnings for expired proofs. To renew, simply run `--link-identity` again.

### Java KeyStore (JKS) Files

If you have a JKS file, convert it to PKCS12 first:

```bash
keytool -importkeystore \
  -srckeystore your-keystore.jks \
  -destkeystore your-keystore.p12 \
  -deststoretype PKCS12
```

### Getting Your Android Signing Key

For apps signed with Android Studio or Gradle:

```bash
# Find certificate fingerprint
keytool -list -keystore ~/.android/debug.keystore

# Convert to PKCS12
keytool -importkeystore \
  -srckeystore ~/.android/debug.keystore \
  -destkeystore debug.p12 \
  -deststoretype PKCS12
```

### NIP-C1 Event Format (kind 30509)

The identity proof is published as a parameterized replaceable event:

```json
{
  "kind": 30509,
  "pubkey": "<nostr-pubkey-hex>",
  "tags": [
    ["d", "<spkifp>"],
    ["signature", "<base64-signature>"],
    ["expiry", "<unix-timestamp>"]
  ],
  "content": ""
}
```

The SPKIFP (Subject Public Key Info Fingerprint) is the SHA-256 hash of the DER-encoded public key. This allows the same identity to work across certificate re-issuance.

The signed message format:

```
Verifying until <expiry> that I control the following Nostr public key: <hex-pubkey>
```

### Multiple Identities

You can publish multiple identity proofs (e.g., for different signing keys or during key rotation). Each proof is a separate kind 30509 event with a different `d` tag (SPKIFP). Clients should accept any valid, non-expired, non-revoked proof.

### Revocation

To revoke an identity proof, publish a new event with the same `d` tag and add a `revoked` tag:

```json
["revoked", "key-compromised"]
```

Standard reasons: `key-compromised`, `key-retired`, `superseded`.

---

## License

MIT
