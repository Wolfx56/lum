MODULE     := github.com/Wolfx56/lum
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS    := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)

LISTEN     ?= :8080
CONFIG_DIR ?= dev/config
DATA_DIR   ?= tmp/data
LOG_LEVEL  ?= debug
AGENT_ID   ?= test-agent
SERVER_URL ?= http://localhost:8080
AGENT_DIR  ?= tmp/agent/$(AGENT_ID)
FAKE_VERSION ?= 4.0.0.700
FAKE_UPGRADE_TO ?=
LINUX_ARCH ?= amd64

PNPM       := pnpm --dir web
# Installed golangci-lint, otherwise a pinned version through go run (no global install).
GOLANGCI   ?= $(shell command -v golangci-lint 2>/dev/null || echo go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0)

.DEFAULT_GOAL := help

.PHONY: help deps run-server run-agent run-web run-fakeapp check-versions dev test test-web lint check-web build-web build build-linux build-agents clean

help: ## List the available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

deps: ## Install the Go and web UI dependencies
	go mod download
	$(PNPM) install

run-server: .env ## Run the dev server (dev/config, database in tmp/data, no embedded UI) on $(LISTEN)
	go run ./cmd/server --config-dir $(CONFIG_DIR) --data-dir $(DATA_DIR) --listen $(LISTEN) --log-level $(LOG_LEVEL)

.env: ## Create .env from .env.example (dummy dev values)
	cp .env.example .env

run-agent: .env ## Run a dev agent (AGENT_ID, SERVER_URL; token and scripts in tmp/agent/AGENT_ID)
	set -a && . ./.env && set +a && LUM_AGENT_SECRET="$$AGENT_BOOTSTRAP_SECRET" go run ./cmd/agent --id $(AGENT_ID) --server $(SERVER_URL) \
		--token-file $(AGENT_DIR)/token --scripts-dir $(AGENT_DIR)/scripts --log-level $(LOG_LEVEL)

run-web: ## Run the Svelte web UI (Vite :5173, proxies /api and /healthz to :8080)
	$(PNPM) dev

run-fakeapp: .env ## Run a fake Sonarr on :8989 (FAKE_VERSION; FAKE_UPGRADE_TO = version installed by an update)
	set -a && . ./.env && set +a && go run ./dev/fakeapp --version $(FAKE_VERSION) --upgrade-to "$(FAKE_UPGRADE_TO)"

check-versions: .env ## Check the versions of CONFIG_DIR once and print the result (dev tool)
	go run ./cmd/lum-check --config-dir $(CONFIG_DIR)

dev: ## Run the server and the web UI in parallel
	$(MAKE) -j2 run-server run-web

test: ## Go tests
	go test ./...

test-web: ## Web UI tests (vitest)
	$(PNPM) test

lint: ## Lint Go (golangci-lint)
	$(GOLANGCI) run

check-web: ## Svelte/TS type checking (svelte-check)
	$(PNPM) check

build-web: ## Production build of the web UI (static SPA) into internal/ui/dist
	$(PNPM) build

build: build-web ## Static binaries bin/lum-server (embedded UI) and bin/lum-agent
	CGO_ENABLED=0 go build -tags embedui -ldflags "$(LDFLAGS)" -o bin/lum-server ./cmd/server
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/lum-agent ./cmd/agent

build-linux: build-web ## Linux binaries for the LXC (LINUX_ARCH=amd64|arm64) in bin/linux-$(LINUX_ARCH)/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -tags embedui -ldflags "$(LDFLAGS)" -o bin/linux-$(LINUX_ARCH)/lum-server ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -ldflags "$(LDFLAGS)" -o bin/linux-$(LINUX_ARCH)/lum-agent ./cmd/agent

build-agents: ## Linux amd64 and arm64 lum-agent binaries in bin/agent/ (copy them to the server's /opt/lum/agent)
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o bin/agent/lum-agent-linux-$$arch ./cmd/agent || exit 1; \
	done

clean: ## Remove build artifacts and dev data (tmp/)
	rm -rf bin internal/ui/dist tmp
