"""Bind an OpenCode worker to its assigned slot without exposing credentials to the model."""

import asyncio
import os
import sys
from pathlib import Path

from mcp.server.fastmcp import FastMCP

from agents_chat.conversations import models
from agents_chat.mcp import server
from agents_chat.mcp.transport import exchange
from agents_chat.storage.database import workspace_key


def serve(workspace: str, code: str) -> None:
    """Expose only the joined worker's read, status and post operations."""
    app = FastMCP("agents-chat-fresh-worker")
    token = None
    joined: dict = {}
    silent_waits: dict[str, int] = {}

    def credential() -> str:
        """Require an authenticated join before accessing this assignment."""
        if token is None:
            raise ValueError("Join your assigned conversation first")
        return token

    @app.tool()
    async def join_conversation() -> dict:
        """Join the assigned conversation and return the work brief and current roster."""
        nonlocal token, joined
        if token is None:
            joined = exchange(models.Join(workspace=workspace, code=code))
            token = joined.pop("token")
        return joined

    @app.tool()
    async def post_message(
        submission_id: models.Key,
        body: models.Body,
        kind: models.PostKind = "note",
        to: models.Label | None = None,
        reply_to: models.MessageId | None = None,
    ) -> dict:
        """Post a finding, blocker, or completion. Reply to the task ID to finish a task.

        When you can continue a blocked task, reply to its original ID with kind='resume'.
        """
        result = await asyncio.to_thread(
            exchange,
            models.Post(
                workspace=workspace,
                token=credential(),
                submission_id=submission_id,
                body=body,
                kind=kind,
                to=to,
                reply_to=reply_to,
            ),
        )
        silent_waits.clear()
        return result

    @app.tool()
    async def read_messages(after: models.Cursor = 0, wait_seconds: models.Wait = 0) -> dict:
        """Read after the last cursor and optionally wait for activity or close guidance."""
        return await server.read_member(workspace, credential(), after, wait_seconds, silent_waits)

    @app.tool()
    async def conversation_status() -> dict:
        """See member activity, fresh-run launch states and open tasks in this conversation."""
        status = await asyncio.to_thread(
            exchange, models.Status(workspace=workspace, token=credential())
        )
        status.pop("participant", None)
        return status

    app.run(transport="stdio")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("Worker requires its configured workspace")
    join_code = os.environ.pop("AGENTS_CHAT_FRESH_JOIN_CODE", None)
    if join_code is None:
        raise SystemExit("Worker requires a private join credential")
    os.umask(0o077)
    serve(workspace_key(Path(sys.argv[1])), join_code)
