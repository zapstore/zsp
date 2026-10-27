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

ifneq ($(filter release,$(MAKECMDGOALS)),)
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

.PHONY: all bin darwin-arm64 linux-arm64 linux-amd64 release clean test test-unit test-integration test-release

all: bin
	ln -sfn $(BIN) $(NAME)
	@if [ -d "$(LOCALBIN)" ]; then \
		ln -sfn "$(CURDIR)/$(BIN)" "$(LOCALBIN)/$(NAME)" || exit 1; \
		echo "linked $(LOCALBIN)/$(NAME)"; \
	fi

bin:
	mkdir -p dist
	rm -rf $(BIN)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
		-o $(BIN) $(CMD)

darwin-arm64:
	$(MAKE) bin GOOS=darwin GOARCH=arm64

linux-arm64:
	$(MAKE) bin GOOS=linux GOARCH=arm64 \
		CC="zig cc -target aarch64-linux-gnu" \
		CXX="zig c++ -target aarch64-linux-gnu"

linux-amd64:
	$(MAKE) bin GOOS=linux GOARCH=amd64 \
		CC="zig cc -target x86_64-linux-gnu" \
		CXX="zig c++ -target x86_64-linux-gnu"

# Three binaries in dist/. Does not create the tag or the GitHub release.
release:
	$(MAKE) darwin-arm64
	$(MAKE) linux-arm64
	$(MAKE) linux-amd64
	cp dist/$(NAME)-darwin-arm64 dist/$(NAME)-$(RELVER)-darwin-arm64
	cp dist/$(NAME)-linux-arm64 dist/$(NAME)-$(RELVER)-linux-arm64
	cp dist/$(NAME)-linux-amd64 dist/$(NAME)-$(RELVER)-linux-amd64
	dist/$(NAME)-$(HOST_OS)-$(HOST_ARCH) --version | grep -qx "$(VERSION)"
	@echo dist/$(NAME)-$(RELVER)-darwin-arm64
	@echo dist/$(NAME)-$(RELVER)-linux-arm64
	@echo dist/$(NAME)-$(RELVER)-linux-amd64

clean:
	rm -rf dist
	rm -f $(NAME)
	@l="$(LOCALBIN)/$(NAME)"; \
	if [ -L "$$l" ]; then \
		case "$$(readlink "$$l")" in "$(CURDIR)/dist/"*) rm -f "$$l";; esac; \
	fi

test:
	$(MAKE) test-unit
	go test -count=1 ./tests
	$(MAKE) test-integration
	$(MAKE) test-release
	go test -tags providers -count=1 -timeout=20m ./tests/integration/providers

test-unit:
	go test $$(go list ./... | grep -v '/tests')

test-integration:
	RELAY_DIR=$(RELAY_DIR) go test -tags publish -count=1 -timeout=15m ./tests/integration/publish

test-release:
	RELAY_DIR=$(RELAY_DIR) HOTBOX_DIR=$(HOTBOX_DIR) go test -tags release -count=1 -timeout=20m ./tests/integration/release
