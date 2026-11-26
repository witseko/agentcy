"""Dump the schema of a workspace store and a token ledger that the Python code creates.

Usage, from the repository root:

    uv run --locked python -I internal/store/testdata/dump_python_schema.py internal/store

The script creates both databases in a fresh temporary AGENTCY_HOME and writes four files
into the given directory: schema.sql and ledger.sql (the DDL that Go replays to create a
store) and testdata/python_master.jsonl and testdata/python_ledger_master.jsonl (every
sqlite_master row as a JSON array of type, name, tbl_name and sql, in rowid order).
"""

import json
import os
import sqlite3
import sys
import tempfile
from pathlib import Path

target = Path(sys.argv[1])
home = Path(tempfile.mkdtemp()) / "home"
home.mkdir(mode=0o700)
os.environ["AGENTCY_HOME"] = str(home)

from agentcy.storage.database import database
from agentcy.tokens import ledger

WORKSPACE = "0" * 64
database(WORKSPACE).dispose()
ledger.ledger_engine(str(ledger.ledger_path())).dispose()


def master_rows(path: Path) -> list[tuple]:
    """Read every sqlite_master row in rowid order."""
    connection = sqlite3.connect(path)
    try:
        return connection.execute(
            "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY rowid"
        ).fetchall()
    finally:
        connection.close()


def replay_script(rows: list[tuple]) -> str:
    """Join the statements that recreate the schema. SQLite makes indexes for constraints
    and the shadow tables of the full-text index itself, so those rows are skipped."""
    statements = [
        sql
        for kind, name, _table, sql in rows
        if sql is not None and not (kind == "table" and name.startswith("conversation_search_"))
    ]
    return "".join(statement + ";\n" for statement in statements)


for path, schema_name, master_name in (
    (home / (WORKSPACE + ".sqlite3"), "schema.sql", "python_master.jsonl"),
    (ledger.ledger_path(), "ledger.sql", "python_ledger_master.jsonl"),
):
    rows = master_rows(path)
    (target / schema_name).write_text(replay_script(rows), encoding="utf-8")
    lines = "".join(json.dumps(list(row), ensure_ascii=False) + "\n" for row in rows)
    (target / "testdata" / master_name).write_text(lines, encoding="utf-8")
