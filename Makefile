BINARY := agent-code-review
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build dashboard dashboard-dev test test-integration lint dev tidy release release-check

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agent-code-review

dashboard:
	npm --prefix internal/dashboard/ui run build

dashboard-dev:
	npm --prefix internal/dashboard/ui run dev

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

release: release-check
	@echo "release check passed for $(VERSION)"
	@echo "next: git tag $(VERSION) && git push origin main $(VERSION)"

release-check:
	@if [ "$(origin VERSION)" = "file" ]; then echo "VERSION is required, e.g. make release-check VERSION=v0.12.1"; exit 1; fi
	@git check-ref-format "refs/tags/$(VERSION)"
	@! git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null || (echo "tag $(VERSION) already exists"; exit 1)
	@test -z "$$(git status --short)" || (echo "working tree is dirty"; git status --short; exit 1)
	$(MAKE) dashboard
	@git diff --exit-code -- internal/dashboard/assets
	$(MAKE) test
	go vet ./...
	npm --prefix internal/dashboard/ui test
