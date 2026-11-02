SHELL := /bin/bash
.DEFAULT_GOAL := help

GO ?= go
COMPOSE ?= docker compose
SQLC ?= sqlc
ENV_FILE ?= .env
BIN_DIR ?= bin

# Tools pinned to the versions CI uses. Install with `make tools`.
GOOSE_VERSION := v3.29.0
SQLC_VERSION := v1.30.0
GOLANGCI_LINT_VERSION := v2.6.2

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: run
run: ## Start the whole stack and wait for /readyz
	$(COMPOSE) up --build -d
	@echo "waiting for http://localhost:$${HTTP_PORT:-8080}/readyz"
	@for i in $$(seq 1 60); do \
		if curl -fsS "http://localhost:$${HTTP_PORT:-8080}/readyz" > /dev/null 2>&1; then \
			echo "ready: $$(curl -fsS http://localhost:$${HTTP_PORT:-8080}/readyz)"; exit 0; \
		fi; sleep 2; \
	done; \
	echo "stack did not become ready; run 'make logs'"; exit 1

.PHONY: up
up: ## Start the stack in the background
	$(COMPOSE) up --build -d

.PHONY: down
down: ## Stop the stack and keep volumes
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the stack and delete volumes
	$(COMPOSE) down -v

.PHONY: logs
logs: ## Follow service logs
	$(COMPOSE) logs -f --tail=100

.PHONY: build
build: ## Build every binary into ./bin
	@mkdir -p $(BIN_DIR)
	@for cmd in api worker admin eval; do \
		echo "building $$cmd"; \
		$(GO) build -trimpath -o "$(BIN_DIR)/$$cmd" "./cmd/$$cmd" || exit 1; \
	done

.PHONY: test
test: ## Unit tests with the race detector
	$(GO) test -race ./...

.PHONY: test-integration
test-integration: ## Integration tests (needs Docker)
	$(GO) test -race -tags=integration ./...

.PHONY: cover
cover: ## Unit test coverage report
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: vet
vet: ## go vet, including integration-tagged files
	$(GO) vet ./...
	$(GO) vet -tags=integration ./...

.PHONY: lint
lint: ## golangci-lint
	golangci-lint run

.PHONY: vuln
vuln: ## govulncheck
	govulncheck ./...

.PHONY: sqlc
sqlc: ## Regenerate type-safe queries
	$(SQLC) generate

.PHONY: migrate-up
migrate-up: ## Apply migrations against the database in .env
	@set -a && . ./$(ENV_FILE) && set +a && $(GO) run ./cmd/admin migrate up

.PHONY: migrate-down
migrate-down: ## Revert the most recent migration against the database in .env
	@set -a && . ./$(ENV_FILE) && set +a && $(GO) run ./cmd/admin migrate down

.PHONY: migrate-status
migrate-status: ## Show migration state
	@set -a && . ./$(ENV_FILE) && set +a && $(GO) run ./cmd/admin migrate status

.PHONY: storage-init
storage-init: ## Create the object storage bucket
	@set -a && . ./$(ENV_FILE) && set +a && $(GO) run ./cmd/admin storage init

.PHONY: job-noop
job-noop: ## Enqueue a no-op job for the worker
	@set -a && . ./$(ENV_FILE) && set +a && $(GO) run ./cmd/admin job noop

.PHONY: sync-greenbook
sync-greenbook: ## Run a full Greenbook product sync now
	@set -a && . ./$(ENV_FILE) && set +a && $(GO) run ./cmd/admin sync greenbook

.PHONY: sync-leaflets
sync-leaflets: ## Download and extract pending leaflets
	@set -a && . ./$(ENV_FILE) && set +a && $(GO) run ./cmd/admin sync leaflets

.PHONY: registry-report
registry-report: ## Show ingestion counts, leaflet OCR needs, and recent runs
	@set -a && . ./$(ENV_FILE) && set +a && $(GO) run ./cmd/admin registry report

.PHONY: eval
eval: ## Run the evaluation suite (phase 10)
	$(GO) run ./cmd/eval

.PHONY: tools
tools: ## Install the CLI tools CI uses
	$(GO) install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
	$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest
