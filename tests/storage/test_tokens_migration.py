"""Open a version-ten store and verify token budget columns and usage table are added."""

import sqlite3
from collections.abc import Callable
from pathlib import Path
from secrets import token_hex

import pytest
from sqlalchemy import inspect

from agents_chat.conversations import models, repository
from agents_chat.storage.database import SCHEMA_VERSION, database
from agents_chat.tokens import ledger


def test_new_store_has_token_schema(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Create a new store at version eleven with model, token_budget, and participant_usage."""
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    engine = database(token_hex(32))
    try:
        with engine.begin() as connection:
            assert SCHEMA_VERSION >= 11
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            columns = {column["name"] for column in inspect(connection).get_columns("participant")}
            assert {"model", "token_budget"} <= columns
            assert "participant_usage" in inspect(connection).get_table_names()
    finally:
        engine.dispose()


def test_upgrade_from_version_ten_enables_token_usage(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    undo_version_eleven: Callable[[sqlite3.Connection], None],
) -> None:
    """Upgrading a v10 store preserves existing conversations and supports report_usage."""
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(tmp_path))
    tmp_path.chmod(0o700)
    ledger.clear_ledger_cache()
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
        undo_version_eleven(previous)
        previous.execute("PRAGMA user_version=10")

    upgraded = database(workspace)
    try:
        with upgraded.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            reported = repository.apply(
                connection,
                models.ReportUsage(
                    workspace=workspace,
                    token=created["token"],
                    input_tokens=1200,
                    output_tokens=300,
                    model="claude-sonnet-4-6",
                ),
            )
            assert reported["usage"]["conversation_tokens"] == 1500
            assert reported["usage"]["source"] == "reported"
    finally:
        upgraded.dispose()
