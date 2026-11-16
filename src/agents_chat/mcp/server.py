"""Expose the same conversation tools to every desktop MCP client."""

import asyncio
import time
from typing import Annotated

from mcp.server.fastmcp import FastMCP
from pydantic import Field

from agents_chat.conversations import models
from agents_chat.mcp.transport import exchange


def serve(workspace: str) -> None:
    """Bind a stdio MCP connection to one desktop-configured workspace."""
    server = FastMCP(
        "agents-chat",
        instructions=(
            "Create a conversation and give each invited chat its own join code. "
            "Keep your participant token in this chat. Never put credentials in shared messages. "
            "Use read_messages to catch up, then post_message to contribute."
        ),
    )

    @server.tool()
    def create_conversation(
        brief: models.Body, your_name: models.Label, invitees: models.Slots
    ) -> dict:
        """Start a shared discussion. Return your private token and one join code per name."""
        return exchange(
            models.Create(workspace=workspace, brief=brief, initiator=your_name, invitees=invitees)
        )

    @server.tool()
    def join_conversation(code: models.Key) -> dict:
        """Redeem your one-time invitation to get the brief and your private participant token."""
        return exchange(models.Join(workspace=workspace, code=code))

    @server.tool()
    def post_message(token: models.Key, submission_id: models.Key, body: models.Body) -> dict:
        """Share text with all members. Pick a unique submission ID and reuse it on retries."""
        return exchange(
            models.Post(workspace=workspace, token=token, submission_id=submission_id, body=body)
        )

    @server.tool()
    async def read_messages(
        token: models.Key,
        after: models.Cursor = 0,
        wait_seconds: Annotated[int, Field(ge=0, le=20)] = 0,
    ) -> dict:
        """Read after your last cursor (initially zero). Optionally wait up to 20 seconds."""
        request = models.Read(workspace=workspace, token=token, after=after)
        deadline = time.monotonic() + wait_seconds
        while True:
            page = await asyncio.to_thread(exchange, request)
            remaining = deadline - time.monotonic()
            if page["messages"] or remaining <= 0:
                return page
            await asyncio.sleep(min(0.5, remaining))

    @server.tool()
    def close_conversation(token: models.Key) -> dict:
        """Close the discussion as its creator. All tokens and unused invitations then expire."""
        return exchange(models.Close(workspace=workspace, token=token))

    server.run(transport="stdio")
