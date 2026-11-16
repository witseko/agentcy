"""Launch the local standard-input/output bridge for a desktop application."""

import argparse
import os
from pathlib import Path

from agents_chat.mcp.server import serve
from agents_chat.storage.database import workspace_key


def main() -> None:
    """Start a shim for the workspace chosen in the desktop application's MCP settings."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--workspace", type=Path, required=True, help="directory shared by the apps"
    )
    options = parser.parse_args()
    os.umask(0o077)
    serve(workspace_key(options.workspace))
