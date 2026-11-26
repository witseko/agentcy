"""Forward local requests to the user-owned core and serialize concurrent startups."""

import fcntl
import json
import os
import socket
import subprocess
import sys
import time
from pathlib import Path

from agentcy.conversations.models import MAX_WAIT_SECONDS, Request
from agentcy.storage.database import data_dir

MAX_REQUEST = 128 * 1024
MAX_RESPONSE = 2 * 1024 * 1024
INVALID_ARGUMENTS = "Invalid bridge arguments"
CORE_VERSION_HELP = (
    "This shim built a valid request, so the running core is likely an older or newer "
    "version. Stop the process running agentcy.mcp.core and retry. The next call starts "
    "the installed core. Join codes and conversations are kept."
)
INVALID_CORE_BIN = "Bridge startup failed. AGENTCY_CORE_BIN must be an absolute path to a file."
IDEMPOTENT_ACTIONS = frozenset(
    {
        "read",
        "status",
        "plugins",
        "post",
        "close",
        "reopen",
        "release",
        "report_usage",
        "mcp_task_status",
        "mcp_task_cancel",
    }
)


class IncompleteResponseError(RuntimeError):
    """Raised when the local core closes the socket before sending a full JSON line."""


def connect(path: Path) -> socket.socket:
    """Open the private socket with a bounded response time."""
    client = socket.socket(socket.AF_UNIX)
    try:
        client.settimeout(MAX_WAIT_SECONDS + 10)
        client.connect(str(path))
        return client
    except OSError:
        client.close()
        raise


def core_command() -> list[str]:
    """Return the command that starts the core.

    AGENTCY_CORE_BIN selects another core executable, run as `<path> core`. It must be an
    absolute path to an existing regular file, so a relative name never resolves against
    the working directory or PATH.

    Raises:
        RuntimeError: If AGENTCY_CORE_BIN is set to anything else.
    """
    core_bin = os.environ.get("AGENTCY_CORE_BIN")
    if core_bin is None:
        # Reuse the shim's interpreter for its installed packages. Isolated mode ignores
        # Python environment settings, the working directory, and user site-packages.
        return [sys.executable, "-I", "-m", "agentcy.mcp.core"]
    path = Path(core_bin)
    if not path.is_absolute() or not path.is_file():
        raise RuntimeError(INVALID_CORE_BIN)
    return [core_bin, "core"]


def core_connection() -> socket.socket:
    """Recheck under the startup lock because several apps may open together."""
    root = data_dir()
    path = root / "core.sock"
    try:
        return connect(path)
    except (FileNotFoundError, ConnectionRefusedError):
        pass
    with (root / "start.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            return connect(path)
        except (FileNotFoundError, ConnectionRefusedError):
            pass
        command = core_command()
        env = {**os.environ, "AGENTCY_HOME": str(root)}
        with (root / "core.log").open("a") as log:
            # core_command() checked the executable; the argument list runs with no shell.
            # nosemgrep: dangerous-subprocess-use-audit
            process = subprocess.Popen(  # noqa: S603
                command,
                env=env,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.DEVNULL,
                stderr=log,
                start_new_session=True,
            )
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline and process.poll() is None:
            try:
                return connect(path)
            except (FileNotFoundError, ConnectionRefusedError):
                time.sleep(0.05)
        raise RuntimeError(f"Bridge startup failed. See {root / 'core.log'}")


def _exchange_once(request: Request) -> dict:
    """Send one bounded JSON line and decode the bridge response."""
    with core_connection() as client, client.makefile("rb") as reader:
        client.sendall(request.model_dump_json().encode() + b"\n")
        line = reader.readline(MAX_RESPONSE + 1)
        if not line.endswith(b"\n") or len(line) > MAX_RESPONSE:
            raise IncompleteResponseError("Incomplete bridge response")
        response = json.loads(line)
        if "error" in response:
            error = response["error"]
            # The request passed this shim's own validation, so a rejection is a version skew.
            if error.startswith(INVALID_ARGUMENTS):
                error = f"{error}. {CORE_VERSION_HELP}"
            raise ValueError(error)
        return response["result"]


def exchange(request: Request) -> dict:
    """Exchange a bounded JSON line, retrying once on transient disconnects for idempotent calls."""
    try:
        return _exchange_once(request)
    except (BrokenPipeError, ConnectionResetError, IncompleteResponseError):
        if getattr(request, "action", None) not in IDEMPOTENT_ACTIONS:
            raise
        return _exchange_once(request)
