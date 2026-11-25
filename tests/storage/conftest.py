"""Share helpers that turn a current store back into an older schema version."""

import sqlite3
from collections.abc import Callable

import pytest


def remove_version_fourteen(previous: sqlite3.Connection) -> None:
    """Drop the MCP task digest, cancel flag, and timestamp columns added in version fourteen."""
    previous.execute("DROP INDEX assignment_mcp_task_hash")
    for column in ("mcp_task_hash", "cancel_requested", "created", "updated"):
        previous.execute(f"ALTER TABLE assignment DROP COLUMN {column}")


def remove_version_thirteen(previous: sqlite3.Connection) -> None:
    """Drop the version-fourteen schema and the conversation title column of version thirteen."""
    remove_version_fourteen(previous)
    previous.execute("ALTER TABLE conversation DROP COLUMN title")


def remove_version_twelve(previous: sqlite3.Connection) -> None:
    """Drop the version-thirteen column and the task due_minutes column added in version twelve."""
    remove_version_thirteen(previous)
    previous.execute("ALTER TABLE message DROP COLUMN due_minutes")


def remove_version_eleven(previous: sqlite3.Connection) -> None:
    """Drop the newer columns, the participant_usage table, and the token columns."""
    remove_version_twelve(previous)
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
def undo_version_fourteen() -> Callable[[sqlite3.Connection], None]:
    """Return the helper that removes the version-fourteen schema from an open store."""
    return remove_version_fourteen


@pytest.fixture
def undo_version_thirteen() -> Callable[[sqlite3.Connection], None]:
    """Return the helper that removes the version-thirteen schema from an open store."""
    return remove_version_thirteen


@pytest.fixture
def undo_version_twelve() -> Callable[[sqlite3.Connection], None]:
    """Return the helper that removes the version-twelve schema from an open store."""
    return remove_version_twelve


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
