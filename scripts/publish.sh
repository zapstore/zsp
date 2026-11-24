#!/bin/sh
# Usage: publish.sh <version-tag> <dist-dir>
# Uploads the release assets to the GitHub release for <version-tag>, creating
# it when missing, then commits Formula/zsp.rb to the Homebrew tap.
# Needs `gh` signed in with push access to zapstore/zsp and zapstore/homebrew-tap.
set -eu

tag=$1
dist=$2
ver=${tag#v}
tap=zapstore/homebrew-tap

files=""
for t in darwin-arm64 linux-arm64 linux-amd64; do
	files="$files $dist/zsp-$ver-$t $dist/zsp-$ver-$t.tar.gz"
done
for f in $files; do
	[ -f "$f" ] || { echo "missing $f; run make release" >&2; exit 1; }
done

gh auth status >/dev/null

if gh release view "$tag" --repo zapstore/zsp >/dev/null 2>&1; then
	# shellcheck disable=SC2086
	gh release upload "$tag" --repo zapstore/zsp --clobber $files
else
	# shellcheck disable=SC2086
	gh release create "$tag" --repo zapstore/zsp --title "$tag" --generate-notes $files
fi
echo "release: $(gh release view "$tag" --repo zapstore/zsp --json url -q .url)"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
gh repo clone "$tap" "$work/tap" -- --quiet
mkdir -p "$work/tap/Formula"
cp "$dist/zsp.rb" "$work/tap/Formula/zsp.rb"
cd "$work/tap"
if [ -z "$(git status --porcelain)" ]; then
	echo "tap already at $ver"
	exit 0
fi
git add Formula/zsp.rb
git commit --quiet -m "zsp $ver"
git push --quiet origin HEAD
echo "tap: https://github.com/$tap (zsp $ver)"
