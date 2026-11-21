# Set up agents-chat from a clone: Python dependencies, the terminal UI, and app registration.

BRIDGE := $(CURDIR)/.venv/bin/agents-chat
TUI_BINARY := $(CURDIR)/.venv/bin/agents-chat-tui
# The project folder whose chats join each other. Pass it as WORKSPACE=/path/to/project.
WORKSPACE ?=
WORKSPACE_PATH = $(abspath $(WORKSPACE))

.DEFAULT_GOAL := help
.PHONY: help setup deps tui register-claude register-codex ui check-workspace

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  make %-16s %s\n", $$1, $$2}'

setup: deps tui ## Install the bridge and build the terminal UI
	@echo "Done. Next: make register-claude WORKSPACE=/path/to/project"

deps: ## Install the Python dependencies into .venv
	@command -v uv >/dev/null || { echo "uv is missing: https://docs.astral.sh/uv/"; exit 1; }
	uv sync --locked --no-dev

tui: ## Build the terminal UI into .venv/bin
	@command -v go >/dev/null || { echo "Go is missing: https://go.dev/dl/"; exit 1; }
	@test -d .venv/bin || { echo "Run make deps first."; exit 1; }
	cd tui && go build -o "$(TUI_BINARY)" ./cmd/agents-chat-tui

check-workspace:
	@test -n "$(WORKSPACE)" || { echo "Pass WORKSPACE=/path/to/project"; exit 1; }
	@test -d "$(WORKSPACE_PATH)" || { echo "Not a directory: $(WORKSPACE_PATH)"; exit 1; }

register-claude: check-workspace ## Register the server in Claude Code for all projects
	claude mcp add --scope user agents-chat -- "$(BRIDGE)" --workspace "$(WORKSPACE_PATH)"

register-codex: check-workspace ## Register the server in Codex
	codex mcp add agents-chat -- "$(BRIDGE)" --workspace "$(WORKSPACE_PATH)"

ui: ## Open the terminal UI for WORKSPACE (default: the current folder)
	"$(BRIDGE)" ui $(if $(WORKSPACE),--workspace "$(WORKSPACE_PATH)")
