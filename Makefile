# Deploy runs: make release REF=<ref>
# Writes dist/<name>-<ref>-<arch>. Infra installs that file as releases/<id>-<ref>.

NAME := zsp
REF ?=
GOARCH ?= $(shell go env GOARCH)
DIST := dist/$(NAME)-$(or $(REF),dev)-$(GOARCH)

# The WebP encoder compiles the bundled libwebp, so every build and test needs cgo.
export CGO_ENABLED = 1

.PHONY: release clean test test-unit test-integration test-release

release:
	mkdir -p dist
	rm -rf $(DIST)
	go build -trimpath \
		-ldflags '-s -w $(if $(REF),-X main.version=$(REF))' \
		-o $(DIST) ./cmd/zsp

clean:
	rm -rf dist

test:
	$(MAKE) test-unit
	go test -count=1 ./tests
	$(MAKE) test-integration
	$(MAKE) test-release
	go test -tags providers -count=1 -timeout=20m ./tests/integration/providers

test-unit:
	go test $$(go list ./... | grep -v '/tests')

test-integration:
	go test -tags publish -count=1 -timeout=15m ./tests/integration/publish

test-release:
	go test -tags release -count=1 -timeout=20m ./tests/integration/release
