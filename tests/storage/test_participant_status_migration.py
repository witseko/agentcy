"""Verify that conversation migrations preserve prior participant and message data."""

import sqlite3
from contextlib import closing
from pathlib import Path
from secrets import token_hex

import pytest

from agents_chat.conversations import models, repository
from agents_chat.storage.database import SCHEMA_VERSION, database


@pytest.mark.parametrize("version", [2, 3, 4])
def test_upgrade_legacy_database(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, version: int
) -> None:
    """Read existing participant status and messages after adding message metadata."""
    directory = tmp_path / "agents-chat"
    directory.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(directory))
    workspace = token_hex(32)
    token = token_hex(32)
    with closing(sqlite3.connect(directory / f"{workspace}.sqlite3")) as previous:
        previous.executescript(
            """
            PRAGMA foreign_keys=ON;
            PRAGMA user_version=2;
            CREATE TABLE conversation (
                id TEXT PRIMARY KEY,
                brief TEXT NOT NULL,
                open INTEGER NOT NULL
            );
            CREATE TABLE participant (
                id INTEGER PRIMARY KEY,
                room TEXT NOT NULL REFERENCES conversation(id),
                label TEXT NOT NULL,
                role INTEGER NOT NULL,
                invite_hash TEXT UNIQUE,
                expires INTEGER,
                token_hash TEXT UNIQUE,
                UNIQUE (room, label)
            );
            CREATE TABLE message (
                id INTEGER PRIMARY KEY,
                room TEXT NOT NULL REFERENCES conversation(id),
                author INTEGER NOT NULL REFERENCES participant(id),
                body TEXT NOT NULL,
                created INTEGER NOT NULL
            );
            CREATE TABLE submission (
                author INTEGER REFERENCES participant(id),
                key TEXT,
                message_id INTEGER NOT NULL REFERENCES message(id),
                PRIMARY KEY (author, key)
            );
            INSERT INTO conversation VALUES ('room-1', 'Old brief', 1);
            """
        )
        previous.execute(
            "INSERT INTO participant (id, room, label, role, token_hash) VALUES (?, ?, ?, ?, ?)",
            (1, "room-1", "Lead", 1, repository.digest(token)),
        )
        previous.execute(
            "INSERT INTO message VALUES (?, ?, ?, ?, ?)",
            (1, "room-1", 1, "Old reply", 1),
        )
        previous.execute("INSERT INTO submission VALUES (1, 'old-key', 1)")
        if version >= 3:
            previous.execute("ALTER TABLE participant ADD COLUMN seen INTEGER")
            previous.execute("PRAGMA user_version=3")
        if version == 4:
            previous.execute("ALTER TABLE message ADD COLUMN kind TEXT")
            previous.execute(
                "ALTER TABLE message ADD COLUMN recipient INTEGER REFERENCES participant(id)"
            )
            previous.execute(
                "ALTER TABLE message ADD COLUMN reply_to INTEGER REFERENCES message(id)"
            )
            previous.execute("PRAGMA user_version=4")
        previous.commit()

    engine = database(workspace)
    try:
        with engine.begin() as connection:
            assert connection.exec_driver_sql("PRAGMA user_version").scalar_one() == SCHEMA_VERSION
            result = repository.apply(connection, models.Read(workspace=workspace, token=token))
            assert result["brief"] == "Old brief"
            assert result["messages"] == [{"id": 1, "label": "Lead", "body": "Old reply"}]
            status = repository.apply(connection, models.Status(workspace=workspace, token=token))
            assert status["members"]["Lead"]["messages"] == 1
            replay = repository.apply(
                connection,
                models.Post(
                    workspace=workspace, token=token, body="Old reply", submission_id="old-key"
                ),
            )
            assert replay == {"message_id": 1}
    finally:
        engine.dispose()
