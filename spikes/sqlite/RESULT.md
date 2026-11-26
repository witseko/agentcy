# SQLite driver spike: modernc.org/sqlite for the Go port

Verdict: **modernc.org/sqlite passes all criteria (a to g). Recommendation: modernc.** The ncruces driver was not tried because no criterion failed (task rule 4).

## Setup

- Driver: `modernc.org/sqlite v1.63.0`, built with `CGO_ENABLED=0` (confirmed with `go version -m`: `build CGO_ENABLED=0`). Go 1.27.4, darwin/arm64.
- Python: the project venv (Python 3.14.8, SQLAlchemy through the project's own `agentcy` code).
- Store: created by `make_store.py` with the project's own repository API in a fresh throwaway `AGENTCY_HOME` (1 conversation, 4 participants, 400 posted messages with non-ASCII, punctuation and multi-word text). `PRAGMA user_version` = 15.
- Files: `make_store.py` (store + expected search JSON + DDL dump), `main.go`/`verify.go` (checks a to g), `roundtrip.py` (check f, Python side), `grow_store.py` + `bench.py` + `bench.go` (speed), `repro.go` (FTS integrity bisect).

## SQLite versions

| Build | `select sqlite_version()` |
| --- | --- |
| Go modernc v1.63.0 | 3.54.1 |
| Python 3.14.8 `sqlite3` (project venv) | 3.53.1 |

The versions differ by one minor release. Search rows, order and snippets were identical, and files written by either side are accepted by the other. The risk is low but real: FTS5 ranking (bm25) or tokenizer tables could change in a later release and alter tie order. Python and Go will both write the same file while old Python plugins and the Go daemon coexist, so keep search parity in a test.

## Verdicts

| Check | Result | Evidence |
| --- | --- | --- |
| a. open, PRAGMAs, version | PASS | `user_version=15`; on 4 distinct pooled connections: `journal_mode=wal foreign_keys=1 busy_timeout=5000` (all set through the DSN) |
| b. search parity | PASS | 16 of 16 search strings identical to the Python JSON: 481 rows, same order, same `snippet()` text, same `created`, `author`, `title_source` (includes Cyrillic, CJK, accented Latin, `C++`, `don't`, `foo.bar baz_qux`, quote/OR/NOT text, emoji, a 1-letter term) |
| c. trigger fires in Go | PASS | Go `INSERT INTO message` -> `conversation_search MATCH` returns exactly that `message_id` with snippet `fresh gotriggerprobe ünïcode from go` |
| d. BEGIN IMMEDIATE and per-connection PRAGMAs | PASS | `_txlock=immediate` in the DSN: after `db.BeginTx` a rival `BEGIN IMMEDIATE` (busy_timeout=0) gets `SQLITE_BUSY`, so the write lock is taken at begin. Explicit `BEGIN IMMEDIATE` on a pinned `*sql.Conn` also works. A plain-DSN read transaction does not block writers. PRAGMAs via `_pragma=` apply to every new connection (see a) |
| e. cross-process write wait | PASS | Python `sqlite3` holds `BEGIN IMMEDIATE` for 2 s: Go write waited 2.09 s and succeeded. Second Go process holding 2 s: Go waited 2.08 s and succeeded. Holder of 7 s: Go got `database is locked (5) (SQLITE_BUSY)` after 5.12 s (the 5 s timeout works, no hang) |
| f. Python round trip, integrity | PASS | After Go wrote 5 rows (incl. FTS trigger rows), `roundtrip.py` with the project's `database()`, `repository.apply` read and post, and `search.find_matches` all worked and found the Go rows; `PRAGMA integrity_check` = ok in both Python and (fresh-connection) Go; `foreign_key_check` empty |
| g. DDL replay | PASS | Replaying the dumped `sqlite_master` SQL (17 statements; FTS5 shadow tables and autoindexes are created by SQLite itself and must be skipped) gives 31 `sqlite_master` rows, identical to the Python store (type, name, tbl_name, sql); `integrity_check` ok; trigger insert and search work on the new store. Embedding the v15 DDL is a sound way to create new stores; set `PRAGMA user_version=15` after |

### Pitfalls found

1. **FTS5 false alarm in `PRAGMA integrity_check`.** On a long-lived connection, after another connection has written, `PRAGMA integrity_check` returns `fts5: checksum mismatch for table "conversation_search"`. A fresh connection says `ok`. Python's own `sqlite3` (3.53.1) behaves the same (reproduced with two plain connections), so this is SQLite, not modernc. Run integrity checks on a fresh connection only.
2. **Paths with `#` or `?` silently open the wrong file** when the DSN is built as `"file:" + path`: SQLite parsed `w#4/p.sqlite3` as file `w`, created an empty database and the queries failed with `no such table`. Build the DSN with `net/url` (recipe below); with it, paths containing space, `#`, `%` and `?` all open the correct store.
3. `_txlock=immediate` applies to every `BeginTx`, including read-only ones. Use two pools (see recipe), or the readers will queue behind writers.

## Timing (100,000 messages, 1,000 runs each, same machine, load average about 5 from other agents)

Same queries: the repository read page query (messages after a cursor, `LIMIT 17`, cursors spread over the whole table), an insert in its own `BEGIN IMMEDIATE` ... `COMMIT` (FTS trigger fires), and the search query (16 rotating strings). Both sides use unprepared statements and read every column. Two runs each; the second run agrees within about 5%.

| Query | Go modernc median | Go modernc p95 | Python sqlite3 median | Python sqlite3 p95 | Go/Python (median) |
| --- | --- | --- | --- | --- | --- |
| incremental read | 182 us | 486 us | 145 us | 372 us | 1.26x |
| insert (own tx) | 137 us | 275 us | 106 us | 196 us | 1.29x |
| search | 15.3 ms | 49.6 ms | 7.8 ms | 26.8 ms | 1.95x |

Second run: read 192/432 us vs 150/413 us, insert 141/278 us vs 116/216 us, search 15.1/49.7 ms vs 8.2/27.6 ms (median/p95). Summary: modernc is 1.3x slower on small queries and about 2x slower on FTS search. No order-of-magnitude problem. The saving of the port comes from removing per-chat interpreters and extra work per call, not from faster SQL. Search at 100k messages costs 8 to 15 ms either way, so it deserves a cheap path (it is rare).

## Recipe for the port

DSN (build it with `net/url`, never string concatenation):

```go
func dsn(path string, immediate bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	if immediate {
		q.Set("_txlock", "immediate")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}
db, err := sql.Open("sqlite", dsn(path, false)) // import _ "modernc.org/sqlite"
```

Example result: `file:/home/u/.agentcy/<key>.sqlite3?_pragma=busy_timeout%285000%29&_pragma=foreign_keys%281%29&_pragma=journal_mode%28WAL%29&_txlock=immediate`

Transactions:

- **Write pool**: `sql.Open("sqlite", dsn(path, true))` with `SetMaxOpenConns(1)`; every `db.BeginTx(ctx, nil)` runs `BEGIN IMMEDIATE`, waits up to 5 s on another process, and returns `database is locked (5) (SQLITE_BUSY)` after that. Commit with `tx.Commit()`.
- **Read pool**: a second `sql.DB` from `dsn(path, false)` for plain queries and read transactions; they never block writers in WAL.
- Fallback if one pool is wanted: pin `conn, _ := db.Conn(ctx)`, `conn.ExecContext(ctx, "BEGIN IMMEDIATE")`, work, `COMMIT`/`ROLLBACK`, `conn.Close()`. Verified to work.
- Treat any error whose text contains `locked` or `busy` as the busy case (modernc gives `database is locked (5) (SQLITE_BUSY)`).
- Open an existing store with the same PRAGMAs as Python. The Python `database()` also runs `PRAGMA journal_mode=WAL` and checks `user_version`; the DSN already does the former. Read `PRAGMA user_version` and refuse values other than 15.
- New stores: replay the v15 DDL (all `sqlite_master` rows with non-NULL SQL, skipping `conversation_search_*` shadow tables), then `PRAGMA user_version=15`.
- FTS match input: port `\w+` as letter, number (Nd/Nl/No) or `_`; Go's `unicode.IsLetter || unicode.IsNumber || '_'` matched Python on all 16 test strings. Combining marks are not `\w` in Python and are not in this rule either, but add a few more test strings (Mn marks, Arabic digits) before relying on it.

## Recommendation

**modernc.org/sqlite v1.63.0.** It is CGO-free, opens and writes unchanged v15 stores, keeps FTS5 results byte-identical to Python for the search query, supports `_txlock`, per-connection PRAGMAs and the 5 s busy wait across processes, and costs 1.3x to 2x on this machine. ncruces is not needed.

## Reproduce

```
AGENTCY_HOME is created fresh by the scripts; the user's real store is never touched.
.venv/bin/python spikes/sqlite/make_store.py HOME OUT          # store, expected.json, ddl.json
cd spikes/sqlite && CGO_ENABLED=0 go build -o SPIKE .
SPIKE -driver modernc verify -store $(cat OUT/store.txt) -expected OUT/expected.json -ddl OUT/ddl.json -work WORK
.venv/bin/python spikes/sqlite/roundtrip.py WORK OUT/tokens.json
.venv/bin/python spikes/sqlite/grow_store.py STORE BIG 100000
SPIKE -driver modernc bench -store BIG -n 1000 ; .venv/bin/python spikes/sqlite/bench.py BIG2 1000
```
