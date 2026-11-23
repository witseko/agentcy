"""Open a version-twelve store and verify that the conversation title column is added."""

import sqlite3
from collections.abc import Callable
from pathlib import Path
from secrets import token_hex

import pytest

from agents_chat.conversations import models, operator, repository
from agents_chat.conversations import operator_models as actions
from agents_chat.storage.database import SCHEMA_VERSION, database


def test_upgrade_from_version_twelve_enables_rename(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    undo_version_thirteen: Callable[[sqlite3.Connection], None],
) -> None:
    """Upgrading a v12 store keeps the title from the brief and accepts a rename."""
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
        undo_version_thirteen(previous)
        previous.execute("PRAGMA user_version=12")

    envelope = {"version": 1, "key": "test-operator-key", "directory": "/work/repository"}
    upgraded = database(workspace)
    try:
        with upgraded.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            listed = operator.apply(
                connection,
                workspace,
                actions.ListConversations.model_validate({**envelope, "action": "operator_list"}),
            )
            assert [item["title"] for item in listed["conversations"]] == ["Existing brief"]
            renamed = operator.apply(
                connection,
                workspace,
                actions.RenameConversation.model_validate(
                    {
                        **envelope,
                        "action": "operator_rename",
                        "conversation": created["conversation"],
                        "title": "New title",
                    }
                ),
            )
            assert renamed == {"title": "New title"}
    finally:
        upgraded.dispose()
