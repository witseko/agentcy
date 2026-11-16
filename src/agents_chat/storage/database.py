"""Open private SQLite stores keyed by the repository's common Git directory."""

import hashlib
import os
import shutil
import subprocess
from functools import lru_cache
from pathlib import Path

from alembic.migration import MigrationContext
from alembic.operations import Operations
from alembic.operations.ops import CreateTableOp
from sqlalchemy import Engine, create_engine, event

from agents_chat.conversations.tables import metadata


def data_dir() -> Path:
    """Keep the socket and databases inside a directory accessible only to the user."""
    path = Path(os.getenv("AGENTS_CHAT_HOME", Path.home() / ".agents-chat"))
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    if path.is_symlink() or path.stat().st_uid != os.getuid() or path.stat().st_mode & 0o077:
        raise ValueError("Bridge data directory must belong to you and have mode 0700")
    return path.resolve()


def workspace_key(workspace: Path) -> str:
    """Resolve linked worktrees to a single storage identifier."""
    root = workspace.resolve(strict=True)
    if not root.is_dir():
        raise ValueError("Workspace must be a directory")
    git = shutil.which("git")
    if git:
        common = subprocess.run(  # noqa: S603 - fixed git arguments, no shell
            [git, "-C", str(root), "rev-parse", "--path-format=absolute", "--git-common-dir"],
            capture_output=True,
            text=True,
            timeout=5,
            check=False,
        )
        if common.returncode == 0:
            root = Path(common.stdout.rstrip("\n")).resolve()
    return hashlib.sha256(os.fsencode(root)).hexdigest()


@lru_cache(maxsize=64)
def database(key: str) -> Engine:
    """Open a versioned store. The daemon owns all schema changes."""
    engine = create_engine("sqlite:///" + str(data_dir() / (key + ".sqlite3")))

    @event.listens_for(engine, "connect")
    def configure(connection, _record):
        """Apply SQLite settings to each newly opened connection."""
        connection.execute("PRAGMA foreign_keys=ON")
        connection.execute("PRAGMA busy_timeout=5000")

    with engine.begin() as connection:
        connection.exec_driver_sql("PRAGMA journal_mode=WAL")
        version = connection.exec_driver_sql("PRAGMA user_version").scalar_one()
        if version == 0:
            # Start an actual SQLite transaction before the first DDL statement.
            connection.exec_driver_sql("BEGIN IMMEDIATE")
            operations = Operations(MigrationContext.configure(connection))
            for table in metadata.sorted_tables:
                operations.invoke(CreateTableOp.from_table(table))
                for index in table.indexes:
                    operations.create_index(index.name, table.name, list(index.columns.keys()))
            connection.exec_driver_sql("PRAGMA user_version=1")
        elif version != 1:
            raise ValueError("Unsupported bridge database version")
    return engine
