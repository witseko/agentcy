"""Launch the bridge for a desktop app or CLI over stdio or HTTP, or the terminal UI."""

import argparse
import os
import sys
from importlib.metadata import version
from pathlib import Path

from agentcy.executables import find_on_path
from agentcy.mcp.transport import core_connection
from agentcy.storage.database import workspace_key

TUI_PROGRAM = "agentcy-tui"
TUI_MISSING = (
    "The terminal UI is not installed. Run 'make setup' in the agentcy clone, "
    "or set AGENTCY_TUI to the absolute path of a built agentcy-tui binary."
)


def main(arguments: list[str] | None = None) -> None:
    """Start the terminal UI for `agentcy ui`, otherwise the shim for the host's settings."""
    arguments = sys.argv[1:] if arguments is None else arguments
    if arguments[:1] == ["ui"]:
        start_ui(arguments[1:])
    else:
        start_shim(arguments)


def start_shim(arguments: list[str]) -> None:
    """Start a shim for the workspace chosen in the host's MCP settings."""
    parser = argparse.ArgumentParser(prog="agentcy", description=__doc__)
    parser.add_argument(
        "--version", action="version", version=f"agentcy {version('agentcy')}"
    )
    parser.add_argument(
        "--workspace", type=Path, required=True, help="directory shared by the apps and CLIs"
    )
    parser.add_argument(
        "--http",
        type=int,
        metavar="PORT",
        help="serve MCP over HTTP on 127.0.0.1:PORT in place of stdio, for a remote chat "
        "behind a tunnel; needs a secret in AGENTCY_HTTP_TOKEN",
    )
    parser.add_argument(
        "--http-public-host",
        metavar="HOST",
        help="host name of the tunnel that forwards to the HTTP port, for example "
        "bridge.example.com",
    )
    options = parser.parse_args(arguments)
    os.umask(0o077)
    directory = options.workspace.resolve(strict=True)
    # The UI path never loads the MCP server.
    from agentcy.mcp.http_transport import read_settings
    from agentcy.mcp.server import serve

    if options.http is None:
        if options.http_public_host is not None:
            parser.error("--http-public-host needs --http")
        serve(workspace_key(directory), directory)
        return
    try:
        http = read_settings(options.http, options.http_public_host)
    except ValueError as error:
        parser.error(str(error))
    serve(workspace_key(directory), directory, http)


def tui_executable() -> str:
    """Find the UI binary from AGENTCY_TUI, the bridge's own bin folder, or PATH.

    `make setup` builds the binary into the virtual environment's bin folder, next to the
    interpreter that runs this command, so the UI works without a PATH change.
    """
    configured = os.environ.get("AGENTCY_TUI")
    if configured:
        path = Path(configured)
        if not path.is_absolute() or not path.is_file() or not os.access(path, os.X_OK):
            raise SystemExit("AGENTCY_TUI must be the absolute path of an executable file")
        return str(path)
    installed = Path(sys.executable).parent / TUI_PROGRAM
    if installed.is_file() and os.access(installed, os.X_OK):
        return str(installed)
    found = find_on_path(TUI_PROGRAM)
    if found is None:
        raise SystemExit(TUI_MISSING)
    return found


def start_ui(arguments: list[str]) -> None:
    """Make sure the core runs, then replace this process with the terminal UI."""
    parser = argparse.ArgumentParser(
        prog="agentcy ui", description="Start and manage conversations from a terminal."
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
    # The binary comes from the user's AGENTCY_TUI or an absolute PATH entry, and receives
    # a fixed argument list with no shell.
    # nosemgrep: dangerous-os-exec-audit
    os.execv(executable, [executable, "--workspace", str(directory)])  # noqa: S606
