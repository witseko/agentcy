"""Launch the local standard-input/output bridge for a desktop app or CLI."""

import argparse
import os
from pathlib import Path

from agents_chat.mcp.server import serve
from agents_chat.storage.database import workspace_key


def main() -> None:
    """Start a shim for the workspace chosen in the host's MCP settings."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--workspace", type=Path, required=True, help="directory shared by the apps and CLIs"
    )
    options = parser.parse_args()
    os.umask(0o077)
    directory = options.workspace.resolve(strict=True)
    serve(workspace_key(directory), directory)
