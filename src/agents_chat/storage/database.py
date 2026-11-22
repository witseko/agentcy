"""Open private SQLite stores keyed by the repository's common Git directory."""

import hashlib
import os
import subprocess
from functools import lru_cache
from pathlib import Path

from alembic.migration import MigrationContext
from alembic.operations import Operations
from alembic.operations.ops import CreateTableOp
from sqlalchemy import (
    Column,
    Connection,
    Engine,
    Integer,
    String,
    Table,
    create_engine,
    event,
    text,
)

from agents_chat.conversations.assignment_tables import assignment
from agents_chat.conversations.search_tables import create_search_index
from agents_chat.conversations.tables import SUBMISSION_TARGET, metadata, proposal
from agents_chat.executables import find_on_path
from agents_chat.tokens.tables import participant_usage

SCHEMA_VERSION = 11


def create_table(operations: Operations, table: Table) -> None:
    """Create a table and its declared indexes."""
    operations.invoke(CreateTableOp.from_table(table))
    for index in table.indexes:
        operations.create_index(
            index.name, table.name, list(index.columns.keys()), unique=index.unique
        )


def migrate_sealed_rounds(operations: Operations) -> None:
    """Add private proposals and allow receipts to refer to them until release."""
    operations.add_column("conversation", Column("round", String))
    operations.add_column("conversation", Column("round_deadline", Integer))
    operations.invoke(CreateTableOp.from_table(proposal))
    with operations.batch_alter_table("submission", recreate="always") as batch:
        batch.alter_column("message_id", existing_type=Integer, nullable=True)
        batch.add_column(Column("proposal_id", String))
        batch.create_foreign_key("submission_proposal_fk", "proposal", ["proposal_id"], ["id"])
        batch.create_check_constraint("submission_target", SUBMISSION_TARGET)


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
    git = find_on_path("git")
    if git:
        env = os.environ.copy()
        for variable in ("GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"):
            env.pop(variable, None)
        # Git comes from an absolute PATH entry and receives a fixed argument list.
        # nosemgrep: dangerous-subprocess-use-audit
        common = subprocess.run(  # noqa: S603 - absolute Git path and fixed arguments, no shell
            [git, "-C", str(root), "rev-parse", "--git-common-dir"],
            env=env,
            capture_output=True,
            text=True,
            timeout=5,
            check=False,
        )
        if common.returncode == 0:
            # Git older than 2.31 has no --path-format, and a main checkout
            # answers with a path relative to the directory given to -C.
            root = (root / common.stdout.rstrip("\n")).resolve()
    return hashlib.sha256(os.fsencode(root)).hexdigest()


def database_exists(key: str) -> bool:
    """Check for a workspace store without creating one."""
    return (data_dir() / (key + ".sqlite3")).is_file()


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
        connection.exec_driver_sql("BEGIN IMMEDIATE")
        version = connection.exec_driver_sql("PRAGMA user_version").scalar_one()
        if version == 0:
            operations = Operations(MigrationContext.configure(connection))
            for table in metadata.sorted_tables:
                create_table(operations, table)
            create_search_index(connection, backfill=False)
            connection.exec_driver_sql(f"PRAGMA user_version={SCHEMA_VERSION}")
        elif 1 <= version < SCHEMA_VERSION:
            upgrade(connection, version)
            connection.exec_driver_sql(f"PRAGMA user_version={SCHEMA_VERSION}")
        elif version != SCHEMA_VERSION:
            raise ValueError("Unsupported bridge database version")
    return engine


def upgrade(connection: Connection, version: int) -> None:
    """Apply each schema change that a store at an older version is missing."""
    operations = Operations(MigrationContext.configure(connection))
    if version == 1:
        # The old creator flag already encodes worker (0) and lead (1).
        operations.alter_column("participant", "creator", new_column_name="role")
    if version < 3:
        operations.add_column("participant", Column("seen", Integer))
    if version < 4:
        operations.add_column("message", Column("kind", String))
        connection.exec_driver_sql(
            "ALTER TABLE message ADD COLUMN recipient INTEGER REFERENCES participant(id)"
        )
        connection.exec_driver_sql(
            "ALTER TABLE message ADD COLUMN reply_to INTEGER REFERENCES message(id)"
        )
    if version < 5:
        migrate_sealed_rounds(operations)
    if version < 6:
        create_table(operations, assignment)
    if version < 7:
        operations.add_column("conversation", Column("created", Integer))
    if version < 8:
        operations.add_column(
            "conversation",
            Column("archived", Integer, nullable=False, server_default=text("0")),
        )
        create_search_index(connection, backfill=True)
    if version < 9:
        operations.add_column(
            "participant",
            Column("retired", Integer, nullable=False, server_default=text("0")),
        )
    if version < 10:
        operations.add_column("conversation", Column("open_invite_hash", String))
        operations.create_index(
            "conversation_open_invite_hash",
            "conversation",
            ["open_invite_hash"],
            unique=True,
        )
    if version < 11:
        operations.add_column("participant", Column("model", String))
        operations.add_column("participant", Column("token_budget", Integer))
        create_table(operations, participant_usage)
