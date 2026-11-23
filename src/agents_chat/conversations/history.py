"""Let any chat of the workspace read closed conversations without a participant token."""

from sqlalchemy import Connection, func, literal_column, select

from agents_chat.conversations import models, operator, repository
from agents_chat.conversations.tables import message, room

LIST_LIMIT = 100
PAGE_SIZE = repository.PAGE_SIZE
TITLE_LENGTH = repository.TITLE_LENGTH
# An open conversation gets the same error as a missing one, so its existence stays private.
UNKNOWN_CLOSED_CONVERSATION = "Unknown closed conversation in this workspace"


def list_closed(connection: Connection, archived: bool | None = None) -> dict:
    """Summarize the newest closed conversations, optionally only archived or only active ones.

    Conversations without a created time come last.
    """
    messages = (
        select(func.count())
        .select_from(message)
        .where(message.c.room == room.c.id)
        .scalar_subquery()
    )
    query = select(
        room.c.id,
        func.substr(room.c.brief, 1, TITLE_LENGTH + 1).label("brief_start"),
        room.c.created,
        room.c.archived,
        messages.label("messages"),
    ).where(room.c.open == 0)
    if archived is not None:
        query = query.where(room.c.archived == int(archived))
    rows = connection.execute(
        query.order_by(
            room.c.created.is_(None),
            room.c.created.desc(),
            literal_column("conversation.rowid").desc(),
        ).limit(LIST_LIMIT)
    ).mappings()
    return {
        "conversations": [
            {
                "conversation": row["id"],
                "title": repository.session_title(row["brief_start"]),
                "created": row["created"],
                "archived": bool(row["archived"]),
                "messages": row["messages"],
            }
            for row in rows
        ]
    }


def read_closed(connection: Connection, request: models.ReadClosed) -> dict:
    """Return one page of a closed conversation, with the brief and roster from cursor zero."""
    record = (
        connection.execute(
            select(room.c.brief, room.c.created, room.c.archived).where(
                room.c.id == request.conversation, room.c.open == 0
            )
        )
        .mappings()
        .one_or_none()
    )
    if record is None:
        raise ValueError(UNKNOWN_CLOSED_CONVERSATION)
    result = {
        "conversation": request.conversation,
        "closed": True,
        "archived": bool(record["archived"]),
        "created": record["created"],
    }
    if request.after == 0:
        result["brief"] = record["brief"]
        result["roster"] = repository.roster(connection, request.conversation)
    page = operator.transcript(
        connection, request.conversation, after=request.after, page_size=PAGE_SIZE
    )
    return {**result, **page}


def apply(connection: Connection, request: models.HistoryRequest) -> dict:
    """Dispatch one token-free read of the workspace's closed conversations."""
    if isinstance(request, models.ListClosed):
        return list_closed(connection, request.archived)
    return read_closed(connection, request)
