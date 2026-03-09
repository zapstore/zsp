BINARY_NAME := zsp
CMD_PATH    := .
DIST        := dist

GOFLAGS := -trimpath
LDFLAGS := -s -w

HOST_OS   := $(shell go env GOOS)
HOST_ARCH := $(shell go env GOARCH)

.PHONY: all build build-darwin-arm64 build-linux-amd64 build-linux-arm64 clean test install fmt vet

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
