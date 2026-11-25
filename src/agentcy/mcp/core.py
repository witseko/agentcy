"""Serve private shim calls on one Unix socket with bounded concurrent requests."""

import fcntl
import hmac
import json
import logging
import os
import secrets
import socketserver
from pathlib import Path
from typing import Annotated

from pydantic import Field, TypeAdapter, ValidationError
from sqlalchemy import select
from sqlalchemy.exc import SQLAlchemyError

from agentcy.conversations import (
    assignments,
    history,
    models,
    operator,
    operator_models,
    repository,
)
from agentcy.conversations.tables import room
from agentcy.mcp.assignment_worker import AssignmentWorker, Dispatch
from agentcy.mcp.concurrency import Notifications, transaction, wait_for_read
from agentcy.mcp.plugin_contract import Start
from agentcy.mcp.plugin_settings import Plugin, configured_plugin, plugin_names
from agentcy.mcp.transport import MAX_REQUEST
from agentcy.storage.database import data_dir, database_exists, workspace_key

LOG = logging.getLogger(__name__)
OPERATOR_KEY_FILE = "operator.key"
OPERATOR_KEY_ERROR = "Operator key rejected"
REQUEST = TypeAdapter(
    Annotated[models.MemberRequest | operator_models.OperatorAction, Field(discriminator="action")]
)
NOTIFYING_OPERATOR_ACTIONS = (
    operator_models.InviteParticipant,
    operator_models.SetOpenInvite,
    operator_models.RetireParticipant,
    operator_models.SetParticipantRole,
    operator_models.SetMemberBudget,
    operator_models.PostMessage,
    operator_models.CloseConversation,
    operator_models.ReopenConversation,
)


def rejects_operator_key(error: ValidationError) -> bool:
    """Recognize a missing or malformed operator key, so it gets the same error as a wrong key."""
    return any(
        len(item["loc"]) == 2
        and str(item["loc"][0]).startswith("operator_")
        and item["loc"][1] == "key"
        for item in error.errors()
    )


def write_operator_key(root: Path) -> str:
    """Replace the operator key at each start, so a key from an earlier core stops working."""
    key = secrets.token_urlsafe(32)
    temporary = root / (OPERATOR_KEY_FILE + ".tmp")
    temporary.unlink(missing_ok=True)
    # O_EXCL refuses an existing file or symlink, and the mode keeps the key private.
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="ascii") as file:
        file.write(key)
    os.replace(temporary, root / OPERATOR_KEY_FILE)
    return key


class Handler(socketserver.StreamRequestHandler):
    """Handle one bounded request from a shim running under this user's account."""

    def handle(self) -> None:
        """Commit before acknowledging a submission and keep secrets out of errors."""
        self.connection.settimeout(5)
        try:
            line = self.rfile.readline(MAX_REQUEST + 1)
        except (TimeoutError, ConnectionResetError, BrokenPipeError):
            return
        if not line:
            return
        try:
            if len(line) > MAX_REQUEST or not line.endswith(b"\n"):
                raise ValueError("Request exceeds size limit")
            request = REQUEST.validate_json(line)
            result = self.server.apply(request)
            response = {"result": result}
        except ValidationError as error:
            if rejects_operator_key(error):
                response = {"error": OPERATOR_KEY_ERROR}
            else:
                response = {"error": "Invalid bridge arguments"}
        except ValueError as error:
            response = {"error": str(error)}
        except (OSError, SQLAlchemyError):
            LOG.error("Core operation failed")
            response = {"error": "Core operation failed"}
        try:
            self.wfile.write(json.dumps(response, ensure_ascii=False).encode() + b"\n")
        except (BrokenPipeError, ConnectionResetError, TimeoutError):
            return


