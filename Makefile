BINARY_NAME := zsp
CMD_PATH := .

.PHONY: all build build-linux-amd64 build-linux-arm64 clean test install fmt vet

all: build

build:
	go build -o $(BINARY_NAME) $(CMD_PATH)

build-linux-amd64:
	GOOS=linux GOARCH=amd64 go build -o $(BINARY_NAME)-linux-amd64 $(CMD_PATH)

build-linux-arm64:
	GOOS=linux GOARCH=arm64 go build -o $(BINARY_NAME)-linux-arm64 $(CMD_PATH)

clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-linux-amd64 $(BINARY_NAME)-linux-arm64
	go clean

test:
	go test -v ./...

install:
	go install $(CMD_PATH)

fmt:
	go fmt ./...

vet:
	go vet ./...

