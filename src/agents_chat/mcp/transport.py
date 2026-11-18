"""Forward local requests to the user-owned core and serialize concurrent startups."""

import fcntl
import json
import os
import socket
import subprocess
import sys
import time
from pathlib import Path

from agents_chat.conversations.models import MAX_WAIT_SECONDS, Request
from agents_chat.storage.database import data_dir

MAX_REQUEST = 128 * 1024
MAX_RESPONSE = 2 * 1024 * 1024


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
        env = {**os.environ, "AGENTS_CHAT_HOME": str(root)}
        with (root / "core.log").open("a") as log:
            # Reuse the shim's interpreter for its installed packages. Isolated mode
            # ignores Python environment settings, the working directory, and user site-packages.
            # nosemgrep: dangerous-subprocess-use-audit
            process = subprocess.Popen(
                [sys.executable, "-I", "-m", "agents_chat.mcp.core"],
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


def exchange(request: Request) -> dict:
    """Exchange a bounded JSON line. Never log prompts or participant tokens."""
    with core_connection() as client, client.makefile("rb") as reader:
        client.sendall(request.model_dump_json().encode() + b"\n")
        line = reader.readline(MAX_RESPONSE + 1)
        if not line.endswith(b"\n") or len(line) > MAX_RESPONSE:
            raise RuntimeError("Incomplete bridge response")
        response = json.loads(line)
        if "error" in response:
            raise ValueError(response["error"])
        return response["result"]
