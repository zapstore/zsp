BINARY_NAME := zsp
CMD_PATH := ./cmd/zsp

.PHONY: all build clean test install fmt vet

all: build

build:
	go build -o $(BINARY_NAME) $(CMD_PATH)

clean:
	rm -f $(BINARY_NAME)
	go clean

test:
	go test -v ./...

install:
	go install $(CMD_PATH)

fmt:
	go fmt ./...

vet:
	go vet ./...

