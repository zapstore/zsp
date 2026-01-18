#!/bin/bash
set -e

BINARY_NAME="zsp"
TARGETS=("linux/amd64" "linux/arm64" "darwin/arm64")

if [[ -z "$1" || "$1" == "-h" || "$1" == "--help" ]]; then
    echo "Usage: $0 <version>"
    echo "Example: $0 0.2.0"
    exit 1
fi

VERSION="$1"
[[ ! "$VERSION" =~ ^v ]] && VERSION="v${VERSION}"

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$ ]]; then
    echo "Invalid version format: $VERSION (expected vX.Y.Z)"
    exit 1
fi

if git rev-parse "$VERSION" >/dev/null 2>&1; then
    echo "Tag $VERSION already exists"
    exit 1
fi

echo "==> Running tests..."
go test ./...

echo "==> Building binaries..."
rm -rf dist && mkdir -p dist
LDFLAGS="-s -w -X main.version=${VERSION#v}"

for target in "${TARGETS[@]}"; do
    OS="${target%/*}" ARCH="${target#*/}"
    echo "  ${OS}/${ARCH}"
    GOOS="$OS" GOARCH="$ARCH" go build -ldflags "$LDFLAGS" -o "dist/${BINARY_NAME}-${OS}-${ARCH}" .
done

(cd dist && shasum -a 256 * > checksums.txt)

echo "==> Creating tag ${VERSION}..."
git tag -a "$VERSION" -m "Release ${VERSION}"

echo "==> Pushing to origin..."
git push origin main "$VERSION"

echo ""
echo "Done! Binaries in dist/"
echo "Create release: https://github.com/zapstore/zsp/releases/new?tag=${VERSION}"
