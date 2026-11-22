"""Open a version-eight store and verify the retired participant column is added."""

import sqlite3
from collections.abc import Callable
from pathlib import Path
from secrets import token_hex

import pytest
from sqlalchemy import inspect

from agents_chat.conversations import models, operator, repository
from agents_chat.conversations import operator_models as actions
from agents_chat.storage.database import SCHEMA_VERSION, database

ENVELOPE = {"version": 1, "key": "test-operator-key", "directory": "/work/repository"}


def test_new_store_has_the_retired_column(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Create a new store at version nine with the retired participant column."""
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    engine = database(token_hex(32))
    try:
        with engine.begin() as connection:
            assert SCHEMA_VERSION >= 9
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            columns = {column["name"] for column in inspect(connection).get_columns("participant")}
            assert "retired" in columns
    finally:
        engine.dispose()


def test_upgrade_from_version_eight_keeps_existing_participants_active(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    undo_version_nine: Callable[[sqlite3.Connection], None],
) -> None:
    """Existing participants default to active (not retired) and can be retired after upgrade."""
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
        undo_version_nine(previous)
        previous.execute("PRAGMA user_version=8")

    upgraded = database(workspace)
    try:
        with upgraded.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            status = repository.apply(
                connection, models.Status(workspace=workspace, token=created["token"])
            )
            assert status["members"]["Worker"]["state"] == "invited"
            request = actions.RetireParticipant.model_validate(
                {
                    **ENVELOPE,
                    "action": "operator_retire",
                    "conversation": created["conversation"],
                    "name": "Worker",
                }
            )
            result = operator.apply(connection, workspace, request)
            assert result == {"participant": "Worker", "retired": True}
    finally:
        upgraded.dispose()
