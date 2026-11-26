"""Shared binary resolution and macOS sandbox helpers for fresh-run provider plugins."""

import json
import os
import stat
import sys
from pathlib import Path

from agentcy.storage.database import data_dir

BIN_VARIABLE = "AGENTCY_BIN"


def agentcy_binary() -> str | None:
    """Return the agentcy executable that the core names in AGENTCY_BIN, or None if unset.

    Raises:
        ValueError: If AGENTCY_BIN is not an absolute path to a regular file.
    """
    value = os.environ.get(BIN_VARIABLE)
    if value is None:
        return None
    path = Path(value)
    try:
        regular = path.is_absolute() and stat.S_ISREG(path.lstat().st_mode)
    except OSError:
        regular = False
    if not regular:
        raise ValueError("AGENTCY_BIN must be an absolute path to a regular file")
    return value


def valid_binary() -> bool:
    """Accept an unset AGENTCY_BIN or an absolute path to a regular file."""
    try:
        agentcy_binary()
    except ValueError:
        return False
    return True


def worker_command(workspace: Path) -> list[str]:
    """Run the worker's bridge server from the Go binary, or from this Python install."""
    binary = agentcy_binary()
    if binary is not None:
        return [binary, "fresh-worker", str(workspace)]
    return [sys.executable, "-E", "-P", "-m", "agentcy.mcp.fresh_server", str(workspace)]


def platform_sandbox() -> bool:
    """Refuse to start without verified read-only and bridge-store isolation."""
    return sys.platform == "darwin" and Path("/usr/bin/sandbox-exec").is_file()


def sandbox_profile(runtime: Path) -> str:
    """Restrict child writes to private scratch and shield the bridge store."""
    writable = json.dumps(str(runtime))
    root = data_dir()
    private_bridge = json.dumps(str(root))
    socket_path = json.dumps(str(root / "core.sock"))
    # The worker's bridge server runs from AGENTCY_BIN, so it stays readable even when it
    # is installed inside the bridge directory.
    executable = agentcy_binary()
    binary = f" (require-not (literal {json.dumps(executable)}))" if executable else ""
    return (
        f"(version 1) (allow default) "
        f"(deny file-write* (require-all (require-not (subpath {writable})) "
        '(require-not (literal "/dev/null")))) '
        f"(deny file-read-data (require-all (subpath {private_bridge}) "
        f"(require-not (subpath {writable})) (require-not (literal {socket_path})){binary}))"
    )
