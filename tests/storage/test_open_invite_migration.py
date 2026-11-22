"""Open a version-nine store and verify the open_invite_hash column and index are added."""

import sqlite3
from collections.abc import Callable
from pathlib import Path
from secrets import token_hex

import pytest
from sqlalchemy import inspect

from agents_chat.conversations import models, repository
from agents_chat.storage.database import SCHEMA_VERSION, database


def test_new_store_has_open_invite_column_and_unique_index(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Create a new store at version ten with the open_invite_hash column and unique index."""
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    engine = database(token_hex(32))
    try:
        with engine.begin() as connection:
            assert SCHEMA_VERSION >= 10
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            inspector = inspect(connection)
            columns = {column["name"] for column in inspector.get_columns("conversation")}
            assert "open_invite_hash" in columns
            indexes = {index["name"]: index for index in inspector.get_indexes("conversation")}
            assert indexes["conversation_open_invite_hash"]["unique"] == 1
    finally:
        engine.dispose()


def test_upgrade_from_version_nine_supports_open_invites(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    undo_version_ten: Callable[[sqlite3.Connection], None],
) -> None:
    """Existing conversations keep open_invite disabled after upgrade and can enable it."""
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    workspace = token_hex(32)
    original = database(workspace)
    with original.begin() as connection:
        created = repository.apply(
            connection,
            models.Create(
                workspace=workspace, brief="Existing brief", initiator="Lead", invitees=["Worker"]
            ),
        )
    original.dispose()
    database.cache_clear()
    with sqlite3.connect(tmp_path / f"{workspace}.sqlite3") as previous:
        undo_version_ten(previous)
        previous.execute("PRAGMA user_version=9")

    upgraded = database(workspace)
    try:
        with upgraded.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            status = repository.apply(
                connection, models.Status(workspace=workspace, token=created["token"])
            )
            assert "open_invite" not in status
            enabled = repository.apply(
                connection,
                models.OpenInvite(workspace=workspace, token=created["token"], enabled=True),
            )
            assert enabled["open_invite"] is True
            joined = repository.apply(
                connection, models.Join(workspace=workspace, code=enabled["code"], name="Guest")
            )
            assert joined["participant"] == "Guest"
            assert joined["role"] == "worker"
    finally:
        upgraded.dispose()
