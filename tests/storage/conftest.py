"""Share helpers that turn a current store back into an older schema version."""

import sqlite3
from collections.abc import Callable

import pytest


def remove_version_eleven(previous: sqlite3.Connection) -> None:
    """Drop the participant_usage table and token columns added in version eleven."""
    previous.execute("DROP TABLE participant_usage")
    previous.execute("ALTER TABLE participant DROP COLUMN token_budget")
    previous.execute("ALTER TABLE participant DROP COLUMN model")


def remove_version_ten(previous: sqlite3.Connection) -> None:
    """Drop version-eleven and version-ten schema changes."""
    remove_version_eleven(previous)
    previous.execute("DROP INDEX conversation_open_invite_hash")
    previous.execute("ALTER TABLE conversation DROP COLUMN open_invite_hash")


def remove_version_nine(previous: sqlite3.Connection) -> None:
    """Drop version-ten and version-nine schema changes."""
    remove_version_ten(previous)
    previous.execute("ALTER TABLE participant DROP COLUMN retired")


def remove_version_eight(previous: sqlite3.Connection) -> None:
    """Drop version-ten, version-nine, and version-eight schema changes."""
    remove_version_nine(previous)
    previous.execute("DROP TRIGGER conversation_search_brief")
    previous.execute("DROP TRIGGER conversation_search_message")
    previous.execute("DROP TABLE conversation_search")
    previous.execute("ALTER TABLE conversation DROP COLUMN archived")


@pytest.fixture
def undo_version_eleven() -> Callable[[sqlite3.Connection], None]:
    """Return the helper that removes the version-eleven schema from an open store."""
    return remove_version_eleven


@pytest.fixture
def undo_version_ten() -> Callable[[sqlite3.Connection], None]:
    """Return the helper that removes the version-ten schema from an open store."""
    return remove_version_ten


@pytest.fixture
def undo_version_nine() -> Callable[[sqlite3.Connection], None]:
    """Return the helper that removes the version-nine (and newer) schema from an open store."""
    return remove_version_nine


@pytest.fixture
def undo_version_eight() -> Callable[[sqlite3.Connection], None]:
    """Return the helper that removes the version-eight (and newer) schema from an open store."""
    return remove_version_eight
