"""Create a version 15 store with the project's own code and dump the expected search output.

Usage: make_store.py HOME_DIR OUT_DIR
HOME_DIR must not exist yet (it becomes a throwaway AGENTCY_HOME with mode 0700).
OUT_DIR receives expected.json (search rows) and ddl.json (sqlite_master rows) and store.txt.
"""

import json
import os
import random
import sys
from pathlib import Path
from secrets import token_hex

home = Path(sys.argv[1])
out = Path(sys.argv[2])
home.mkdir(mode=0o700)
os.environ["AGENTCY_HOME"] = str(home)
out.mkdir(parents=True, exist_ok=True)

from agentcy.conversations import models, repository, search  # noqa: E402
from agentcy.storage.database import SCHEMA_VERSION, data_dir, database  # noqa: E402

SEARCHES = [
    "retry",
    "retry behavior",
    "naïve café",
    "Zürich Müller",
    "日本語 テスト",
    "привет мир",
    "don't stop",
    "state-of-the-art",
    "C++ templates",
    "foo.bar baz_qux",
    "\"quoted\" phrase OR NOT",
    "the quick brown fox",
    "emoji 🚀 launch",
    "review plan",
    "x",
    "migration schema version",
]
WORDS = (
    "retry behavior review plan migration schema version database latency worker lead cache "
    "naïve café Zürich Müller résumé 日本語 テスト привет мир don't stop state-of-the-art "
    "C++ templates foo.bar baz_qux quoted phrase quick brown fox jumps over lazy dog "
    "launch 🚀 deploy rollback timeout socket daemon shim token budget"
).split()

workspace = token_hex(32)
engine = database(workspace)
random.seed(15)


def apply(connection, action, **fields):
    """Validate a request like the tests do and run it."""
    request = models.REQUEST.validate_python(
        {"action": action, "workspace": workspace, **fields}
    )
    return repository.apply(connection, request)


with engine.begin() as connection:
    team = apply(
        connection,
        "create",
        brief="Review the retry behavior of the café daemon (naïve first pass)",
        initiator="Lead",
        invitees=["Reviewer", "Analyst", "Tester"],
    )
    tokens = {"Lead": team["token"]}
    for name, code in team["invitations"].items():
        tokens[name] = apply(connection, "join", code=code)["token"]
    names = list(tokens)
    for index in range(400):
        author = names[index % len(names)]
        words = [random.choice(WORDS) for _ in range(random.randint(4, 30))]
        body = f"msg {index}: " + " ".join(words)
        apply(
            connection,
            "post",
            token=tokens[author],
            body=body,
            submission_id=f"s{index}",
        )

with engine.begin() as connection:
    version = connection.exec_driver_sql("PRAGMA user_version").scalar_one()
    assert version == SCHEMA_VERSION == 15
    expected = {}
    for query in SEARCHES:
        rows = search.find_matches(connection, query, repository.title_source())
        expected[query] = rows
    ddl = [
        list(row)
        for row in connection.exec_driver_sql(
            "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY rowid"
        )
    ]
(out / "expected.json").write_text(json.dumps(expected, indent=1, ensure_ascii=False))
(out / "ddl.json").write_text(json.dumps(ddl, indent=1, ensure_ascii=False))
(out / "store.txt").write_text(str(data_dir() / (workspace + ".sqlite3")))
(out / "tokens.json").write_text(json.dumps({"workspace": workspace, "tokens": tokens}))
print("hits per query:", {q: len(r) for q, r in expected.items()})
print("store:", data_dir() / (workspace + ".sqlite3"))
