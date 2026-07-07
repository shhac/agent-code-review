BINARY := agent-code-review
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint dev tidy

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agent-code-review

test:
	go test ./... -count=1

lint:
	golangci-lint run ./...

dev:
	go run ./cmd/agent-code-review $(ARGS)

tidy:
	go mod tidy
