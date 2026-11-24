#!/bin/sh
# Usage: brew-formula.sh <version> <dist-dir>
# Prints Formula/zsp.rb for the tarballs in <dist-dir>. <version> has no leading v.
set -eu

version=$1
dist=$2
base="https://github.com/zapstore/zsp/releases/download/v$version"

sha() {
	shasum -a 256 "$dist/zsp-$version-$1.tar.gz" | cut -d' ' -f1
}

cat <<RUBY
class Zsp < Formula
  desc "Publish Android apps to Nostr relays for Zapstore"
  homepage "https://zapstore.dev"
  version "$version"
  license "MIT"

  on_macos do
    depends_on arch: :arm64

    url "$base/zsp-$version-darwin-arm64.tar.gz"
    sha256 "$(sha darwin-arm64)"
  end

  on_linux do
    on_arm do
      url "$base/zsp-$version-linux-arm64.tar.gz"
      sha256 "$(sha linux-arm64)"
    end
    on_intel do
      url "$base/zsp-$version-linux-amd64.tar.gz"
      sha256 "$(sha linux-amd64)"
    end
  end

  def install
    bin.install "zsp"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/zsp --version")
  end
end
RUBY
