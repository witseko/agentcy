# Set up agentcy from a clone: Python dependencies, the terminal UI, and app registration.

BRIDGE := $(CURDIR)/.venv/bin/agentcy
TUI_BINARY := $(CURDIR)/.venv/bin/agentcy-tui
# The release version. pyproject.toml holds it; `make release VERSION=x.y.z` changes it.
VERSION ?= $(shell sed -n 's/^version = "\(.*\)"/\1/p' pyproject.toml)
# The platforms with a terminal UI binary in each GitHub release.
PLATFORMS := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64
HOST_PLATFORM := $(shell uname -s | tr '[:upper:]' '[:lower:]')-$(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
REPOSITORY_URL := https://github.com/witseko/agentcy
RELEASE_URL = $(REPOSITORY_URL)/releases/download/v$(VERSION)
# The release workflow builds the release files here. Git ignores this folder.
RELEASE_DIR := dist
# These flags make the build reproducible: the same source, version, and Go release give the
# same bytes on every machine, so anyone can check a release binary against its tagged source.
GO_BUILD_FLAGS = -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.version=$(VERSION)"
# The project folder whose chats join each other. Pass it as WORKSPACE=/path/to/project.
WORKSPACE ?=
WORKSPACE_PATH = $(abspath $(WORKSPACE))
# The local port of the HTTP bridge and the host name of the tunnel that forwards to it.
HTTP_PORT ?= 8765
PUBLIC_HOST ?=

.DEFAULT_GOAL := help
.PHONY: help setup deps tui release-files release register-claude register-codex ui http check-workspace

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  make %-16s %s\n", $$1, $$2}'

setup: deps tui ## Install the bridge and the terminal UI
	@echo "Done. Next: make register-claude WORKSPACE=/path/to/project"

deps: ## Install the Python dependencies into .venv
	@command -v uv >/dev/null || { echo "uv is missing: https://docs.astral.sh/uv/"; exit 1; }
	uv sync --locked --no-dev

tui: ## Install the terminal UI into .venv/bin: built with Go, else downloaded from the release
	@test -d .venv/bin || { echo "Run make deps first."; exit 1; }
	@if command -v go >/dev/null; then \
		echo "Building the terminal UI $(VERSION) from source"; \
		cd tui && go build $(GO_BUILD_FLAGS) -o "$(TUI_BINARY)" ./cmd/agentcy-tui; \
	else \
		echo "Go is missing. Downloading the terminal UI $(VERSION) for $(HOST_PLATFORM) from the GitHub release"; \
		sh scripts/install-tui-release.sh "$(RELEASE_URL)" "$(HOST_PLATFORM)" "$(TUI_BINARY)"; \
	fi

release-files: ## Build the terminal UI for every platform and SHA256SUMS into dist/
	@command -v go >/dev/null || { echo "Go is missing: https://go.dev/dl/"; exit 1; }
	rm -f "$(RELEASE_DIR)"/agentcy-tui-* "$(RELEASE_DIR)/SHA256SUMS"
	@for platform in $(PLATFORMS); do \
		echo "Building $(RELEASE_DIR)/agentcy-tui-$$platform $(VERSION)"; \
		( cd tui && CGO_ENABLED=0 GOOS=$${platform%-*} GOARCH=$${platform#*-} \
			go build $(GO_BUILD_FLAGS) -o "$(CURDIR)/$(RELEASE_DIR)/agentcy-tui-$$platform" ./cmd/agentcy-tui ) || exit 1; \
	done
	cd "$(RELEASE_DIR)" && if command -v sha256sum >/dev/null; then sha256sum agentcy-tui-*; \
		else shasum -a 256 agentcy-tui-*; fi > SHA256SUMS

release: ## Prepare release VERSION=x.y.z: set the version in pyproject.toml and uv.lock
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "Pass VERSION=x.y.z"; exit 1; }
	sed 's/^version = ".*"/version = "$(VERSION)"/' pyproject.toml > pyproject.toml.new
	mv pyproject.toml.new pyproject.toml
	uv lock
	@echo "Release $(VERSION) is ready. Commit the changes, merge them into main, then run:"
	@echo "  git tag v$(VERSION) && git push origin v$(VERSION)"

check-workspace:
	@test -n "$(WORKSPACE)" || { echo "Pass WORKSPACE=/path/to/project"; exit 1; }
	@test -d "$(WORKSPACE_PATH)" || { echo "Not a directory: $(WORKSPACE_PATH)"; exit 1; }

register-claude: check-workspace ## Register the server in Claude Code for all projects
	claude mcp add --scope user agentcy -- "$(BRIDGE)" --workspace "$(WORKSPACE_PATH)"

register-codex: check-workspace ## Register the server in Codex
	codex mcp add agentcy -- "$(BRIDGE)" --workspace "$(WORKSPACE_PATH)"

ui: ## Open the terminal UI for WORKSPACE (default: the current folder)
	"$(BRIDGE)" ui $(if $(WORKSPACE),--workspace "$(WORKSPACE_PATH)")

http: check-workspace ## Serve WORKSPACE over HTTP for a remote chat (PUBLIC_HOST=tunnel host, HTTP_PORT=8765)
	"$(BRIDGE)" --workspace "$(WORKSPACE_PATH)" --http "$(HTTP_PORT)" \
		$(if $(PUBLIC_HOST),--http-public-host "$(PUBLIC_HOST)")
