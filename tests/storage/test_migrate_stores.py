"""Verify scripts/migrate_stores.py, which brings every store to the version the Go core opens."""

import fcntl
import os
import sqlite3
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path
from secrets import token_hex

import pytest

from agentcy.conversations import models, repository
from agentcy.storage.database import SCHEMA_VERSION, database

SCRIPT = Path(__file__).resolve().parents[2] / "scripts" / "migrate_stores.py"


def run_script(home: Path) -> subprocess.CompletedProcess[str]:
    """Run the migration script with this interpreter and a private AGENTCY_HOME."""
    return subprocess.run(  # noqa: S603 - this interpreter and a fixed script path, no shell
        [sys.executable, "-I", str(SCRIPT)],
        env={"AGENTCY_HOME": str(home), "PATH": os.defpath},
        capture_output=True,
        check=False,
        text=True,
        timeout=30,
    )


def stored_version(path: Path) -> int:
    """Read the schema version of a store."""
    with sqlite3.connect(path) as connection:
        return connection.execute("PRAGMA user_version").fetchone()[0]


def create_store(workspace: str) -> str:
    """Create a current store with one conversation and return the lead token."""
    engine = database(workspace)
    try:
        with engine.begin() as connection:
            team = repository.apply(
                connection,
                models.Create(
                    workspace=workspace, brief="Existing brief", initiator="Lead", invitees=["W"]
                ),
            )
    finally:
        engine.dispose()
        database.cache_clear()
    return team["token"]


@pytest.fixture
def home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Return a private bridge data directory that this process also uses."""
    directory = tmp_path / "agentcy"
    directory.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(directory))
    return directory


def test_migrates_an_old_store_and_leaves_a_current_store_alone(
    home: Path, undo_version_fifteen: Callable[[sqlite3.Connection], None]
) -> None:
    """A version-14 store reaches version 15 with its data; a current store stays as it was."""
    old, current = sorted([token_hex(32), token_hex(32)])
    old_token = create_store(old)
    create_store(current)
    with sqlite3.connect(home / f"{old}.sqlite3") as previous:
        undo_version_fifteen(previous)
        previous.execute("PRAGMA user_version=14")
    current_bytes = (home / f"{current}.sqlite3").read_bytes()

    result = run_script(home)

    assert result.returncode == 0, result.stderr
    assert f"Migrated: {old}.sqlite3 (version 14 to {SCHEMA_VERSION})" in result.stdout
    assert f"Already current: {current}.sqlite3 (version {SCHEMA_VERSION})" in result.stdout
    assert stored_version(home / f"{old}.sqlite3") == SCHEMA_VERSION
    assert (home / f"{current}.sqlite3").read_bytes() == current_bytes
    engine = database(old)
    try:
        with engine.begin() as connection:
            status = repository.apply(connection, models.Status(workspace=old, token=old_token))
        assert status["members"]["Lead"]["role"] == "lead"
    finally:
        engine.dispose()
        database.cache_clear()


def test_refuses_to_run_while_a_core_holds_the_lock(
    home: Path, undo_version_fifteen: Callable[[sqlite3.Connection], None]
) -> None:
    """With core.lock held, the script changes no store and tells the user to stop the core."""
    workspace = token_hex(32)
    create_store(workspace)
    with sqlite3.connect(home / f"{workspace}.sqlite3") as previous:
        undo_version_fifteen(previous)
        previous.execute("PRAGMA user_version=14")
    with (home / "core.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)

        result = run_script(home)

    assert result.returncode == 1
    assert "make stop-core" in result.stderr
    assert stored_version(home / f"{workspace}.sqlite3") == 14


def test_reports_a_store_from_a_newer_version_and_fails(home: Path) -> None:
    """A store newer than this code is reported, kept, and makes the run fail."""
    workspace = token_hex(32)
    create_store(workspace)
    with sqlite3.connect(home / f"{workspace}.sqlite3") as newer:
        newer.execute(f"PRAGMA user_version={SCHEMA_VERSION + 1}")

    result = run_script(home)

    assert result.returncode == 1
    assert f"Not migrated: {workspace}.sqlite3 (version {SCHEMA_VERSION + 1})" in result.stdout
    assert stored_version(home / f"{workspace}.sqlite3") == SCHEMA_VERSION + 1
