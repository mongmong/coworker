.PHONY: help test test-unit test-integration test-replay test-live lint build clean tidy

BINARY := coworker
MODULE := github.com/chris/coworker

# Inject version from git when available; fall back to dev marker.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
LDFLAGS := -ldflags "-X '$(MODULE)/cli.Version=$(VERSION)'"

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: test-unit ## Run the default (unit) test suite.

test-unit: ## Run all unit + integration Go tests with -race.
	go test -race ./... -count=1 -timeout 180s

test-integration: ## Run integration tests with mock CLIs.
	go test ./tests/integration/... -count=1 -timeout 60s

test-replay: ## Run replay tests (gated by COWORKER_REPLAY=1).
	COWORKER_REPLAY=1 go test ./tests/replay/... -count=1 -timeout 60s

test-live: ## Run live CLI smoke tests (gated by build tag and COWORKER_LIVE=1).
	COWORKER_LIVE=1 go test -tags live ./tests/live/... -count=1 -timeout 300s

lint: ## Run golangci-lint.
	golangci-lint run ./...

build: ## Build the coworker binary with version injected.
	go build $(LDFLAGS) -o $(BINARY) ./cmd/coworker

clean: ## Remove built artifacts and test cache.
	rm -f $(BINARY)
	go clean -testcache

tidy: ## Tidy go.mod / go.sum.
	go mod tidy

.PHONY: golden-update
golden-update: ## Regenerate TUI golden output files.
	UPDATE_GOLDEN=1 go test ./tui/... -run TestGolden -count=1
