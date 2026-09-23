# Deploy runs: make release REF=<ref>
# Writes dist/<name>-<ref>-<arch>. Infra installs that file as releases/<id>-<ref>.

NAME := zsp
REF ?=
GOARCH ?= $(shell go env GOARCH)
DIST := dist/$(NAME)-$(or $(REF),dev)-$(GOARCH)

.PHONY: release clean

release:
	mkdir -p dist
	rm -rf $(DIST)
	CGO_ENABLED=0 go build -trimpath \
		-ldflags '-s -w $(if $(REF),-X main.version=$(REF))' \
		-o $(DIST) ./cmd/zsp

clean:
	rm -rf dist
