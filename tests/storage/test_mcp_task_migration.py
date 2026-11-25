"""Open a version-thirteen store and verify that assignments gain the MCP task columns."""

import sqlite3
from collections.abc import Callable
from pathlib import Path
from secrets import token_hex, token_urlsafe

import pytest
from sqlalchemy import inspect, select

from agentcy.conversations import assignments, models, repository
from agentcy.conversations.assignment_tables import assignment
from agentcy.storage.database import SCHEMA_VERSION, database


def test_upgrade_from_version_thirteen_keeps_assignments(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    undo_version_fourteen: Callable[[sqlite3.Connection], None],
) -> None:
    """An existing assignment keeps its state, gets no MCP task, and is not cancelled."""
    monkeypatch.setenv("AGENTCY_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    workspace = token_hex(32)
    original = database(workspace)
    with original.begin() as connection:
        created = repository.apply(
            connection,
            models.Create(workspace=workspace, brief="Work", initiator="Lead", invitees=["Fresh"]),
        )
    original.dispose()
    database.cache_clear()
    with sqlite3.connect(tmp_path / f"{workspace}.sqlite3") as previous:
        undo_version_fourteen(previous)
        slot = previous.execute("SELECT id FROM participant WHERE label = 'Fresh'").fetchone()[0]
        previous.execute(
            "INSERT INTO assignment (id, slot, room, plugin, invite_hash, state, deadline) "
            "VALUES (?, ?, ?, 'fixture', 'hash', 'running', 4102444800)",
            (token_hex(16), slot, created["conversation"]),
        )
        previous.execute("PRAGMA user_version=13")

    upgraded = database(workspace)
    try:
        with upgraded.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            indexes = {
                index["name"]: index for index in inspect(connection).get_indexes("assignment")
            }
            assert indexes["assignment_mcp_task_hash"]["unique"]
            row = connection.execute(select(assignment)).mappings().one()
            assert row["state"] == "running"
            assert row["mcp_task_hash"] is None
            assert row["cancel_requested"] == 0
            with pytest.raises(ValueError, match=assignments.UNKNOWN_MCP_TASK):
                assignments.mcp_task_status(connection, token_urlsafe(32))
    finally:
        upgraded.dispose()
