"""Provide an isolated SQLite store for conversation tests."""

from collections.abc import Iterator
from pathlib import Path
from secrets import token_hex

import pytest
from sqlalchemy import Connection

from agentcy.storage.database import database


@pytest.fixture
def conversation_db(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Iterator[Connection]:
    """Open a fresh, private workspace database for one test."""
    directory = tmp_path / "agentcy"
    directory.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(directory))
    workspace = token_hex(32)
    engine = database(workspace)
    try:
        with engine.begin() as connection:
            connection.info["workspace"] = workspace
            yield connection
    finally:
        engine.dispose()
