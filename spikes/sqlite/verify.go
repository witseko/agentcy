package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
)

// result records one pass or fail with evidence.
type result struct {
	name     string
	ok       bool
	evidence string
}

var results []result

func report(name string, ok bool, format string, args ...any) {
	r := result{name, ok, fmt.Sprintf(format, args...)}
	results = append(results, r)
	status := "PASS"
	if !ok {
		status = "FAIL"
	}
	fmt.Printf("[%s] %s: %s\n", status, name, r.evidence)
}

// hit is one search row in the same shape as the Python dump.
type hit struct {
	Room        string  `json:"room"`
	TitleSource string  `json:"title_source"`
	Archived    int64   `json:"archived"`
	MessageID   *int64  `json:"message_id"`
	Author      *string `json:"author"`
	Created     int64   `json:"created"`
	Snippet     string  `json:"snippet"`
}

const searchSQL = `SELECT conversation_search.room, coalesce(conversation.title, substr(conversation.brief, ?, ?)) AS title_source, conversation.archived, conversation_search.message_id, participant.label AS author, coalesce(message.created, conversation.created) AS created, snippet(conversation_search, ?, ?, ?, ?, ?) AS snippet
FROM conversation_search JOIN conversation ON conversation.id = conversation_search.room LEFT OUTER JOIN message ON message.id = conversation_search.message_id LEFT OUTER JOIN participant ON participant.id = message.author
WHERE conversation_search MATCH ? ORDER BY conversation_search.rank
 LIMIT ? OFFSET ?`

// matchExpression mirrors search.match_expression: Unicode word runs, at most 8, each quoted prefix.
func matchExpression(query string) string {
	var terms []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			terms = append(terms, string(current))
			current = nil
		}
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
			current = append(current, r)
		} else {
			flush()
		}
	}
	flush()
	if len(terms) > 8 {
		terms = terms[:8]
	}
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = `"` + t + `"*`
	}
	return strings.Join(parts, " ")
}

