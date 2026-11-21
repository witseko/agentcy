"""Open a version-six conversation with the created conversation column."""

import sqlite3
from pathlib import Path
from secrets import token_hex

import pytest
from sqlalchemy import inspect

from agents_chat.conversations import models, operator, repository
from agents_chat.storage.database import SCHEMA_VERSION, database


def open_store(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> str:
    """Point the bridge at a private data directory and return a new workspace key."""
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    return token_hex(32)


def test_new_store_starts_at_version_seven(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Create a new store with the created column at the current version."""
    workspace = open_store(monkeypatch, tmp_path)
    engine = database(workspace)
    try:
        with engine.begin() as connection:
            assert SCHEMA_VERSION == 7
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == 7
            columns = {column["name"] for column in inspect(connection).get_columns("conversation")}
            assert "created" in columns
    finally:
        engine.dispose()


def test_upgrade_from_version_six_lists_old_conversations(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Add the created column as NULL and keep old conversations readable and listable."""
    workspace = open_store(monkeypatch, tmp_path)
    original = database(workspace)
    with original.begin() as connection:
        result = repository.apply(
            connection,
            models.Create(
                workspace=workspace,
                brief="Old brief",
                initiator="Lead",
                invitees=["W"],
            ),
        )
    original.dispose()
    database.cache_clear()
    with sqlite3.connect(tmp_path / f"{workspace}.sqlite3") as previous:
        previous.execute("ALTER TABLE conversation DROP COLUMN created")
        previous.execute("PRAGMA user_version=6")

    upgraded = database(workspace)
    try:
        with upgraded.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == 7
            status = repository.apply(
                connection, models.Status(workspace=workspace, token=result["token"])
            )
            assert status["participant"] == "Lead"
            listed = operator.list_conversations(connection)["conversations"]
            assert listed == [
                {
                    "conversation": result["conversation"],
                    "title": "Old brief",
                    "open": True,
                    "created": None,
                    "members": 2,
                    "joined": 1,
                }
            ]
    finally:
        upgraded.dispose()
