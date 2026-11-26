"""Time the read, insert and search queries with Python sqlite3.

Usage: bench.py STORE N
"""

import re
import sqlite3
import sys
import time

READ_SQL = """SELECT message.id, participant.label, message.body, message.kind, recipient.label AS "to", message.reply_to, message.due_minutes, participant.role AS author_role, message.recipient = ? OR message.recipient IS NULL AND message.kind IS NOT ? AND message.kind IS NOT ? AS wake, blocker_task.id IS NOT NULL AS task_blocker
FROM message JOIN participant ON message.author = participant.id LEFT OUTER JOIN participant AS recipient ON message.recipient = recipient.id LEFT OUTER JOIN message AS blocker_task ON message.kind = ? AND blocker_task.id = message.reply_to AND blocker_task.room = message.room AND blocker_task.kind = ?
WHERE message.room = ? AND message.id > ? AND (message.author != ? OR message.kind IN (?, ?)) ORDER BY message.id
 LIMIT ? OFFSET ?"""
SEARCH_SQL = """SELECT conversation_search.room, coalesce(conversation.title, substr(conversation.brief, ?, ?)) AS title_source, conversation.archived, conversation_search.message_id, participant.label AS author, coalesce(message.created, conversation.created) AS created, snippet(conversation_search, ?, ?, ?, ?, ?) AS snippet
FROM conversation_search JOIN conversation ON conversation.id = conversation_search.room LEFT OUTER JOIN message ON message.id = conversation_search.message_id LEFT OUTER JOIN participant ON participant.id = message.author
WHERE conversation_search MATCH ? ORDER BY conversation_search.rank
 LIMIT ? OFFSET ?"""
QUERIES = [
    "retry", "retry behavior", "naïve café", "Zürich Müller", "日本語 テスト", "привет мир",
    "don't stop", "state-of-the-art", "C++ templates", "foo.bar baz_qux", "the quick brown fox",
    "review plan", "x", "migration schema version", "alpha beta", "cursor page",
]


def expression(query):
    terms = re.findall(r"\w+", query)[:8]
    return " ".join(f'"{t}"*' for t in terms)


def summarize(name, samples):
    samples.sort()
    median = samples[len(samples) // 2] / 1000
    p95 = samples[int(len(samples) * 0.95)] / 1000
    print(f"{name}: median={median:.1f}us p95={p95:.1f}us n={len(samples)}")


store, n = sys.argv[1], int(sys.argv[2])
c = sqlite3.connect(store, isolation_level=None)
c.execute("PRAGMA busy_timeout=5000")
c.execute("PRAGMA foreign_keys=ON")
c.execute("PRAGMA journal_mode=WAL")
room = c.execute("SELECT id FROM conversation LIMIT 1").fetchone()[0]
author = c.execute("SELECT id FROM participant WHERE label='Reviewer'").fetchone()[0]
top = c.execute("SELECT max(id) FROM message").fetchone()[0]
print("python sqlite", sqlite3.sqlite_version, "messages", top)

samples = []
for i in range(n):
    after = (i * 7919) % (top - 20)
    start = time.perf_counter_ns()
    rows = c.execute(READ_SQL, (author, "blocker", "event", "blocker", "task", room, after, author, "event", "proposal", 17, 0)).fetchall()
    samples.append(time.perf_counter_ns() - start)
summarize("read", samples)

samples = []
for i in range(n):
    start = time.perf_counter_ns()
    c.execute("BEGIN IMMEDIATE")
    c.execute("INSERT INTO message (room, author, body, created) VALUES (?, ?, ?, ?)", (room, author, f"py bench insert {i} retry alpha", 1))
    c.execute("COMMIT")
    samples.append(time.perf_counter_ns() - start)
summarize("insert", samples)

samples = []
for i in range(n):
    expr = expression(QUERIES[i % len(QUERIES)])
    start = time.perf_counter_ns()
    rows = c.execute(SEARCH_SQL, (1, 81, 0, "", "", "…", 12, expr, 50, 0)).fetchall()
    samples.append(time.perf_counter_ns() - start)
summarize("search", samples)