func runSearch(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string) ([]hit, error) {
	expr := matchExpression(query)
	if expr == "" {
		return nil, fmt.Errorf("no terms")
	}
	rows, err := q.QueryContext(ctx, searchSQL, 1, 81, 0, "", "", "…", 12, expr, 50, 0)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hit{}
	for rows.Next() {
		var h hit
		var mid sql.NullInt64
		var author sql.NullString
		if err := rows.Scan(&h.Room, &h.TitleSource, &h.Archived, &mid, &author, &h.Created, &h.Snippet); err != nil {
			return nil, err
		}
		if mid.Valid {
			h.MessageID = &mid.Int64
		}
		if author.Valid {
			h.Author = &author.String
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func pragmaString(ctx context.Context, c *sql.Conn, name string) (string, error) {
	var v string
	err := c.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v)
	return v, err
}

func verify(driver, store, expectedPath, ddlPath, work string) error {
	ctx := context.Background()
	// The work directory is a fresh AGENTCY_HOME (mode 0700) holding a copy of the Python store.
	if err := os.MkdirAll(work, 0o700); err != nil {
		return err
	}
	copyPath := filepath.Join(work, filepath.Base(store))
	if err := copyFile(store, copyPath); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(store + suffix); err == nil {
			_ = copyFile(store+suffix, copyPath+suffix)
		}
	}

	db, err := open(driver, copyPath, "")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(4)

	// a. open, user_version, journal_mode, per-connection PRAGMAs on 4 distinct connections.
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		report("a open+pragmas", false, "user_version: %v", err)
	} else {
		var libVersion string
		_ = db.QueryRowContext(ctx, "select sqlite_version()").Scan(&libVersion)
		fmt.Println("sqlite_version() in Go build:", libVersion)
		var conns []*sql.Conn
		allOK := true
		var details []string
		for i := 0; i < 4; i++ {
			c, err := db.Conn(ctx)
			if err != nil {
				allOK = false
				details = append(details, err.Error())
				break
			}
			conns = append(conns, c)
			jm, _ := pragmaString(ctx, c, "journal_mode")
			fk, _ := pragmaString(ctx, c, "foreign_keys")
			bt, _ := pragmaString(ctx, c, "busy_timeout")
			details = append(details, fmt.Sprintf("conn%d journal_mode=%s foreign_keys=%s busy_timeout=%s", i, jm, fk, bt))
			if jm != "wal" || fk != "1" || bt != "5000" {
				allOK = false
			}
		}
		for _, c := range conns {
			c.Close()
		}
		report("a open+pragmas", allOK && version == 15, "user_version=%d; %s", version, strings.Join(details, "; "))
	}

	// b. search parity with the Python dump.
	raw, err := os.ReadFile(expectedPath)
	if err != nil {
		return err
	}
	var expected map[string][]hit
	if err := json.Unmarshal(raw, &expected); err != nil {
		return err
	}
	queries := make([]string, 0, len(expected))
	for q := range expected {
		queries = append(queries, q)
	}
	sort.Strings(queries)
	identical, total, rowsCompared := 0, 0, 0
	var firstDiff string
	for _, q := range queries {
		total++
		got, err := runSearch(ctx, db, q)
		if err != nil {
			if firstDiff == "" {
				firstDiff = fmt.Sprintf("%q: %v", q, err)
			}
			continue
		}
		want := expected[q]
		if want == nil {
			want = []hit{}
		}
		rowsCompared += len(want)
		if reflect.DeepEqual(got, want) {
			identical++
			continue
		}
		if firstDiff == "" {
			for i := 0; i < len(got) && i < len(want); i++ {
				if !reflect.DeepEqual(got[i], want[i]) {
					g, _ := json.Marshal(got[i])
					w, _ := json.Marshal(want[i])
					firstDiff = fmt.Sprintf("%q row %d go=%s py=%s", q, i, g, w)
					break
				}
			}
			if firstDiff == "" {
				firstDiff = fmt.Sprintf("%q: row counts go=%d py=%d", q, len(got), len(want))
			}
		}
	}
	report("b search parity", identical == total, "%d of %d search strings identical (%d rows, order and snippets) %s", identical, total, rowsCompared, firstDiff)

	// c. insert a message in Go and find it through the trigger-maintained index.
	var room string
	var author int64
	if err := db.QueryRowContext(ctx, "SELECT room, id FROM participant WHERE label='Reviewer'").Scan(&room, &author); err != nil {
		return err
	}
	needle := "gotriggerprobe ünïcode"
	res, err := db.ExecContext(ctx, "INSERT INTO message (room, author, body, created) VALUES (?, ?, ?, ?)", room, author, "fresh "+needle+" from go", time.Now().Unix())
	if err != nil {
		report("c trigger fires in Go", false, "insert: %v", err)
	} else {
		id, _ := res.LastInsertId()
		hits, err := runSearch(ctx, db, "gotriggerprobe ünïcode")
		found := err == nil && len(hits) == 1 && hits[0].MessageID != nil && *hits[0].MessageID == id
		snippet := ""
		if len(hits) > 0 {
			snippet = hits[0].Snippet
		}
		report("c trigger fires in Go", found, "inserted message_id=%d, search returned %d hit(s), snippet=%q, err=%v", id, len(hits), snippet, err)
	}

	// d. BEGIN IMMEDIATE via DSN _txlock=immediate and via explicit statement on a pinned connection.
	checkImmediate(ctx, driver, copyPath)

	// e. a second process holds a write transaction for 2 s while Go writes.
	checkContention(ctx, driver, copyPath, room, author)

	// f. integrity, then the Python round trip is run by the caller (roundtrip.py).
	// Use a fresh pool: SQLite's FTS5 integrity check reports a false "checksum mismatch" on a
	// long-lived connection after another connection has written (Python's sqlite3 does the same).
	fresh, err := open(driver, copyPath, "")
	if err != nil {
		return err
	}
	var integrity string
	if err := fresh.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		report("f1 integrity_check on fresh connection", false, "%v", err)
	} else {
		report("f1 integrity_check on fresh connection", integrity == "ok", "%s", integrity)
	}
	var stale string
	_ = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&stale)
	fmt.Printf("note: integrity_check on the long-lived pool that predates other writers says %q\n", stale)
	fresh.Close()
	var fkViolations int
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err == nil {
		for rows.Next() {
			fkViolations++
		}
		rows.Close()
	}
	report("f2 foreign_key_check", fkViolations == 0, "%d violations", fkViolations)
	db.Close()

	// g. create a new store from the dumped DDL.
	checkDDL(ctx, driver, work, ddlPath)

	fmt.Println("\nSUMMARY")
	for _, r := range results {
		s := "PASS"
		if !r.ok {
			s = "FAIL"
		}
		fmt.Printf("%s  %s\n", s, r.name)
	}
	return nil
}

func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o600)
}

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "locked") || strings.Contains(s, "busy")
}

