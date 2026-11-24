# dist/<name>-<os>-<arch>

NAME := zsp
CMD := ./cmd/zsp

# libwebp is cgo, including under make test.
export CGO_ENABLED = 1
GOFLAGS := -trimpath
LDFLAGS := -s -w

# Stamp main.version only when HEAD is exactly a git tag.
VERSION := $(shell git describe --tags --exact-match HEAD 2>/dev/null)
ifneq ($(VERSION),)
LDFLAGS += -X main.version=$(VERSION)
endif
# v2.0.0 -> 2.0.0. Release asset names use this.
RELVER := $(patsubst v%,%,$(VERSION))

ifneq ($(filter release publish,$(MAKECMDGOALS)),)
ifeq ($(VERSION),)
$(error HEAD is not exactly a git tag; tag this commit, then run make release)
endif
endif

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
HOST_OS := $(shell go env GOOS)
HOST_ARCH := $(shell go env GOARCH)
BIN := dist/$(NAME)-$(GOOS)-$(GOARCH)
LOCALBIN := $(HOME)/.local/bin

comma := ,
ifeq ($(GOOS),darwin)
CGO_LDFLAGS += -Wl,-no_warn_duplicate_libraries
else
# A Darwin parent exports this flag; zig rejects it when linking Linux.
CGO_LDFLAGS := $(filter-out -Wl$(comma)-no_warn_duplicate_libraries,$(CGO_LDFLAGS))
endif
export CGO_LDFLAGS

# Integration tests build from *_DIR unless *_BINARY is set.
RELAY_DIR ?= $(abspath ../relay)
HOTBOX_DIR ?= $(abspath ../hotbox)

.DEFAULT_GOAL := help
.PHONY: help build darwin-arm64 linux-arm64 linux-amd64 release publish clean test test-unit test-integration test-release

help: ## List targets
	@awk 'BEGIN { FS = ":.*## " } /^[a-zA-Z0-9_.-]+:.*## / { printf "  %-20s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build dist/<name>-<os>-<arch>; link it when built for this machine
	mkdir -p dist
	rm -rf $(BIN)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
		-o $(BIN) $(CMD)
ifeq ($(GOOS)-$(GOARCH),$(HOST_OS)-$(HOST_ARCH))
	ln -sfn $(BIN) $(NAME)
	@if [ -d "$(LOCALBIN)" ]; then \
		ln -sfn "$(CURDIR)/$(BIN)" "$(LOCALBIN)/$(NAME)" || exit 1; \
		echo "linked $(LOCALBIN)/$(NAME)"; \
	fi
endif

darwin-arm64: ## macOS arm64 → dist/ only
	$(MAKE) build GOOS=darwin GOARCH=arm64

linux-arm64: ## Linux arm64 → dist/ only
	$(MAKE) build GOOS=linux GOARCH=arm64 \
		CC="zig cc -target aarch64-linux-gnu" \
		CXX="zig c++ -target aarch64-linux-gnu"

linux-amd64: ## Linux amd64 → dist/ only
	$(MAKE) build GOOS=linux GOARCH=amd64 \
		CC="zig cc -target x86_64-linux-gnu" \
		CXX="zig c++ -target x86_64-linux-gnu"

# Three binaries, their tarballs, and the Homebrew formula in dist/. Does not
# create the tag or the GitHub release; `make publish` does the upload.
release: ## Versioned binaries, tarballs, and zsp.rb in dist/ (HEAD must be a tag)
	$(MAKE) darwin-arm64
	$(MAKE) linux-arm64
	$(MAKE) linux-amd64
	cp dist/$(NAME)-darwin-arm64 dist/$(NAME)-$(RELVER)-darwin-arm64
	cp dist/$(NAME)-linux-arm64 dist/$(NAME)-$(RELVER)-linux-arm64
	cp dist/$(NAME)-linux-amd64 dist/$(NAME)-$(RELVER)-linux-amd64
	dist/$(NAME)-$(HOST_OS)-$(HOST_ARCH) --version | grep -qx "$(VERSION)"
	scripts/tarball.sh dist/$(NAME)-darwin-arm64 dist/$(NAME)-$(RELVER)-darwin-arm64.tar.gz
	scripts/tarball.sh dist/$(NAME)-linux-arm64 dist/$(NAME)-$(RELVER)-linux-arm64.tar.gz
	scripts/tarball.sh dist/$(NAME)-linux-amd64 dist/$(NAME)-$(RELVER)-linux-amd64.tar.gz
	scripts/brew-formula.sh $(RELVER) dist > dist/$(NAME).rb
	@echo dist/$(NAME)-$(RELVER)-darwin-arm64
	@echo dist/$(NAME)-$(RELVER)-linux-arm64
	@echo dist/$(NAME)-$(RELVER)-linux-amd64
	@echo dist/$(NAME)-$(RELVER)-darwin-arm64.tar.gz
	@echo dist/$(NAME)-$(RELVER)-linux-arm64.tar.gz
	@echo dist/$(NAME)-$(RELVER)-linux-amd64.tar.gz
	@echo dist/$(NAME).rb

publish: release ## Upload dist/ to the GitHub release and update the Homebrew tap (needs gh)
	scripts/publish.sh $(VERSION) dist

clean: ## Remove dist/ and symlinks
	rm -rf dist
	rm -f $(NAME)
	@l="$(LOCALBIN)/$(NAME)"; \
	if [ -L "$$l" ]; then \
		case "$$(readlink "$$l")" in "$(CURDIR)/dist/"*) rm -f "$$l";; esac; \
	fi

test: ## Unit, integration, release, and provider tests
	$(MAKE) test-unit
	go test -count=1 ./tests
	$(MAKE) test-integration
	$(MAKE) test-release
	go test -tags providers -count=1 -timeout=20m ./tests/integration/providers

test-unit: ## Unit tests only
	go test $$(go list ./... | grep -v '/tests')

test-integration: ## Publish against a local relay built from RELAY_DIR
	RELAY_DIR=$(RELAY_DIR) go test -tags publish -count=1 -timeout=15m ./tests/integration/publish

test-release: ## Release flow with relay and hotbox built from RELAY_DIR and HOTBOX_DIR
	RELAY_DIR=$(RELAY_DIR) HOTBOX_DIR=$(HOTBOX_DIR) go test -tags release -count=1 -timeout=20m ./tests/integration/release
