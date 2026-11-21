"""Query the full-text index of conversation briefs and published messages."""

import re

from sqlalchemy import Connection, func, literal_column, select

from agents_chat.conversations.search_tables import conversation_search
from agents_chat.conversations.tables import member, message, room

SEARCH_LIMIT = 50
MAX_TERMS = 8
SNIPPET_TOKENS = 12
WORD = re.compile(r"\w+")


def match_expression(query: str) -> str:
    """Turn free text into an FTS5 query that matches every word as a prefix.

    Each word is quoted, so FTS5 operators and punctuation in the text have no effect.
    """
    terms = WORD.findall(query)[:MAX_TERMS]
    if not terms:
        raise ValueError("Search needs at least one letter or digit")
    return " ".join(f'"{term}"*' for term in terms)


def find_matches(connection: Connection, query: str, brief_length: int) -> list[dict]:
    """Return the best matches with their conversation, author, time and a short snippet.

    A brief match has no message, so its time is the conversation's created time.
    """
    index = literal_column("conversation_search")
    snippet = func.snippet(index, 0, "", "", "…", SNIPPET_TOKENS)
    rows = connection.execute(
        select(
            conversation_search.c.room,
            func.substr(room.c.brief, 1, brief_length).label("brief_start"),
            room.c.archived,
            conversation_search.c.message_id,
            member.c.label.label("author"),
            func.coalesce(message.c.created, room.c.created).label("created"),
            snippet.label("snippet"),
        )
        .select_from(
            conversation_search.join(room, room.c.id == conversation_search.c.room)
            .outerjoin(message, message.c.id == conversation_search.c.message_id)
            .outerjoin(member, member.c.id == message.c.author)
        )
        .where(index.match(match_expression(query)))
        .order_by(conversation_search.c.rank)
        .limit(SEARCH_LIMIT)
    ).mappings()
    return [dict(row) for row in rows]
