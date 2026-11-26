"""Open a store that Go wrote to with the project's own code, then read, post and search.

Usage: roundtrip.py HOME_DIR TOKENS_JSON
HOME_DIR is an AGENTCY_HOME (mode 0700) that holds the store named <workspace>.sqlite3.
"""

import json
import os
import sqlite3
import sys

os.environ["AGENTCY_HOME"] = sys.argv[1]
tokens = json.load(open(sys.argv[2]))
workspace = tokens["workspace"]

from agentcy.conversations import models, repository, search  # noqa: E402
from agentcy.storage.database import data_dir, database  # noqa: E402

engine = database(workspace)
ok = True
with engine.begin() as connection:
    def call(action, **fields):
        request = models.REQUEST.validate_python(
            {"action": action, "workspace": workspace, "token": tokens["tokens"]["Reviewer"], **fields}
        )
        return repository.apply(connection, request)

    page = call("read", after=395)
    ids = [m["id"] for m in page["messages"]]
    print("read after 395 ->", ids)
    ok &= bool(ids)
    posted = call("post", body="python after go roundtrip marker", submission_id="rt1")
    print("post ->", posted)
    for query in ("gotriggerprobe", "go waited row", "roundtrip marker"):
        hits = search.find_matches(connection, query, repository.title_source())
        print(f"search {query!r} -> {len(hits)} hits, first snippet: {hits[0]['snippet'] if hits else None!r}")
        ok &= bool(hits)
engine.dispose()
path = data_dir() / (workspace + ".sqlite3")
check = sqlite3.connect(path).execute("PRAGMA integrity_check").fetchall()
print("python integrity_check:", check)
ok &= check == [("ok",)]
print("python sqlite", sqlite3.sqlite_version)
print("ROUNDTRIP", "PASS" if ok else "FAIL")
sys.exit(0 if ok else 1)
