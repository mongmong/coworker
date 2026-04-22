.PHONY: help test lint build clean tidy

BINARY := coworker
MODULE := github.com/chris/coworker

# Inject version from git when available; fall back to dev marker.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
LDFLAGS := -ldflags "-X '$(MODULE)/cli.Version=$(VERSION)'"

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: ## Run all Go tests.
	go test ./... -count=1 -timeout 60s

lint: ## Run golangci-lint.
	golangci-lint run ./...

build: ## Build the coworker binary with version injected.
	go build $(LDFLAGS) -o $(BINARY) ./cmd/coworker

clean: ## Remove built artifacts and test cache.
	rm -f $(BINARY)
	go clean -testcache

tidy: ## Tidy go.mod / go.sum.
	go mod tidy
