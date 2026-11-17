"""Serve private shim calls on one Unix socket with bounded concurrent requests."""

import fcntl
import json
import logging
import os
import socketserver
from pathlib import Path

from pydantic import ValidationError
from sqlalchemy import select
from sqlalchemy.exc import SQLAlchemyError

from agents_chat.conversations import assignments, models, repository
from agents_chat.conversations.tables import room
from agents_chat.mcp.assignment_worker import AssignmentWorker, Dispatch
from agents_chat.mcp.concurrency import Notifications, transaction, wait_for_read
from agents_chat.mcp.plugin_contract import Start
from agents_chat.mcp.plugin_settings import configured_plugin
from agents_chat.mcp.transport import MAX_REQUEST
from agents_chat.storage.database import data_dir, workspace_key

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
            request = models.REQUEST.validate_json(line)
            result = self.server.apply(request)
            response = {"result": result}
        except ValidationError:
            response = {"error": "Invalid bridge arguments"}
        except ValueError as error:
            response = {"error": str(error)}
        except (OSError, SQLAlchemyError):
            LOG.error("Core operation failed")
            response = {"error": "Core operation failed"}
        self.wfile.write(json.dumps(response, ensure_ascii=False).encode() + b"\n")


class Core(socketserver.ThreadingUnixStreamServer):
    """Exit after an idle interval when the last conversation has closed."""

    def __init__(self, address: str) -> None:
        self.idle = False
        self.notifications = Notifications()
        super().__init__(address, Handler)
        self.worker = AssignmentWorker(self.notifications)

    def apply(self, request: models.Request) -> dict:
        """Commit each action before waking other members of the conversation."""
        if isinstance(request, models.Read) and request.wait_seconds:
            return wait_for_read(request, self.notifications)
        if isinstance(request, models.Launch):
            return self.launch(request)
        room_id = None
        with transaction(request.workspace) as connection:
            if isinstance(request, models.Member) and not isinstance(request, models.Status):
                room_id = repository.authenticate(connection, request.token)["room"]
            result = repository.apply(connection, request)
        if room_id is not None:
            self.notifications.notify(request.workspace, room_id)
        return result

    def launch(self, request: models.Launch) -> dict:
        """Bind a requested worker to this checkout and queue the committed assignment."""
        with transaction(request.workspace) as connection:
            participant = assignments.launch_authority(connection, request.token)
            if not self.worker.ready.is_set():
                raise ValueError("Fresh-run worker is reconciling earlier runs. Retry shortly")
            directory = Path(request.directory)
            if not directory.is_absolute() or workspace_key(directory) != request.workspace:
                raise ValueError("Fresh-run checkout is outside the requested workspace")
            directory = directory.resolve(strict=True)
            plugin = configured_plugin(request.plugin)
            result, code, room_id = assignments.queue(
                connection, participant, request, plugin.timeout_seconds
            )
        if code is not None:
            launch = Start(
                assignment_id=result["assignment_id"],
                workspace=str(directory),
                bridge_home=str(data_dir()),
                join_code=code,
                model=plugin.model,
                timeout_seconds=plugin.timeout_seconds,
                token_cap=plugin.token_cap,
                cost_cap_microusd=plugin.cost_cap_microusd,
            )
            self.worker.jobs.put(Dispatch(request.workspace, plugin, launch))
            self.notifications.notify(request.workspace, room_id)
        return result

    def handle_timeout(self) -> None:
        """Check persisted conversations, including stores from a previous process."""
        self.idle = True
        for path in data_dir().glob("*.sqlite3"):
            with transaction(path.stem) as connection:
                if assignments.has_pending(connection) or connection.scalar(
                    select(room.c.id).where(room.c.open == 1).limit(1)
                ):
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
        with Core(str(path)) as server:
            path.chmod(0o600)
            server.timeout = 60
            server.worker.start()
            try:
                while not server.idle:
                    server.handle_request()
            finally:
                server.worker.jobs.put(None)
                server.worker.join()
                path.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
