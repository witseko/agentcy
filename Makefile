# Set up agentcy from a clone: the agentcy binary, the terminal UI, and app registration.

BRIDGE := $(CURDIR)/bin/agentcy
# `agentcy ui` looks for the terminal UI beside its own binary, so both live in bin/.
TUI_BINARY := $(CURDIR)/bin/agentcy-tui
# The release version. The VERSION file holds it; `make release VERSION=x.y.z` changes it.
VERSION ?= $(shell cat "$(CURDIR)/VERSION")
# The platforms with an agentcy binary and a terminal UI binary in each GitHub release.
PLATFORMS := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64
# The release programs as <asset name prefix>=<Go package>.
RELEASE_PROGRAMS := agentcy=./cmd/agentcy agentcy-tui=./tui/cmd/agentcy-tui
HOST_PLATFORM := $(shell uname -s | tr '[:upper:]' '[:lower:]')-$(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
REPOSITORY_URL := https://github.com/witseko/agentcy
RELEASE_URL = $(REPOSITORY_URL)/releases/download/v$(VERSION)
# The release workflow builds the release files here. Git ignores this folder.
RELEASE_DIR := dist
# These flags make the build reproducible: the same source, version, and Go release give the
# same bytes on every machine, so anyone can check a release binary against its tagged source.
GO_BUILD_FLAGS = -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.version=$(VERSION)"
# The bridge data directory, as the core finds it: AGENTCY_HOME, else ~/.agentcy.
DATA_DIR = $(or $(AGENTCY_HOME),$(HOME)/.agentcy)
# The project folder whose chats join each other. Pass it as WORKSPACE=/path/to/project.
WORKSPACE ?=
WORKSPACE_PATH = $(abspath $(WORKSPACE))
# The local port of the HTTP bridge and the host name of the tunnel that forwards to it.
HTTP_PORT ?= 8765
PUBLIC_HOST ?=

.DEFAULT_GOAL := help
.PHONY: help setup agentcy tui build stop-core release-files release \
	register-claude register-codex ui http check-workspace check-bridge

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  make %-16s %s\n", $$1, $$2}'

# setup stops a running core last: the core stays alive while any conversation is open, so
# without this an old core would keep serving the new shims after an upgrade.
setup: agentcy tui ## Install agentcy and the terminal UI into bin/, then stop a running core
	@$(MAKE) --no-print-directory stop-core
	@echo "Done. Next: make register-claude WORKSPACE=/path/to/project"

agentcy: ## Install bin/agentcy: built with Go, else downloaded from the release
	@if command -v go >/dev/null; then \
		$(MAKE) --no-print-directory build; \
	else \
		mkdir -p "$(CURDIR)/bin"; \
		echo "Go is missing. Downloading agentcy $(VERSION) for $(HOST_PLATFORM) from $(RELEASE_URL)"; \
		sh scripts/install-release.sh "$(RELEASE_URL)" "agentcy-$(HOST_PLATFORM)" "$(BRIDGE)"; \
	fi

tui: ## Install the terminal UI into bin/: built with Go, else downloaded from the release
	@mkdir -p "$(CURDIR)/bin"
	@if command -v go >/dev/null; then \
		echo "Building the terminal UI $(VERSION) from source"; \
		CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -o "$(TUI_BINARY)" ./tui/cmd/agentcy-tui; \
	else \
		echo "Go is missing. Downloading the terminal UI $(VERSION) for $(HOST_PLATFORM) from $(RELEASE_URL)"; \
		sh scripts/install-release.sh "$(RELEASE_URL)" "agentcy-tui-$(HOST_PLATFORM)" "$(TUI_BINARY)"; \
	fi

build: ## Build bin/agentcy from source with Go
	@command -v go >/dev/null || { echo "Go is missing: https://go.dev/dl/"; exit 1; }
	@echo "Building agentcy $(VERSION) from source"
	CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -o "$(BRIDGE)" ./cmd/agentcy

stop-core: ## Stop the running core of AGENTCY_HOME (default ~/.agentcy), if any
	@sh scripts/stop-core.sh "$(DATA_DIR)"

release-files: ## Build agentcy and the terminal UI for every platform and SHA256SUMS into dist/
	@command -v go >/dev/null || { echo "Go is missing: https://go.dev/dl/"; exit 1; }
	rm -f "$(RELEASE_DIR)"/agentcy-* "$(RELEASE_DIR)/SHA256SUMS"
	@for platform in $(PLATFORMS); do \
		for program in $(RELEASE_PROGRAMS); do \
			asset="$${program%%=*}-$$platform"; \
			echo "Building $(RELEASE_DIR)/$$asset $(VERSION)"; \
			( CGO_ENABLED=0 GOOS=$${platform%-*} GOARCH=$${platform#*-} \
				go build $(GO_BUILD_FLAGS) -o "$(CURDIR)/$(RELEASE_DIR)/$$asset" "$${program#*=}" ) || exit 1; \
		done; \
	done
	cd "$(RELEASE_DIR)" && if command -v sha256sum >/dev/null; then sha256sum agentcy-*; \
		else shasum -a 256 agentcy-*; fi > SHA256SUMS

release: ## Prepare release VERSION=x.y.z: set the version in VERSION
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "Pass VERSION=x.y.z"; exit 1; }
	echo "$(VERSION)" > VERSION
	@echo "Release $(VERSION) is ready. Commit the changes, merge them into main, then run:"
	@echo "  git tag v$(VERSION) && git push origin v$(VERSION)"

check-workspace:
	@test -n "$(WORKSPACE)" || { echo "Pass WORKSPACE=/path/to/project"; exit 1; }
	@test -d "$(WORKSPACE_PATH)" || { echo "Not a directory: $(WORKSPACE_PATH)"; exit 1; }

check-bridge:
	@test -x "$(BRIDGE)" || { echo "$(BRIDGE) is missing. Run make setup first."; exit 1; }

register-claude: check-workspace check-bridge ## Register the server in Claude Code for all projects
	claude mcp add --scope user agentcy -- "$(BRIDGE)" --workspace "$(WORKSPACE_PATH)"

register-codex: check-workspace check-bridge ## Register the server in Codex
	codex mcp add agentcy -- "$(BRIDGE)" --workspace "$(WORKSPACE_PATH)"

ui: check-bridge ## Open the terminal UI for WORKSPACE (default: the current folder)
	"$(BRIDGE)" ui $(if $(WORKSPACE),--workspace "$(WORKSPACE_PATH)")

http: check-workspace check-bridge ## Serve WORKSPACE over HTTP for a remote chat (PUBLIC_HOST=tunnel host, HTTP_PORT=8765)
	"$(BRIDGE)" --workspace "$(WORKSPACE_PATH)" --http "$(HTTP_PORT)" \
		$(if $(PUBLIC_HOST),--http-public-host "$(PUBLIC_HOST)")

.PHONY: bench
bench: build ## Benchmark a core (default: Go in bin/). Set AGENTCY_CORE_CMD, AGENTCY_BIN, BENCH_FLAGS
	AGENTCY_CORE_CMD="$${AGENTCY_CORE_CMD:-$(BRIDGE) core}" \
		AGENTCY_BIN="$${AGENTCY_BIN:-$(BRIDGE)}" go run -tags bench ./bench $(BENCH_FLAGS)

.PHONY: bench-http
bench-http: build ## Run k6 HTTP MCP performance tests (K6_PROFILE=smoke|load, K6_ERA=both|2026-07-28|2025-11-25)
	AGENTCY_BIN="$${AGENTCY_BIN:-$(BRIDGE)}" \
		K6_PROFILE="$${K6_PROFILE:-smoke}" \
		K6_ERA="$${K6_ERA:-both}" \
		K6_SUMMARY_EXPORT="$${K6_SUMMARY_EXPORT:-}" \
		sh bench/k6/run.sh $(K6_FLAGS)

# The daemon that the conformance suite starts. Pass AGENTCY_CORE_CMD to test another one.
AGENTCY_CORE_CMD ?= $(BRIDGE) core
.PHONY: conformance
conformance: build ## Compare the core daemon (default: Go in bin/) with the recorded conformance goldens
	AGENTCY_CORE_CMD="$(AGENTCY_CORE_CMD)" go test -tags conformance ./conformance/...
