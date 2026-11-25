"""Declare durable assignment state and expiring launch reservations."""

from sqlalchemy import Column, ForeignKey, Index, Integer, String, Table, text

from agentcy.conversations.tables import metadata

PENDING_STATES = ("queued", "starting", "running")

assignment = Table(
    "assignment",
    metadata,
    Column("id", String, primary_key=True),
    Column("slot", ForeignKey("participant.id"), nullable=False, unique=True),
    Column("room", ForeignKey("conversation.id"), nullable=False),
    Column("plugin", String, nullable=False),
    Column("invite_hash", String, nullable=False),
    Column("state", String, nullable=False),
    Column("deadline", Integer, nullable=False),
    Column("round", String),
    Column("exit_code", Integer),
    # SHA-256 of the MCP task ID. The ID itself is a bearer capability and is never stored.
    Column("mcp_task_hash", String),
    Column("cancel_requested", Integer, nullable=False, server_default=text("0")),
    Column("created", Integer),
    Column("updated", Integer),
)
Index("assignment_active", assignment.c.state, assignment.c.deadline)
Index("assignment_mcp_task_hash", assignment.c.mcp_task_hash, unique=True)
