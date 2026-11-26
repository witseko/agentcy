"""Copy a store and grow it to a target number of messages with raw SQL (triggers keep FTS current).

Usage: grow_store.py SRC_STORE DST_STORE TARGET_MESSAGES
"""

import random
import shutil
import sqlite3
import sys

src, dst, target = sys.argv[1], sys.argv[2], int(sys.argv[3])
shutil.copyfile(src, dst)
WORDS = (
    "retry behavior review plan migration schema version database latency worker lead cache "
    "naïve café Zürich Müller résumé 日本語 テスト привет мир don't stop state-of-the-art "
    "C++ templates foo.bar baz_qux quoted phrase quick brown fox jumps over lazy dog "
    "launch deploy rollback timeout socket daemon shim token budget alpha beta gamma delta "
    "epsilon zeta theta kappa lambda sigma omega parser lexer index query cursor page"
).split()
random.seed(7)
c = sqlite3.connect(dst, isolation_level=None)
c.execute("PRAGMA journal_mode=WAL")
room = c.execute("SELECT id FROM conversation LIMIT 1").fetchone()[0]
authors = [r[0] for r in c.execute("SELECT id FROM participant WHERE room=?", (room,))]
count = c.execute("SELECT count(*) FROM message").fetchone()[0]
c.execute("BEGIN")
for index in range(count, target):
    body = f"bulk {index}: " + " ".join(random.choice(WORDS) for _ in range(random.randint(4, 30)))
    c.execute(
        "INSERT INTO message (room, author, body, created) VALUES (?, ?, ?, ?)",
        (room, authors[index % len(authors)], body, 1795691300 + index),
    )
c.execute("COMMIT")
c.execute("PRAGMA wal_checkpoint(TRUNCATE)")
print("messages:", c.execute("SELECT count(*) FROM message").fetchone()[0])
print("fts rows:", c.execute("SELECT count(*) FROM conversation_search").fetchone()[0])
print("integrity:", c.execute("PRAGMA integrity_check").fetchone()[0])
