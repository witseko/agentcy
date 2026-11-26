//go:build conformance

package conformance

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	// Registers the "sqlite" driver that reads the daemons' stores.
	_ "modernc.org/sqlite"
)

// compareWalkDumps compares the database files of the two homes and every row of every
// table in them, normalised like the replies.
func compareWalkDumps(t *testing.T, seed int64, steps int, sides [2]*walkSide, start int64) {
	t.Helper()
	var dumps [2]string
	for index, side := range sides {
		dumps[index] = dumpHome(t, side, start)
	}
	if dumps[0] == dumps[1] {
		return
	}
	t.Fatalf("seed %d: the final databases differ after %d steps\n%s\nreplay: go test -tags conformance ./conformance/... -run TestDifferentialWalk -seed %d -steps %d",
		seed, steps, firstDifference(dumps[0], dumps[1]), seed, steps)
}

// dumpHome lists the SQLite files of a home and dumps each of them.
func dumpHome(t *testing.T, side *walkSide, start int64) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(side.daemon.home, "*.sqlite3"))
	if err != nil {
		t.Fatalf("list stores: %v", err)
	}
	sort.Strings(paths)
	var builder strings.Builder
	for _, path := range paths {
		fmt.Fprintf(&builder, "== file %s\n", side.normalizer.text(filepath.Base(path)))
		dumpDatabase(t, &builder, side, path, start)
	}
	return builder.String()
}

// dumpDatabase writes every row of every table of one database, in rowid order. The
// FTS5 shadow tables are skipped; the search table itself is dumped.
func dumpDatabase(t *testing.T, builder *strings.Builder, side *walkSide, path string, start int64) {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer database.Close()
	tables, err := tableNames(database)
	if err != nil {
		t.Fatalf("list tables of %s: %v", path, err)
	}
	for _, table := range tables {
		fmt.Fprintf(builder, "-- table %s\n", table)
		if err := dumpTable(builder, database, side.normalizer, table, start); err != nil {
			t.Fatalf("dump %s: %v", table, err)
		}
	}
}

// tableNames lists the tables of a database by name, without SQLite's own and the FTS5
// shadow tables.
func tableNames(database *sql.DB) ([]string, error) {
	rows, err := database.Query(`SELECT name FROM sqlite_master WHERE type = 'table'
		AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'conversation_search_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// dumpTable writes one line per row with normalised values.
func dumpTable(builder *strings.Builder, database *sql.DB, normalizer *normalizer, table string, start int64) error {
	// The name comes from sqlite_master of a store the test created, not from input.
	rows, err := database.Query(`SELECT * FROM "` + table + `" ORDER BY rowid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	values := make([]any, len(columns))
	targets := make([]any, len(columns))
	for index := range values {
		targets[index] = &values[index]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		cells := make([]string, len(columns))
		for index, value := range values {
			cells[index] = columns[index] + "=" + dumpValue(normalizer, value, start)
		}
		builder.WriteString(strings.Join(cells, " | ") + "\n")
	}
	return rows.Err()
}

// dumpValue normalises one stored value: text like a reply, a Unix time near start as
// <time>, anything else as stored.
func dumpValue(normalizer *normalizer, value any, start int64) string {
	switch typed := value.(type) {
	case nil:
		return "NULL"
	case int64:
		if typed > start-timestampWindowSeconds && typed < start+timestampWindowSeconds {
			return "<time>"
		}
		return fmt.Sprint(typed)
	case string:
		return quoteJSON(normalizer.text(typed))
	case []byte:
		return quoteJSON(normalizer.text(string(typed)))
	}
	return fmt.Sprint(value)
}
