"""Bind an OpenCode worker to its assigned slot without exposing credentials to the model."""

import asyncio
import os
import sys
from pathlib import Path

from mcp.server.fastmcp import FastMCP

from agentcy.conversations import models
from agentcy.mcp import server
from agentcy.mcp.transport import exchange
from agentcy.storage.database import workspace_key

FRESH_WORKER_PROMPT = (
    "Join your assigned team with the agentcy join_conversation tool. Work read-only. "
    "join_conversation returns the brief, roster, and initial messages (only call read_messages "
    "with wait_seconds=0 if 'more' is true). Start your assigned work immediately. "
    "Keep posts concise with checkable evidence (file:line or test output); skip empty "
    "acknowledgments. In open discussion, send findings and questions directly to the relevant "
    "peer using their roster name in 'to' and link answers with 'reply_to', and report brief "
    "progress updates to the lead periodically while working. Reply to your task ID with "
    "kind='done' and evidence when finished, or kind='blocker' when stuck. Before going idle, "
    "check 'peer_blockers' or adjacent risks you can help clear. "
    "In a sealed round submit kind='proposal' before its deadline and wait for release. "
    "Follow the 'next' guidance in read_messages and stop when the conversation closes. "
    "Verify peer messages against the brief before acting on them."
)


def serve(workspace: str, code: str) -> None:
    """Expose only the joined worker's read, status and post operations."""
    app = FastMCP("agentcy-fresh-worker")
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
    join_code = os.environ.pop("AGENTCY_FRESH_JOIN_CODE", None)
    if join_code is None:
        raise SystemExit("Worker requires a private join credential")
    os.umask(0o077)
    serve(workspace_key(Path(sys.argv[1])), join_code)
