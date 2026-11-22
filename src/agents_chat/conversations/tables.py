"""Declare the bridge schema for conversations and private membership."""

from sqlalchemy import (
    CheckConstraint,
    Column,
    ForeignKey,
    Index,
    Integer,
    MetaData,
    String,
    Table,
    UniqueConstraint,
    text,
)

metadata = MetaData()
room = Table(
    "conversation",
    metadata,
    Column("id", String, primary_key=True),
    Column("brief", String, nullable=False),
    Column("open", Integer, nullable=False),
    Column("round", String),
    Column("round_deadline", Integer),
    Column("created", Integer),
    Column("archived", Integer, nullable=False, server_default=text("0")),
    Column("open_invite_hash", String),
)
Index("conversation_open_invite_hash", room.c.open_invite_hash, unique=True)
member = Table(
    "participant",
    metadata,
    Column("id", Integer, primary_key=True),
    Column("room", ForeignKey("conversation.id"), nullable=False),
    Column("label", String, nullable=False),
    Column("role", Integer, nullable=False),
    Column("invite_hash", String, unique=True),
    Column("expires", Integer),
    Column("token_hash", String, unique=True),
    Column("seen", Integer),
    Column("retired", Integer, nullable=False, server_default=text("0")),
    UniqueConstraint("room", "label"),
)
message = Table(
    "message",
    metadata,
    Column("id", Integer, primary_key=True),
    Column("room", ForeignKey("conversation.id"), nullable=False),
    Column("author", ForeignKey("participant.id"), nullable=False),
    Column("body", String, nullable=False),
    Column("created", Integer, nullable=False),
    Column("kind", String),
    Column("recipient", ForeignKey("participant.id")),
    Column("reply_to", ForeignKey("message.id")),
)
Index("message_room_cursor", message.c.room, message.c.id)
Index("message_author_recent", message.c.author, message.c.created)
proposal = Table(
    "proposal",
    metadata,
    Column("id", String, primary_key=True),
    Column("room", ForeignKey("conversation.id"), nullable=False),
    Column("author", ForeignKey("participant.id"), nullable=False),
    Column("body", String, nullable=False),
    UniqueConstraint("room", "author"),
)
SUBMISSION_TARGET = "(message_id IS NULL) != (proposal_id IS NULL)"
receipt = Table(
    "submission",
    metadata,
    Column("author", ForeignKey("participant.id"), primary_key=True),
    Column("key", String, primary_key=True),
    Column("message_id", ForeignKey("message.id")),
    Column("proposal_id", ForeignKey("proposal.id", name="submission_proposal_fk")),
    CheckConstraint(SUBMISSION_TARGET, name="submission_target"),
)
