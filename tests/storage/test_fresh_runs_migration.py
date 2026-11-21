"""Open a version-five conversation with the new assignment schema."""

import sqlite3
from collections.abc import Callable
from pathlib import Path
from secrets import token_hex

import pytest
from sqlalchemy import inspect

from agents_chat.conversations import models, repository
from agents_chat.storage.database import SCHEMA_VERSION, database


def test_upgrade_from_version_five_preserves_conversation(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    undo_version_eight: Callable[[sqlite3.Connection], None],
) -> None:
    """Upgrade a sealed conversation from the schema before assignments were stored."""
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    workspace = token_hex(32)
    original = database(workspace)
    with original.begin() as connection:
        result = repository.apply(
            connection,
            models.Create(
                workspace=workspace,
                brief="Round",
                initiator="Lead",
                invitees=["Worker"],
                sealed=True,
            ),
        )
    original.dispose()
    database.cache_clear()
    with sqlite3.connect(tmp_path / f"{workspace}.sqlite3") as previous:
        undo_version_eight(previous)
        previous.execute("DROP TABLE assignment")
        previous.execute("ALTER TABLE conversation DROP COLUMN created")
        previous.execute("PRAGMA user_version=5")

    upgraded = database(workspace)
    try:
        with upgraded.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            assert inspect(connection).has_table("assignment")
            status = repository.apply(
                connection, models.Status(workspace=workspace, token=result["token"])
            )
            assert status["round"] == "sealed"
            assert status["members"]["Worker"]["state"] == "invited"
    finally:
        upgraded.dispose()
