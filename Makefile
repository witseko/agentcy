# Set up agents-chat from a clone: Python dependencies, the terminal UI, and app registration.

BRIDGE := $(CURDIR)/.venv/bin/agents-chat
TUI_BINARY := $(CURDIR)/.venv/bin/agents-chat-tui
# The release version. pyproject.toml holds it; `make release VERSION=x.y.z` changes it.
VERSION ?= $(shell sed -n 's/^version = "\(.*\)"/\1/p' pyproject.toml)
# The platforms with a committed terminal UI binary in bin/<os>-<arch>/.
PLATFORMS := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64
HOST_PLATFORM := $(shell uname -s | tr '[:upper:]' '[:lower:]')-$(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
PREBUILT_TUI := bin/$(HOST_PLATFORM)/agents-chat-tui
# These flags make the build reproducible: the same source, version, and Go release give the
# same bytes on every machine, so CI can compare the committed binaries with a fresh build.
GO_BUILD_FLAGS = -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.version=$(VERSION)"
# The project folder whose chats join each other. Pass it as WORKSPACE=/path/to/project.
WORKSPACE ?=
WORKSPACE_PATH = $(abspath $(WORKSPACE))
# The local port of the HTTP bridge and the host name of the tunnel that forwards to it.
HTTP_PORT ?= 8765
PUBLIC_HOST ?=

.DEFAULT_GOAL := help
.PHONY: help setup deps tui tui-binaries release register-claude register-codex ui http check-workspace

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  make %-16s %s\n", $$1, $$2}'

setup: deps tui ## Install the bridge and the terminal UI
	@echo "Done. Next: make register-claude WORKSPACE=/path/to/project"

deps: ## Install the Python dependencies into .venv
	@command -v uv >/dev/null || { echo "uv is missing: https://docs.astral.sh/uv/"; exit 1; }
	uv sync --locked --no-dev

tui: ## Install the terminal UI into .venv/bin: built with Go, else the prebuilt binary
	@test -d .venv/bin || { echo "Run make deps first."; exit 1; }
	@if command -v go >/dev/null; then \
		echo "Building the terminal UI $(VERSION) from source"; \
		cd tui && go build $(GO_BUILD_FLAGS) -o "$(TUI_BINARY)" ./cmd/agents-chat-tui; \
	elif test -f "$(PREBUILT_TUI)"; then \
		echo "Installing the prebuilt terminal UI for $(HOST_PLATFORM)"; \
		install -m 755 "$(PREBUILT_TUI)" "$(TUI_BINARY)"; \
	else \
		echo "No prebuilt terminal UI for $(HOST_PLATFORM). Install Go: https://go.dev/dl/"; exit 1; \
	fi

tui-binaries: ## Rebuild the committed terminal UI binaries in bin/ for every platform
	@command -v go >/dev/null || { echo "Go is missing: https://go.dev/dl/"; exit 1; }
	@for platform in $(PLATFORMS); do \
		echo "Building bin/$$platform/agents-chat-tui $(VERSION)"; \
		( cd tui && CGO_ENABLED=0 GOOS=$${platform%-*} GOARCH=$${platform#*-} \
			go build $(GO_BUILD_FLAGS) -o "$(CURDIR)/bin/$$platform/agents-chat-tui" ./cmd/agents-chat-tui ) || exit 1; \
	done

release: ## Prepare release VERSION=x.y.z: set the version and rebuild the binaries
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "Pass VERSION=x.y.z"; exit 1; }
	sed 's/^version = ".*"/version = "$(VERSION)"/' pyproject.toml > pyproject.toml.new
	mv pyproject.toml.new pyproject.toml
	uv lock
	$(MAKE) tui-binaries VERSION=$(VERSION)
	@echo "Release $(VERSION) is ready. Commit the changes, merge them into main, then run:"
	@echo "  git tag v$(VERSION) && git push origin v$(VERSION)"

check-workspace:
	@test -n "$(WORKSPACE)" || { echo "Pass WORKSPACE=/path/to/project"; exit 1; }
	@test -d "$(WORKSPACE_PATH)" || { echo "Not a directory: $(WORKSPACE_PATH)"; exit 1; }

register-claude: check-workspace ## Register the server in Claude Code for all projects
	claude mcp add --scope user agents-chat -- "$(BRIDGE)" --workspace "$(WORKSPACE_PATH)"

register-codex: check-workspace ## Register the server in Codex
	codex mcp add agents-chat -- "$(BRIDGE)" --workspace "$(WORKSPACE_PATH)"

ui: ## Open the terminal UI for WORKSPACE (default: the current folder)
	"$(BRIDGE)" ui $(if $(WORKSPACE),--workspace "$(WORKSPACE_PATH)")

http: check-workspace ## Serve WORKSPACE over HTTP for a remote chat (PUBLIC_HOST=tunnel host, HTTP_PORT=8765)
	"$(BRIDGE)" --workspace "$(WORKSPACE_PATH)" --http "$(HTTP_PORT)" \
		$(if $(PUBLIC_HOST),--http-public-host "$(PUBLIC_HOST)")
