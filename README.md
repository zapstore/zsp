# zsp

A fast CLI tool for publishing Android apps to Nostr relays. Used by [Zapstore](https://zapstore.dev).

## Features

- **APK acquisition** from GitHub, GitLab, Codeberg, F-Droid, web pages, or local files
- **APK parsing** to extract package info, version, certificate fingerprint, and icon
- **C1 proof** that the Android signing certificate belongs to your Nostr key
- **Metadata enrichment** from Fastlane, GitHub, GitLab, F-Droid, or Google Play Store
- **Blossom uploads** for icons, screenshots, and a local APK that has no public URL
- **Nostr event signing** with an `nsec` or a NIP-46 bunker
- **Relay publishing** of the application, release, asset, and C1 proof

## Installation

### From Source

```bash
go install github.com/zapstore/zsp/v2/cmd/zsp@latest
```

### Pre-built Binaries

Download from [releases](https://github.com/zapstore/zsp/releases).

## Quick Start

Run the wizard yourself:

```bash
zsp
```

The wizard is how you verify C1: it proves your Android signing certificate belongs to your Nostr key. Do this by hand. Do not give an agent your keystore, password, private key, `nsec`, or `.env`.

For everything else, print the skill and pass it to your agent:

```bash
zsp skill
```

The skill is the documentation. It tells the agent how to write `zapstore.yaml` and publish.

## License

MIT
