"""Migrate every workspace store in the bridge data directory to the current schema version.

The Go core opens only stores at schema version 15 (SCHEMA_VERSION). The Python core migrates
an older store when it opens it. This script opens each store once with the Python code, so
the Go core never meets an older version. Run it through `make migrate-stores`, which stops a
running core first.

The script holds core.lock for the whole run. Every core, Python or Go, takes an flock on that
file before it serves and exits when another process holds it, so no core can open a store
meanwhile. The script deletes nothing: a migration changes a store in place, in one
transaction.

It prints one line per store and exits with status 1 when a core runs or a store cannot be
migrated.
"""

import fcntl
import os
import sqlite3
import sys
from pathlib import Path

from agentcy.storage.database import SCHEMA_VERSION, data_dir, database, workspace_keys


def stored_version(path: Path) -> int:
    """Read a store's schema version without changing the file."""
    with sqlite3.connect(path.as_uri() + "?mode=ro", uri=True) as connection:
        return connection.execute("PRAGMA user_version").fetchone()[0]


def migrate(root: Path) -> bool:
    """Open each workspace store once and print its outcome. Return False on any failure."""
    succeeded = True
    for key in sorted(workspace_keys()):
        name = key + ".sqlite3"
        before = stored_version(root / name)
        if before == SCHEMA_VERSION:
            print(f"Already current: {name} (version {before})")
            continue
        try:
            database(key).dispose()
        except ValueError as error:
            print(f"Not migrated: {name} (version {before}): {error}")
            succeeded = False
            continue
        print(f"Migrated: {name} (version {before} to {SCHEMA_VERSION})")
    database.cache_clear()
    return succeeded


def main() -> int:
    """Hold the core lock, migrate every store, and return the exit status."""
    # Files this script creates, such as the lock and the WAL files, are private to the user.
    os.umask(0o077)
    root = data_dir()
    with (root / "core.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print(f"An agentcy core runs for {root}. Run make stop-core first.", file=sys.stderr)
            return 1
        print(f"Migrating the workspace stores in {root} to version {SCHEMA_VERSION}")
        return 0 if migrate(root) else 1


if __name__ == "__main__":
    sys.exit(main())
