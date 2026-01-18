#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Project configuration
BINARY_NAME="zsp"
MODULE="github.com/zapstore/zsp"

# Build targets: OS/ARCH pairs
TARGETS=(
    "linux/amd64"
    "linux/arm64"
    "darwin/amd64"
    "darwin/arm64"
)

print_step() {
    echo -e "${BLUE}==>${NC} $1"
}

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

usage() {
    echo "Usage: $0 <version>"
    echo ""
    echo "Create a new release for ${BINARY_NAME}."
    echo ""
    echo "Arguments:"
    echo "  version    Version string (e.g., 0.2.0 or v0.2.0)"
    echo ""
    echo "Options:"
    echo "  -h, --help     Show this help message"
    echo "  -n, --dry-run  Build binaries but don't create git tag"
    echo ""
    echo "Examples:"
    echo "  $0 0.2.0"
    echo "  $0 v0.2.0"
    echo "  $0 --dry-run 0.2.0"
    exit 1
}

# Parse arguments
DRY_RUN=false
VERSION=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            usage
            ;;
        -n|--dry-run)
            DRY_RUN=true
            shift
            ;;
        -*)
            print_error "Unknown option: $1"
            usage
            ;;
        *)
            VERSION="$1"
            shift
            ;;
    esac
done

if [[ -z "$VERSION" ]]; then
    print_error "Version is required"
    usage
fi

# Normalize version (ensure it starts with 'v')
if [[ ! "$VERSION" =~ ^v ]]; then
    VERSION="v${VERSION}"
fi

# Validate version format (vX.Y.Z or vX.Y.Z-suffix)
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$ ]]; then
    print_error "Invalid version format: $VERSION"
    echo "Expected format: vX.Y.Z or vX.Y.Z-suffix (e.g., v0.2.0, v1.0.0-beta.1)"
    exit 1
fi

# Version without 'v' prefix for ldflags
VERSION_NUM="${VERSION#v}"

echo ""
echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║${NC}     ${BINARY_NAME} Release ${VERSION}     ${BLUE}║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
echo ""

# Check for uncommitted changes
print_step "Checking git status..."
if [[ -n $(git status --porcelain) ]]; then
    print_warning "You have uncommitted changes:"
    git status --short
    echo ""
    read -p "Continue anyway? [y/N] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Check if tag already exists
if git rev-parse "$VERSION" >/dev/null 2>&1; then
    print_error "Tag $VERSION already exists"
    exit 1
fi

print_success "Git status OK"

# Run tests
print_step "Running tests..."
if go test -v ./... ; then
    print_success "All tests passed"
else
    print_error "Tests failed"
    exit 1
fi

# Create dist directory
DIST_DIR="dist"
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

# Build for all targets
print_step "Building binaries..."

LDFLAGS="-s -w -X main.version=${VERSION_NUM}"

for target in "${TARGETS[@]}"; do
    OS="${target%/*}"
    ARCH="${target#*/}"
    OUTPUT_NAME="${BINARY_NAME}-${OS}-${ARCH}"
    
    if [[ "$OS" == "windows" ]]; then
        OUTPUT_NAME="${OUTPUT_NAME}.exe"
    fi
    
    echo "  Building ${OS}/${ARCH}..."
    
    GOOS="$OS" GOARCH="$ARCH" go build \
        -ldflags "$LDFLAGS" \
        -o "${DIST_DIR}/${OUTPUT_NAME}" \
        .
    
    # Create checksum
    (cd "$DIST_DIR" && shasum -a 256 "$OUTPUT_NAME" >> checksums.txt)
done

print_success "Built ${#TARGETS[@]} binaries"

# Show built artifacts
echo ""
print_step "Build artifacts:"
ls -lh "$DIST_DIR"
echo ""
echo "Checksums:"
cat "$DIST_DIR/checksums.txt"
echo ""

if [[ "$DRY_RUN" == true ]]; then
    print_warning "Dry run mode - skipping git tag creation"
    echo ""
    echo "Binaries are available in: ${DIST_DIR}/"
    exit 0
fi

# Create and push git tag
print_step "Creating git tag ${VERSION}..."
git tag -a "$VERSION" -m "Release ${VERSION}"
print_success "Created tag ${VERSION}"

print_step "Pushing tag to origin..."
git push origin "$VERSION"
print_success "Pushed tag to origin"

# Final summary
echo ""
echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║${NC}          Release Complete!             ${GREEN}║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════╝${NC}"
echo ""
echo "Tag: ${VERSION}"
echo "Binaries: ${DIST_DIR}/"
echo ""
echo "Next steps:"
echo "  1. Go to: https://github.com/zapstore/zsp/releases/new?tag=${VERSION}"
echo "  2. Set title: ${VERSION}"
echo "  3. Upload the binaries from ${DIST_DIR}/"
echo "  4. Add release notes"
echo "  5. Publish the release"
echo ""
echo "Or use gh CLI (if installed):"
echo "  gh release create ${VERSION} ${DIST_DIR}/* --title '${VERSION}' --generate-notes"
echo ""
