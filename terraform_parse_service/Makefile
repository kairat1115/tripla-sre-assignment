.DEFAULT_GOAL := help

GO ?= go
CONFIG_PATH ?= configs/config.yaml
BINARY ?= bin/terraform-parse-service-server
UNIT_PACKAGES := ./cmd/... ./internal/...
INTEGRATION_PACKAGES := ./test/...

.PHONY: help fmt lint vet test test-unit test-integration build run-server

help: ## Show available targets.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go code.
	gofmt -w $$(find . -name '*.go')

lint: ## Check Go formatting.
	@files="$$(gofmt -l .)" && if [ -n "$$files" ]; then echo "Go files need formatting:"; echo "$$files"; exit 1; fi

vet: ## Run Go vet.
	$(GO) vet ./...

test-unit: ## Run unit/package tests.
	$(GO) test $(UNIT_PACKAGES)

test-integration: ## Run integration tests.
	$(GO) test $(INTEGRATION_PACKAGES)

test: test-unit test-integration ## Run all tests.

build: ## Build the server binary.
	mkdir -p $(dir $(BINARY))
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BINARY) ./cmd/server

run-server: ## Run the server locally.
	CONFIG_PATH=$(CONFIG_PATH) $(GO) run ./cmd/server
