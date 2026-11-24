"""Declare durable assignment state and expiring launch reservations."""

from sqlalchemy import Column, ForeignKey, Index, Integer, String, Table

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
)
Index("assignment_active", assignment.c.state, assignment.c.deadline)
