BINARY := agent-code-review
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test test-integration lint dev tidy

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agent-code-review

test:
	go test ./... -count=1

# Drives the real codex CLI (needs codex on PATH + auth; spends quota) and, if
# AGENT_CODE_REVIEW_TEST_REPO is set, live gh discovery against that repo.
test-integration:
	go test ./internal/review/ ./internal/discover/ -count=1 -tags=integration -v -timeout 10m

lint:
	golangci-lint run ./...

dev:
	go run ./cmd/agent-code-review $(ARGS)

tidy:
	go mod tidy
