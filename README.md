# zsp

A fast CLI tool for publishing Android apps to Nostr relays. Used by [Zapstore](https://zapstore.dev).

## Features

- **Smart APK detection** - Automatically selects the best APK from GitHub/GitLab releases
- **Multiple sources** - GitHub, GitLab, F-Droid, web scraping, or local files
- **Certificate verification** - Extracts and publishes APK signing certificate SHA-256
- **Metadata enrichment** - Fetch app metadata from GitHub or F-Droid
- **Nostr publishing** - Signs and publishes to Nostr relays
- **Blossom uploads** - Automatic file hosting via Blossom CDN

## Installation

```bash
go install github.com/zapstore/zsp@latest
```

Or download pre-built binaries from [releases](https://github.com/zapstore/zsp/releases).

## Quick Start

### From GitHub Repository

```bash
# Set your signing key
export SIGN_WITH=nsec1...

# Publish from a GitHub repo
zsp -r "https://github.com/user/app"

# With metadata enrichment from GitHub
zsp -r "https://github.com/user/app" -m github
```

### With Config File

Create `zapstore.yaml`:

```yaml
repository: https://github.com/user/app
name: My App
description: A wonderful app
tags: [productivity, tools]
```

Then run:

```bash
zsp zapstore.yaml
```

### Interactive Wizard

Just run `zsp` with no arguments to launch the interactive wizard:

```bash
zsp
```

## Configuration

See [SPEC.md](SPEC.md) for full configuration reference.

### Minimal Config

```yaml
repository: https://github.com/user/app
```

### F-Droid Source

```yaml
repository: https://github.com/AntennaPod/AntennaPod
release_repository: https://f-droid.org/packages/de.danoeh.antennapod
```

### Web Scraping Source

```yaml
repository: https://github.com/user/app
release_repository:
  url: https://example.com/releases
  asset_url: https://example.com/app_$version.apk
  html:
    selector: ".version-badge"
    attribute: text
```

### Full Config

```yaml
repository: https://github.com/user/app
name: My App
description: A wonderful app for doing things
tags: [productivity, tools]
license: MIT
website: https://myapp.example.com
icon: ./icon.png
images:
  - https://example.com/screenshot1.png
changelog: ./CHANGELOG.md  # Optional, uses remote release notes if not set
```

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `SIGN_WITH` | Yes | `nsec1...`, `npub1...`, `bunker://...`, or `browser` |
| `GITHUB_TOKEN` | No | GitHub API token (avoids rate limits) |
| `RELAYS` | No | Custom relay URLs (comma-separated) |
| `BLOSSOM` | No | Custom Blossom server |
| `FDROID_DATA_PATH` | No | Path to fdroiddata clone |

## CLI Flags

```
Usage: zsp [options] [config.yaml]

Options:
  -r <url>        Repository URL (quick mode)
  -m <source>     Fetch metadata from source (repeatable: -m github -m fdroid)
  -y              Skip confirmations
  -h, --help      Show help with examples
  -v, --version   Print version

  --fetch-metadata <source>   Same as -m
  --extract       Extract APK metadata as JSON (local APK only)
  --dry-run       Output events without publishing
  --quiet         Minimal output, no prompts
  --verbose       Debug output
  --no-color      Disable colors
```

### Metadata Sources (`-m`)

Enrich app metadata from external sources:

```bash
# Fetch from GitHub (description, topics, license, website)
zsp zapstore.yaml -m github

# Fetch from F-Droid (name, summary, description, categories)
zsp zapstore.yaml -m fdroid

# Combine multiple sources
zsp zapstore.yaml -m github -m fdroid
```

Available sources: `github`, `fdroid`, `playstore` (not yet implemented)

## Signing Methods

### Private Key (direct signing)

```bash
SIGN_WITH=nsec1... zsp zapstore.yaml
```

> ⚠️ **Security Note:** Passing private keys via environment variables has risks:
> - May be visible in `/proc/*/environ` on Linux
> - Can appear in shell history if set inline
> - May be logged by process monitoring tools
>
> For production use, prefer **bunker://** or **browser** signing methods.

### Public Key (unsigned output)

```bash
SIGN_WITH=npub1... zsp zapstore.yaml > unsigned-events.json
```

### NIP-46 Bunker (remote signing)

```bash
SIGN_WITH="bunker://..." zsp zapstore.yaml
```

### Browser Extension (NIP-07)

Sign events using your browser's Nostr extension (Alby, nos2x, Flamingo, etc.):

```bash
SIGN_WITH=browser zsp zapstore.yaml
```

This opens a browser window where you can approve signing with your NIP-07 extension.

## License

MIT
