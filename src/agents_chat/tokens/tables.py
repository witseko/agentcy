"""Declare workspace and user-level tables for token usage tracking."""

from sqlalchemy import Column, ForeignKey, Index, Integer, MetaData, String, Table, text

from agents_chat.conversations.tables import metadata as workspace_metadata

participant_usage = Table(
    "participant_usage",
    workspace_metadata,
    Column("slot", ForeignKey("participant.id"), primary_key=True),
    Column("room", ForeignKey("conversation.id"), nullable=False),
    Column("estimated_input", Integer, nullable=False, server_default=text("0")),
    Column("estimated_output", Integer, nullable=False, server_default=text("0")),
    Column("reported_input", Integer),
    Column("reported_output", Integer),
    Column("source", String, nullable=False, server_default=text("'estimated'")),
    Column("updated", Integer, nullable=False),
)
Index("participant_usage_room", participant_usage.c.room)

ledger_metadata = MetaData()
model_usage = Table(
    "model_usage",
    ledger_metadata,
    Column("workspace", String, primary_key=True),
    Column("room", String, primary_key=True),
    Column("slot", Integer, primary_key=True),
    Column("model", String, nullable=False),
    Column("input_tokens", Integer, nullable=False),
    Column("output_tokens", Integer, nullable=False),
    Column("total_tokens", Integer, nullable=False),
    Column("source", String, nullable=False),
    Column("updated", Integer, nullable=False),
)
Index("model_usage_model_updated", model_usage.c.model, model_usage.c.updated)
