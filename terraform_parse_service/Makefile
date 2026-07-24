.DEFAULT_GOAL := help

GO ?= go
CONFIG_PATH ?= configs/config.yaml
BINARY ?= bin/terraform-parse-service-server
OTELC_VERSION ?= v1.0.1
OTELC := $(CURDIR)/bin/otelc
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

$(OTELC):
	mkdir -p $(dir $(OTELC))
	GOBIN=$(dir $(OTELC)) $(GO) install go.opentelemetry.io/otelc/tool/cmd/otelc@$(OTELC_VERSION)

build: $(OTELC) ## Build an auto-instrumented server binary.
	mkdir -p $(dir $(BINARY))
	CGO_ENABLED=0 $(OTELC) go build -trimpath -o $(BINARY) ./cmd/server

run-server: build ## Build and run the auto-instrumented server locally.
	CONFIG_PATH=$(CONFIG_PATH) ./$(BINARY)
