"""Open a version-fourteen store and verify that the message index by kind is added."""

import sqlite3
from collections.abc import Callable
from pathlib import Path
from secrets import token_hex

import pytest

from agentcy.conversations import models, repository
from agentcy.storage.database import SCHEMA_VERSION, database


def test_upgrade_from_version_fourteen_adds_the_index_and_keeps_status(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    undo_version_fifteen: Callable[[sqlite3.Connection], None],
) -> None:
    """Upgrading a v14 store indexes its existing messages and reports the same activity."""
    monkeypatch.setenv("AGENTCY_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    workspace = token_hex(32)
    original = database(workspace)
    with original.begin() as connection:
        team = repository.apply(
            connection,
            models.Create(
                workspace=workspace, brief="Existing brief", initiator="Lead", invitees=["Worker"]
            ),
        )
        repository.apply(
            connection,
            models.Post(
                workspace=workspace, token=team["token"], body="Earlier note", submission_id="one"
            ),
        )
    original.dispose()
    database.cache_clear()
    with sqlite3.connect(tmp_path / f"{workspace}.sqlite3") as previous:
        undo_version_fifteen(previous)
        previous.execute("PRAGMA user_version=14")

    upgraded = database(workspace)
    try:
        with upgraded.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            indexes = connection.exec_driver_sql("PRAGMA index_list(message)").all()
            assert "message_room_kind" in {index[1] for index in indexes}
            status = repository.apply(
                connection, models.Status(workspace=workspace, token=team["token"])
            )
            assert status["members"]["Lead"]["messages"] == 1
    finally:
        upgraded.dispose()