class Core(socketserver.ThreadingUnixStreamServer):
    """Exit after an idle interval when the last conversation has closed."""

    def __init__(self, address: str, operator_key: str) -> None:
        self.idle = False
        self.operator_key = operator_key
        self.notifications = Notifications()
        super().__init__(address, Handler)
        self.worker = AssignmentWorker(self.notifications)

    def apply(self, request: models.Request | operator_models.OperatorRequest) -> dict:
        """Commit each action before waking other members of the conversation."""
        if isinstance(request, operator_models.OperatorRequest):
            return self.operate(request)
        if isinstance(request, models.Read) and request.wait_seconds:
            return wait_for_read(request, self.notifications)
        if isinstance(request, models.HistoryRequest):
            return self.read_history(request)
        if isinstance(request, models.Launch):
            return self.launch(request)
        if isinstance(request, models.McpTaskRequest):
            return self.apply_mcp_task(request)
        if isinstance(request, models.Plugins):
            with transaction(request.workspace) as connection:
                assignments.launch_authority(connection, request.token)
            return {"plugins": plugin_names()}
        room_id = None
        with transaction(request.workspace) as connection:
            if isinstance(request, models.Member) and not isinstance(request, models.Status):
                room_id = repository.authenticate(connection, request.token)["room"]
            result = repository.apply(connection, request)
            if isinstance(request, models.Join):
                room_id = result["conversation"]
        if room_id is not None:
            self.notifications.notify(request.workspace, room_id)
        return result

    def read_history(self, request: models.HistoryRequest) -> dict:
        """Serve closed conversations to any chat of the workspace without creating a store."""
        if not database_exists(request.workspace):
            if isinstance(request, models.ListClosed):
                return {"conversations": []}
            raise ValueError(history.UNKNOWN_CLOSED_CONVERSATION)
        with transaction(request.workspace) as connection:
            return history.apply(connection, request)

    def apply_mcp_task(self, request: models.McpTaskRequest) -> dict:
        """Read or cancel the fresh run behind an MCP task without creating a store."""
        if not database_exists(request.workspace):
            raise ValueError(assignments.UNKNOWN_MCP_TASK)
        with transaction(request.workspace) as connection:
            if isinstance(request, models.McpTaskCancel):
                return assignments.cancel_mcp_task(connection, request.task_id)
            return assignments.mcp_task_status(connection, request.task_id)

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
            queued = assignments.queue(
                connection,
                participant,
                request.workspace,
                request.name,
                request.plugin,
                plugin.timeout_seconds,
                model=plugin.model,
                mcp_task=request.mcp_task,
            )
        return self.dispatch(request.workspace, directory, plugin, queued)

    def dispatch(
        self, workspace: str, directory: Path, plugin: Plugin, queued: tuple[dict, str | None, str]
    ) -> dict:
        """Hand a newly committed assignment and its join code to the dispatch worker."""
        result, code, room_id = queued
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
            self.worker.jobs.put(Dispatch(workspace, plugin, launch))
            self.notifications.notify(workspace, room_id)
        return result

    def operate(self, request: operator_models.OperatorRequest) -> dict:
        """Check the operator key, run one human action and wake listeners after a write."""
        if not hmac.compare_digest(request.key.encode(), self.operator_key.encode()):
            raise ValueError(OPERATOR_KEY_ERROR)
        if isinstance(request, operator_models.ListPlugins):
            return {"plugins": plugin_names()}
        workspace = operator.checkout_workspace(request.directory)
        if not isinstance(request, operator_models.CreateConversation) and not database_exists(
            workspace
        ):
            # Listing and searching must not create a store for a checkout that has none.
            if isinstance(request, operator_models.ListConversations):
                return {"conversations": []}
            if isinstance(request, operator_models.SearchConversations):
                return {"hits": []}
            raise ValueError(operator.UNKNOWN_CONVERSATION)
        if isinstance(request, operator_models.LaunchParticipant):
            return self.operator_launch(workspace, request)
        with transaction(workspace) as connection:
            result = operator.apply(connection, workspace, request)
        if isinstance(request, NOTIFYING_OPERATOR_ACTIONS):
            self.notifications.notify(workspace, request.conversation)
        return result

    def operator_launch(self, workspace: str, request: operator_models.LaunchParticipant) -> dict:
        """Queue a fresh run for the human through the same dispatch path as a lead's launch."""
        if not self.worker.ready.is_set():
            raise ValueError("Fresh-run worker is reconciling earlier runs. Retry shortly")
        directory = Path(request.directory).resolve(strict=True)
        plugin = configured_plugin(request.plugin)
        with transaction(workspace) as connection:
            queued = operator.launch(
                connection, workspace, request, plugin.timeout_seconds, model=plugin.model
            )
        return self.dispatch(workspace, directory, plugin, queued)

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
        operator_key = write_operator_key(root)
        path: Path = root / "core.sock"
        path.unlink(missing_ok=True)
        with Core(str(path), operator_key) as server:
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
