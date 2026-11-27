# zsp

A CLI that publishes Android apps to Nostr relays for [Zapstore](https://zapstore.dev).

## Features

- **APK acquisition** from GitHub, GitLab, Codeberg, F-Droid, web pages, or local files
- **APK parsing** to extract package info, version, certificate fingerprint, and icon
- **C1 proof** that the Android signing certificate belongs to your Nostr key
- **Metadata enrichment** from Fastlane, GitHub, GitLab, F-Droid, or Google Play Store
- **Blossom uploads** for icons, screenshots, and a local APK that has no public URL
- **Nostr event signing** with an `nsec` or a NIP-46 bunker
- **Relay publishing** of the application, release, asset, and C1 proof

## Install

With Homebrew (macOS arm64, Linux arm64 and x86-64):

```bash
brew install zapstore/tap/zsp
```

Or download a binary or tarball from [releases](https://github.com/zapstore/zsp/releases), or build from source:

```bash
go install github.com/zapstore/zsp/v2/cmd/zsp@latest
```

In a checkout, `make build` builds `dist/zsp-<os>-<arch>` and links it as `./zsp`. `make help` lists every target.

To release, from a clean checkout: `make release`. It tags HEAD (the next minor when a commit since the last tag starts with `feat`, otherwise the next patch), builds the binaries, tarballs, and `dist/zsp.rb`, pushes the commit and the tag, uploads them to the GitHub release, and commits the formula to `zapstore/homebrew-tap`. It needs `gh` signed in.

## Use

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
