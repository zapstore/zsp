#!/bin/sh
# Usage: tag.sh [vX.Y.Z]
# Tags HEAD when it has no tag, and prints the tag.
# With no argument the next tag follows the commits since the latest one:
# a subject starting with feat bumps the minor, anything else bumps the patch.
# A breaking subject (feat!: or fix!:) bumps the major.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if [ -n "$(git status --porcelain)" ]; then
	echo "working tree is not clean" >&2
	exit 1
fi

next_version() {
	latest=$1
	echo "$latest" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || {
		echo "latest tag $latest is not vMAJOR.MINOR.PATCH" >&2
		exit 1
	}
	ver=${latest#v}
	major=${ver%%.*}
	rest=${ver#*.}
	minor=${rest%%.*}
	patch=${rest#*.}
	subjects=$(git log --format=%s "$latest"..HEAD)
	if printf '%s\n' "$subjects" | grep -Eq '^[a-z]+(\([^)]*\))?!:'; then
		major=$((major + 1))
		minor=0
		patch=0
	elif printf '%s\n' "$subjects" | grep -Eq '^feat(\([^)]*\))?:'; then
		minor=$((minor + 1))
		patch=0
	else
		patch=$((patch + 1))
	fi
	echo "v$major.$minor.$patch"
}

want=${1-}
current=$(git describe --tags --exact-match HEAD 2>/dev/null || true)
if [ -n "$current" ]; then
	if [ -n "$want" ] && [ "$want" != "$current" ]; then
		echo "HEAD is $current, not $want" >&2
		exit 1
	fi
	echo "$current"
	exit 0
fi

if [ -n "$want" ]; then
	next=$want
	echo "$next" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || {
		echo "tag $next is not vMAJOR.MINOR.PATCH" >&2
		exit 1
	}
else
	next=$(next_version "$(git describe --tags --abbrev=0)") || exit 1
fi

if git rev-parse -q --verify "refs/tags/$next" >/dev/null; then
	echo "tag $next already exists" >&2
	exit 1
fi
git tag "$next"
echo "$next"
