"""Serve private shim calls on one Unix socket with serial database transactions."""

import fcntl
import json
import logging
import os
import socketserver
from pathlib import Path

from pydantic import ValidationError
from sqlalchemy import select
from sqlalchemy.exc import SQLAlchemyError

from agents_chat.conversations import repository
from agents_chat.conversations.models import REQUEST
from agents_chat.conversations.tables import room
from agents_chat.mcp.transport import MAX_REQUEST
from agents_chat.storage.database import data_dir, database

LOG = logging.getLogger(__name__)


class Handler(socketserver.StreamRequestHandler):
    """Handle one bounded request from a shim running under this user's account."""

    def handle(self) -> None:
        """Commit before acknowledging a submission and keep secrets out of errors."""
        self.connection.settimeout(5)
        line = self.rfile.readline(MAX_REQUEST + 1)
        if not line:
            return
        try:
            if len(line) > MAX_REQUEST or not line.endswith(b"\n"):
                raise ValueError("Request exceeds size limit")
            request = REQUEST.validate_json(line)
            with database(request.workspace).begin() as connection:
                result = repository.apply(connection, request)
            response = {"result": result}
        except ValidationError:
            response = {"error": "Invalid bridge arguments"}
        except ValueError as error:
            response = {"error": str(error)}
        except (OSError, SQLAlchemyError):
            LOG.error("Core operation failed")
            response = {"error": "Core operation failed"}
        self.wfile.write(json.dumps(response, ensure_ascii=False).encode() + b"\n")


class Core(socketserver.UnixStreamServer):
    """Exit after an idle interval when the last conversation has closed."""

    idle = False

    def handle_timeout(self) -> None:
        """Check persisted conversations, including stores from a previous process."""
        self.idle = True
        for path in data_dir().glob("*.sqlite3"):
            with database(path.stem).connect() as connection:
                if connection.scalar(select(room.c.id).where(room.c.open == 1).limit(1)):
                    self.idle = False
                    return


def main() -> None:
    """Own the daemon lock before unlinking a socket left by a previous process."""
    os.umask(0o077)
    root = data_dir()
    with (root / "core.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        path: Path = root / "core.sock"
        path.unlink(missing_ok=True)
        with Core(str(path), Handler) as server:
            path.chmod(0o600)
            server.timeout = 60
            try:
                while not server.idle:
                    server.handle_request()
            finally:
                path.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
