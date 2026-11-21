"""Open a version-seven store and find its old conversations after adding search."""

import sqlite3
from collections.abc import Callable
from pathlib import Path
from secrets import token_hex

import pytest
from sqlalchemy import Connection, inspect

from agents_chat.conversations import models, operator, repository
from agents_chat.conversations import operator_models as actions
from agents_chat.storage.database import SCHEMA_VERSION, database

ENVELOPE = {"version": 1, "key": "test-operator-key", "directory": "/work/repository"}


def search(connection: Connection, workspace: str, query: str) -> list[dict]:
    """Run an operator search in the store."""
    request = actions.SearchConversations.model_validate(
        {**ENVELOPE, "action": "operator_search", "query": query}
    )
    return operator.apply(connection, workspace, request)["hits"]


def test_new_store_has_the_index_and_archived_column(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Create a new store at version eight with the search index."""
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    engine = database(token_hex(32))
    try:
        with engine.begin() as connection:
            assert SCHEMA_VERSION == 8
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == 8
            columns = {column["name"] for column in inspect(connection).get_columns("conversation")}
            assert "archived" in columns
            assert inspect(connection).has_table("conversation_search")
    finally:
        engine.dispose()


def test_upgrade_from_version_seven_indexes_old_conversations(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    undo_version_eight: Callable[[sqlite3.Connection], None],
) -> None:
    """Index existing briefs and messages, keep chats active, and index new messages."""
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    workspace = token_hex(32)
    original = database(workspace)
    with original.begin() as connection:
        created = repository.apply(
            connection,
            models.Create(
                workspace=workspace, brief="Old pelican brief", initiator="Lead", invitees=["W"]
            ),
        )
        repository.apply(
            connection,
            models.Post(
                workspace=workspace,
                token=created["token"],
                submission_id="old",
                body="An old walrus finding",
            ),
        )
    original.dispose()
    database.cache_clear()
    with sqlite3.connect(tmp_path / f"{workspace}.sqlite3") as previous:
        undo_version_eight(previous)
        previous.execute("PRAGMA user_version=7")

    upgraded = database(workspace)
    try:
        with upgraded.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == 8
            assert [hit["message_id"] for hit in search(connection, workspace, "pelican")] == [None]
            walrus = search(connection, workspace, "walrus")
            assert [(hit["author"], hit["archived"]) for hit in walrus] == [("Lead", False)]
            listed = operator.list_conversations(connection)["conversations"]
            assert [item["conversation"] for item in listed] == [created["conversation"]]
            repository.apply(
                connection,
                models.Post(
                    workspace=workspace,
                    token=created["token"],
                    submission_id="new",
                    body="A new narwhal finding",
                ),
            )
            assert len(search(connection, workspace, "narwhal")) == 1
    finally:
        upgraded.dispose()