func checkImmediate(ctx context.Context, driver, path string) {
	// Variant 1: DSN _txlock=immediate, then db.BeginTx takes the write lock at once.
	imm, err := open(driver, path, "immediate")
	if err != nil {
		report("d1 DSN _txlock=immediate", false, "%v", err)
		return
	}
	defer imm.Close()
	probe, err := open(driver, path, "")
	if err != nil {
		report("d1 DSN _txlock=immediate", false, "%v", err)
		return
	}
	defer probe.Close()
	tx, err := imm.BeginTx(ctx, nil)
	if err != nil {
		report("d1 DSN _txlock=immediate", false, "begin: %v", err)
		return
	}
	// A second connection with busy_timeout=0 must now fail to start a write transaction.
	pc, _ := probe.Conn(ctx)
	_, _ = pc.ExecContext(ctx, "PRAGMA busy_timeout=0")
	_, perr := pc.ExecContext(ctx, "BEGIN IMMEDIATE")
	if perr == nil {
		_, _ = pc.ExecContext(ctx, "ROLLBACK")
	}
	pc.Close()
	_ = tx.Rollback()
	report("d1 DSN _txlock=immediate", isBusy(perr), "after db.BeginTx a rival BEGIN IMMEDIATE got: %v", perr)

	// Variant 2: explicit BEGIN IMMEDIATE on a pinned connection from a plain DSN.
	c, err := probe.Conn(ctx)
	if err != nil {
		report("d2 explicit BEGIN IMMEDIATE on pinned conn", false, "%v", err)
		return
	}
	_, berr := c.ExecContext(ctx, "BEGIN IMMEDIATE")
	rival, _ := imm.Conn(ctx)
	_, _ = rival.ExecContext(ctx, "PRAGMA busy_timeout=0")
	_, rerr := rival.ExecContext(ctx, "BEGIN IMMEDIATE")
	rival.Close()
	_, cerr := c.ExecContext(ctx, "COMMIT")
	c.Close()
	report("d2 explicit BEGIN IMMEDIATE on pinned conn", berr == nil && isBusy(rerr) && cerr == nil, "begin=%v rival=%v commit=%v", berr, rerr, cerr)

	// Variant 3: a read-only transaction under the plain DSN must not take the write lock.
	rtx, err := probe.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		report("d3 plain DSN read tx leaves writers free", false, "begin: %v", err)
		return
	}
	var cnt int
	_ = rtx.QueryRowContext(ctx, "SELECT count(*) FROM message").Scan(&cnt)
	w, _ := imm.Conn(ctx)
	_, _ = w.ExecContext(ctx, "PRAGMA busy_timeout=0")
	_, werr := w.ExecContext(ctx, "BEGIN IMMEDIATE")
	if werr == nil {
		_, _ = w.ExecContext(ctx, "ROLLBACK")
	}
	w.Close()
	_ = rtx.Rollback()
	report("d3 plain DSN read tx leaves writers free", werr == nil, "read tx open, count=%d, rival write begin err=%v", cnt, werr)
}

const holderPython = `
import sqlite3, sys, time
c = sqlite3.connect(sys.argv[1], isolation_level=None, timeout=5)
c.execute("BEGIN IMMEDIATE")
c.execute("INSERT INTO message (room, author, body, created) VALUES (?, ?, 'python holder row', 1)", (sys.argv[2], int(sys.argv[3])))
print("locked", flush=True)
time.sleep(float(sys.argv[4]))
c.execute("COMMIT")
`

func checkContention(ctx context.Context, driver, path, room string, author int64) {
	w, err := open(driver, path, "immediate")
	if err != nil {
		report("e contention", false, "%v", err)
		return
	}
	defer w.Close()

	runOne := func(label string, cmd *exec.Cmd) {
		stdout, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			report(label, false, "start holder: %v", err)
			return
		}
		buf := make([]byte, 16)
		stdout.Read(buf) // wait for "locked"
		start := time.Now()
		tx, err := w.BeginTx(ctx, nil)
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO message (room, author, body, created) VALUES (?, ?, 'go waited row', 2)", room, author)
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		elapsed := time.Since(start)
		_ = cmd.Wait()
		report(label, err == nil && elapsed >= 1500*time.Millisecond && elapsed < 5*time.Second, "Go write waited %.2fs, error=%v", elapsed.Seconds(), err)
	}
	py := exec.Command("python3", "-I", "-c", holderPython, path, room, fmt.Sprint(author), "2")
	runOne("e1 contention vs Python sqlite3 holder (2s)", py)
	self, _ := os.Executable()
	g := exec.Command(self, "-driver", driver, "hold", "-store", path, "-seconds", "2")
	runOne("e2 contention vs second Go process holder (2s)", g)

	// A holder longer than the busy timeout must surface a busy error, not hang.
	py = exec.Command("python3", "-I", "-c", holderPython, path, room, fmt.Sprint(author), "7")
	stdout, _ := py.StdoutPipe()
	_ = py.Start()
	buf := make([]byte, 16)
	stdout.Read(buf)
	start := time.Now()
	tx, err := w.BeginTx(ctx, nil)
	if err == nil {
		_ = tx.Rollback()
	}
	elapsed := time.Since(start)
	_ = py.Wait()
	report("e3 holder longer than timeout gives busy error", isBusy(err) && elapsed >= 4500*time.Millisecond && elapsed < 6500*time.Millisecond, "after %.2fs err=%v", elapsed.Seconds(), err)
}

