"""Launch the local standard-input/output bridge for a desktop app or CLI, or the terminal UI."""

import argparse
import os
import sys
from pathlib import Path

from agents_chat.executables import find_on_path
from agents_chat.mcp.transport import core_connection
from agents_chat.storage.database import workspace_key

TUI_PROGRAM = "agents-chat-tui"
TUI_MISSING = (
    "The terminal UI is not installed. In the tui/ folder of the agents-chat clone, run "
    "'go install ./cmd/agents-chat-tui' and put $(go env GOPATH)/bin on PATH, "
    "or set AGENTS_CHAT_TUI to the absolute path of a built agents-chat-tui binary."
)


def main(arguments: list[str] | None = None) -> None:
    """Start the terminal UI for `agents-chat ui`, otherwise the shim for the host's settings."""
    arguments = sys.argv[1:] if arguments is None else arguments
    if arguments[:1] == ["ui"]:
        start_ui(arguments[1:])
    else:
        start_shim(arguments)


def start_shim(arguments: list[str]) -> None:
    """Start a shim for the workspace chosen in the host's MCP settings."""
    parser = argparse.ArgumentParser(prog="agents-chat", description=__doc__)
    parser.add_argument(
        "--workspace", type=Path, required=True, help="directory shared by the apps and CLIs"
    )
    options = parser.parse_args(arguments)
    os.umask(0o077)
    directory = options.workspace.resolve(strict=True)
    # The UI path never loads the MCP server.
    from agents_chat.mcp.server import serve

    serve(workspace_key(directory), directory)


def tui_executable() -> str:
    """Find the UI binary from AGENTS_CHAT_TUI or the absolute entries of PATH."""
    configured = os.environ.get("AGENTS_CHAT_TUI")
    if configured:
        path = Path(configured)
        if not path.is_absolute() or not path.is_file() or not os.access(path, os.X_OK):
            raise SystemExit("AGENTS_CHAT_TUI must be the absolute path of an executable file")
        return str(path)
    found = find_on_path(TUI_PROGRAM)
    if found is None:
        raise SystemExit(TUI_MISSING)
    return found


def start_ui(arguments: list[str]) -> None:
    """Make sure the core runs, then replace this process with the terminal UI."""
    parser = argparse.ArgumentParser(
        prog="agents-chat ui", description="Start and manage conversations from a terminal."
    )
    parser.add_argument(
        "--workspace",
        type=Path,
        default=Path.cwd(),
        help="checkout whose conversations to show (default: the current directory)",
    )
    options = parser.parse_args(arguments)
    executable = tui_executable()
    try:
        directory = options.workspace.resolve(strict=True)
    except OSError:
        raise SystemExit(f"Workspace not found: {options.workspace}") from None
    if not directory.is_dir():
        raise SystemExit(f"Workspace is not a directory: {directory}")
    try:
        core_connection().close()
    except (OSError, RuntimeError, ValueError) as error:
        raise SystemExit(f"Cannot start the bridge core: {error}") from None
    # The binary comes from the user's AGENTS_CHAT_TUI or an absolute PATH entry, and receives
    # a fixed argument list with no shell.
    # nosemgrep: dangerous-os-exec-audit
    os.execv(executable, [executable, "--workspace", str(directory)])  # noqa: S606
