"""Share helpers that turn a current store back into an older schema version."""

import sqlite3
from collections.abc import Callable

import pytest


def remove_version_eight(previous: sqlite3.Connection) -> None:
    """Drop the search index, its triggers and the archived column added in version eight."""
    previous.execute("DROP TRIGGER conversation_search_brief")
    previous.execute("DROP TRIGGER conversation_search_message")
    previous.execute("DROP TABLE conversation_search")
    previous.execute("ALTER TABLE conversation DROP COLUMN archived")


@pytest.fixture
def undo_version_eight() -> Callable[[sqlite3.Connection], None]:
    """Return the helper that removes the version-eight schema from an open store."""
    return remove_version_eight
