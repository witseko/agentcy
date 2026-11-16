"""Declare the initial bridge schema for conversations and private membership."""

from sqlalchemy import Column, ForeignKey, Index, Integer, MetaData, String, Table, UniqueConstraint

metadata = MetaData()
room = Table(
    "conversation",
    metadata,
    Column("id", String, primary_key=True),
    Column("brief", String, nullable=False),
    Column("open", Integer, nullable=False),
)
member = Table(
    "participant",
    metadata,
    Column("id", Integer, primary_key=True),
    Column("room", ForeignKey("conversation.id"), nullable=False),
    Column("label", String, nullable=False),
    Column("creator", Integer, nullable=False),
    Column("invite_hash", String, unique=True),
    Column("expires", Integer),
    Column("token_hash", String, unique=True),
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
)
Index("message_room_cursor", message.c.room, message.c.id)
Index("message_author_recent", message.c.author, message.c.created)
receipt = Table(
    "submission",
    metadata,
    Column("author", ForeignKey("participant.id"), primary_key=True),
    Column("key", String, primary_key=True),
    Column("message_id", ForeignKey("message.id"), nullable=False),
)
