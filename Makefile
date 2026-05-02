BINARY  := esops
PKG     := github.com/esops-dev/esops-go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
  -X $(PKG)/internal/version.Version=$(VERSION) \
  -X $(PKG)/internal/version.Commit=$(COMMIT) \
  -X $(PKG)/internal/version.Date=$(DATE)

GO      ?= go
BIN_DIR := bin
GOBIN   := $(shell $(GO) env GOPATH)/bin

.DEFAULT_GOAL := help

.PHONY: help build test test-integration lint security update tidy clean release-snapshot certs certs-clean dev-up dev-down dev-logs dev-seed dev-seed-unassigned dev-seed-snapshots

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the esops binary into ./bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) ./cmd/esops

test: ## Run unit tests with the race detector
	$(GO) test -race -count=1 ./...

test-integration: ## Run integration tests (build tag: integration)
	$(GO) test -race -count=1 -tags=integration ./...

lint: ## Run golangci-lint
	@command -v golangci-lint >/dev/null || { echo "install: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run

security: ## Run gosec and govulncheck
	@command -v gosec >/dev/null || $(GO) install github.com/securego/gosec/v2/cmd/gosec@latest
	@command -v govulncheck >/dev/null || $(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	PATH="$(GOBIN):$$PATH" gosec ./...
	PATH="$(GOBIN):$$PATH" govulncheck ./...

update: ## Update module dependencies and tidy
	$(GO) get -u ./...
	$(GO) mod tidy

tidy: ## go mod tidy
	$(GO) mod tidy

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) dist

release-snapshot: ## Build a local goreleaser snapshot
	@command -v goreleaser >/dev/null || { echo "install: https://goreleaser.com/install/"; exit 1; }
	goreleaser release --snapshot --clean

certs: ## Generate throw-away dev certs for the mTLS cluster (idempotent)
	./scripts/certs/generate.sh

certs-clean: ## Remove generated dev certs
	rm -rf scripts/certs/out

dev-up: certs ## Start local ES (:9200/:9201) + OS (:9202) + ES-mTLS (:9203) clusters
	docker compose up -d --wait es01 es02 os01 es-mtls
	docker compose up -d es-mtls-setup
	docker compose wait es-mtls-setup

dev-down: ## Stop local dev clusters and remove their ephemeral state
	docker compose down -v

dev-logs: ## Tail logs from local dev clusters
	docker compose logs -f

dev-seed: ## Seed sample data into both local dev clusters
	./scripts/seed-dev.sh --url http://localhost:9200
	./scripts/seed-dev.sh --url http://localhost:9202 --user 'admin:EsopsDev123!'

dev-seed-unassigned: ## Same as dev-seed, plus an over-replicated index so allocation-explain has something to chew on
	./scripts/seed-dev.sh --url http://localhost:9200 --with-unassigned
	./scripts/seed-dev.sh --url http://localhost:9202 --user 'admin:EsopsDev123!' --with-unassigned

dev-seed-snapshots: ## Same as dev-seed, plus a registered fs repo and two snapshots so `esops snapshot list` has data
	./scripts/seed-dev.sh --url http://localhost:9200 --with-snapshots
	./scripts/seed-dev.sh --url http://localhost:9202 --user 'admin:EsopsDev123!' --with-snapshots
