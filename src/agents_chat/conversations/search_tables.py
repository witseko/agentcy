"""Declare the full-text index over conversation briefs and message bodies."""

from sqlalchemy import Connection, column, table

# The index keeps its own copy of each text. Briefs and messages are never edited or deleted
# today; code that starts deleting them must also delete their rows here.
CREATE_INDEX = (
    "CREATE VIRTUAL TABLE conversation_search "
    "USING fts5(body, room UNINDEXED, message_id UNINDEXED)"
)
INDEX_NEW_BRIEFS = (
    "CREATE TRIGGER conversation_search_brief AFTER INSERT ON conversation BEGIN "
    "INSERT INTO conversation_search (body, room, message_id) VALUES (new.brief, new.id, NULL); "
    "END"
)
INDEX_NEW_MESSAGES = (
    "CREATE TRIGGER conversation_search_message AFTER INSERT ON message BEGIN "
    "INSERT INTO conversation_search (body, room, message_id) "
    "VALUES (new.body, new.room, new.id); "
    "END"
)
INDEX_EXISTING_BRIEFS = (
    "INSERT INTO conversation_search (body, room, message_id) "
    "SELECT brief, id, NULL FROM conversation"
)
INDEX_EXISTING_MESSAGES = (
    "INSERT INTO conversation_search (body, room, message_id) SELECT body, room, id FROM message"
)

# A lightweight handle for queries. The virtual table is created by create_search_index.
conversation_search = table(
    "conversation_search",
    column("rowid"),
    column("body"),
    column("room"),
    column("message_id"),
    column("rank"),
)


def create_search_index(connection: Connection, backfill: bool) -> None:
    """Create the index and its insert triggers, and index existing rows when asked."""
    for statement in (CREATE_INDEX, INDEX_NEW_BRIEFS, INDEX_NEW_MESSAGES):
        connection.exec_driver_sql(statement)
    if backfill:
        connection.exec_driver_sql(INDEX_EXISTING_BRIEFS)
        connection.exec_driver_sql(INDEX_EXISTING_MESSAGES)
