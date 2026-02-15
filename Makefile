BINARY_NAME := zsp
CMD_PATH := .
DIST := dist

.PHONY: all build build-linux-amd64 build-linux-arm64 build-darwin-arm64 clean test install fmt vet

# Default: build for current arch only
build:
	go build -o $(BINARY_NAME) $(CMD_PATH)

# Build all 3 arches into dist/
all: $(DIST)/$(BINARY_NAME)-linux-amd64 $(DIST)/$(BINARY_NAME)-linux-arm64 $(DIST)/$(BINARY_NAME)-darwin-arm64

$(DIST):
	mkdir -p $(DIST)

$(DIST)/$(BINARY_NAME)-linux-amd64: $(DIST)
	GOOS=linux GOARCH=amd64 go build -o $@ $(CMD_PATH)

$(DIST)/$(BINARY_NAME)-linux-arm64: $(DIST)
	GOOS=linux GOARCH=arm64 go build -o $@ $(CMD_PATH)

$(DIST)/$(BINARY_NAME)-darwin-arm64: $(DIST)
	GOOS=darwin GOARCH=arm64 go build -o $@ $(CMD_PATH)

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

