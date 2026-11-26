"""Run an MCP stdio server and copy every JSON-RPC line in both directions to a log file.

Usage: python tee_stdio.py LOG_PATH -- COMMAND [ARGS...]

Each log line is a JSON object {"direction": "client_to_server" | "server_to_client",
"message": <the JSON-RPC message>}. Bytes pass through unchanged; stderr is inherited.
"""

import json
import subprocess
import sys
import threading
from typing import IO


def pump(source: IO[bytes], sink: IO[bytes], direction: str, log: IO[str], lock: threading.Lock) -> None:
    """Copy lines from source to sink and log each one as a parsed message."""
    for line in iter(source.readline, b""):
        text = line.decode("utf-8").strip()
        if text:
            # Log first, so a step marker written after the client sees a reply comes later.
            with lock:
                log.write(json.dumps({"direction": direction, "message": json.loads(text)}) + "\n")
                log.flush()
        sink.write(line)
        sink.flush()
    sink.close()


def main() -> None:
    """Start the child, pump both streams, and exit with the child's code."""
    log_path, separator, *command = sys.argv[1:]
    if separator != "--" or not command:
        raise SystemExit(__doc__)
    child = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE)  # noqa: S603
    lock = threading.Lock()
    with open(log_path, "a", encoding="utf-8") as log:
        upstream = threading.Thread(
            target=pump,
            args=(sys.stdin.buffer, child.stdin, "client_to_server", log, lock),
            daemon=True,
        )
        upstream.start()
        pump(child.stdout, sys.stdout.buffer, "server_to_client", log, lock)
        sys.exit(child.wait())


if __name__ == "__main__":
    main()
