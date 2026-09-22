BINARY_NAME := zsp
CMD_PATH    := ./cmd/zsp
DIST        := dist

GOFLAGS := -trimpath
LDFLAGS := -s -w

HOST_OS   := $(shell go env GOOS)
HOST_ARCH := $(shell go env GOARCH)

.PHONY: all build build-darwin-arm64 build-linux-amd64 build-linux-arm64 clean test install fmt vet release

build:
	CGO_ENABLED=1 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY_NAME) $(CMD_PATH)

all: build-darwin-arm64 build-linux-amd64 build-linux-arm64

build-darwin-arm64:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
		-o $(DIST)/$(BINARY_NAME)-darwin-arm64 $(CMD_PATH)

build-linux-amd64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
		-o $(DIST)/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)

build-linux-arm64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
		go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
		-o $(DIST)/$(BINARY_NAME)-linux-arm64 $(CMD_PATH)

clean:
	rm -f $(BINARY_NAME)
	rm -rf $(DIST)
	go clean

test:
	go test -v ./...

install:
	go install $(CMD_PATH)

fmt:
	go fmt ./...

vet:
	go vet ./...

# make release VERSION=0.2.0
# Tags vX.Y.Z, builds dist/, and pushes the tag and main to origin.
release:
	@if [ -z "$(VERSION)" ]; then \
		echo "Usage: make release VERSION=0.2.0"; \
		exit 1; \
	fi
	@version="$(VERSION)"; \
	case "$$version" in v*) ;; *) version="v$$version" ;; esac; \
	printf '%s\n' "$$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$$' || { \
		echo "Invalid version format: $$version (expected vX.Y.Z)"; \
		exit 1; \
	}; \
	if git rev-parse "$$version" >/dev/null 2>&1; then \
		echo "Tag $$version already exists"; \
		exit 1; \
	fi; \
	plain="$${version#v}"; \
	echo "==> Running tests..."; \
	go test ./...; \
	echo "==> Building binaries..."; \
	rm -rf $(DIST) && mkdir -p $(DIST); \
	rel_ldflags='$(LDFLAGS) -X main.version='"$$plain"; \
	echo "  darwin/arm64"; \
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$$rel_ldflags" -o $(DIST)/$(BINARY_NAME)-$$plain-darwin-arm64 $(CMD_PATH); \
	echo "  linux/amd64"; \
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$$rel_ldflags" -o $(DIST)/$(BINARY_NAME)-$$plain-linux-amd64 $(CMD_PATH); \
	echo "  linux/arm64"; \
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$$rel_ldflags" -o $(DIST)/$(BINARY_NAME)-$$plain-linux-arm64 $(CMD_PATH); \
	(cd $(DIST) && shasum -a 256 * > checksums.txt); \
	echo "==> Creating tag $$version..."; \
	git tag -a "$$version" -m "Release $$version"; \
	echo "==> Pushing to origin..."; \
	git push origin main "$$version"; \
	echo ""; \
	echo "Done! Binaries in $(DIST)/"; \
	echo "Create release: https://github.com/zapstore/zsp/releases/new?tag=$$version"