func hold(driver, store string, seconds float64) error {
	ctx := context.Background()
	db, err := open(driver, store, "immediate")
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var room string
	var author int64
	if err := tx.QueryRowContext(ctx, "SELECT room, id FROM participant LIMIT 1").Scan(&room, &author); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO message (room, author, body, created) VALUES (?, ?, 'go holder row', 1)", room, author); err != nil {
		return err
	}
	fmt.Println("locked")
	time.Sleep(time.Duration(seconds * float64(time.Second)))
	return tx.Commit()
}

type masterRow [4]*string

func readMaster(ctx context.Context, db *sql.DB) ([]masterRow, error) {
	rows, err := db.QueryContext(ctx, "SELECT type, name, tbl_name, sql FROM sqlite_master")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []masterRow
	for rows.Next() {
		var a, b, c string
		var d sql.NullString
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			return nil, err
		}
		row := masterRow{&a, &b, &c, nil}
		if d.Valid {
			s := d.String
			row[3] = &s
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func key(r masterRow) string {
	sqlText := "<NULL>"
	if r[3] != nil {
		sqlText = *r[3]
	}
	return *r[0] + "|" + *r[1] + "|" + *r[2] + "|" + sqlText
}

func checkDDL(ctx context.Context, driver, work, ddlPath string) {
	raw, err := os.ReadFile(ddlPath)
	if err != nil {
		report("g DDL replay", false, "%v", err)
		return
	}
	var dump [][]*string
	if err := json.Unmarshal(raw, &dump); err != nil {
		report("g DDL replay", false, "%v", err)
		return
	}
	fresh := filepath.Join(work, "fresh-from-ddl.sqlite3")
	_ = os.Remove(fresh)
	db, err := open(driver, fresh, "")
	if err != nil {
		report("g DDL replay", false, "%v", err)
		return
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	applied := 0
	for _, row := range dump {
		typ, name, stmt := *row[0], *row[1], row[3]
		if stmt == nil {
			continue // automatic indexes appear by themselves
		}
		if typ == "table" && strings.HasPrefix(name, "conversation_search_") {
			continue // FTS5 shadow tables appear with CREATE VIRTUAL TABLE
		}
		if _, err := db.ExecContext(ctx, *stmt); err != nil {
			report("g DDL replay", false, "statement for %s %s failed: %v", typ, name, err)
			return
		}
		applied++
	}
	if _, err := db.ExecContext(ctx, "PRAGMA user_version=15"); err != nil {
		report("g DDL replay", false, "%v", err)
		return
	}
	got, err := readMaster(ctx, db)
	if err != nil {
		report("g DDL replay", false, "%v", err)
		return
	}
	var want []masterRow
	for _, row := range dump {
		want = append(want, masterRow{row[0], row[1], row[2], row[3]})
	}
	gk, wk := map[string]bool{}, map[string]bool{}
	for _, r := range got {
		gk[key(r)] = true
	}
	for _, r := range want {
		wk[key(r)] = true
	}
	var onlyGo, onlyPy []string
	for k := range gk {
		if !wk[k] {
			onlyGo = append(onlyGo, k)
		}
	}
	for k := range wk {
		if !gk[k] {
			onlyPy = append(onlyPy, k)
		}
	}
	sort.Strings(onlyGo)
	sort.Strings(onlyPy)
	var integrity string
	_ = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity)
	// The new store must also work: insert via trigger and search.
	_, ierr := db.ExecContext(ctx, "INSERT INTO conversation (id, brief, open, created) VALUES ('r1', 'fresh brief', 1, 5)")
	var hits int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_search WHERE conversation_search MATCH '"fresh"*'`).Scan(&hits)
	report("g DDL replay equals Python sqlite_master", len(onlyGo) == 0 && len(onlyPy) == 0 && len(got) == len(want),
		"%d statements replayed; rows go=%d py=%d; only-in-go=%d only-in-py=%d; integrity=%s; trigger insert err=%v hits=%d; diff=%v %v",
		applied, len(got), len(want), len(onlyGo), len(onlyPy), integrity, ierr, hits, trunc(onlyGo), trunc(onlyPy))
}

func trunc(s []string) []string {
	if len(s) > 3 {
		return s[:3]
	}
	return s
}

// ftsCheck runs both integrity checks on a store and reports the result.
func ftsCheck(driver, store string) error {
	ctx := context.Background()
	db, err := open(driver, store, "")
	if err != nil {
		return err
	}
	defer db.Close()
	var pragma string
	perr := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&pragma)
	_, ferr := db.ExecContext(ctx, "INSERT INTO conversation_search(conversation_search) VALUES('integrity-check')")
	fmt.Printf("PRAGMA integrity_check=%q err=%v\nfts5 integrity-check err=%v\n", pragma, perr, ferr)
	return nil
}
