#!/bin/sh
# Usage: tarball.sh <binary> <out.tar.gz>
# Packs <binary> as `zsp`, plus LICENSE, into <out.tar.gz>.
set -eu

bin=$1
out=$2
root=$(cd "$(dirname "$0")/.." && pwd)

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

cp "$bin" "$stage/zsp"
chmod 755 "$stage/zsp"
cp "$root/LICENSE" "$stage/LICENSE"

tar -cf - -C "$stage" zsp LICENSE | gzip -9n >"$out"
