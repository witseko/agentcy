"""Open a version-eleven store and verify tasks gain a due time without losing old tasks."""

import sqlite3
from collections.abc import Callable
from pathlib import Path
from secrets import token_hex

import pytest

from agentcy.conversations import models, repository
from agentcy.storage.database import SCHEMA_VERSION, database


def test_upgrade_from_version_eleven_keeps_old_tasks_and_accepts_due_times(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    undo_version_twelve: Callable[[sqlite3.Connection], None],
) -> None:
    """A task stored before the upgrade stays open without a due time, and new tasks get one."""
    monkeypatch.setenv("AGENTCY_HOME", str(tmp_path))
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
        undo_version_twelve(previous)
        previous.execute(
            "INSERT INTO message (room, author, body, created, kind, recipient) "
            "SELECT room, 1, 'Old task', 1700000000, 'task', 2 FROM participant WHERE id = 1"
        )
        previous.execute("PRAGMA user_version=11")

    upgraded = database(workspace)
    try:
        with upgraded.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            new_task = repository.apply(
                connection,
                models.Post(
                    workspace=workspace,
                    token=created["token"],
                    submission_id="new-task",
                    body="New task",
                    kind="task",
                    to="Worker",
                    due_minutes=15,
                ),
            )["message_id"]
            status = repository.apply(
                connection, models.Status(workspace=workspace, token=created["token"])
            )
            old, new = status["members"]["Worker"]["open_tasks"]
            assert old == {"id": new_task - 1}
            assert new["id"] == new_task
            assert "due" in new
    finally:
        upgraded.dispose()
