package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// testKey is a valid workspace key for tests.
var testKey = strings.Repeat("ab", 32)

// newDataDir returns a fresh private data directory.
func newDataDir(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

// newRegistry returns a registry for dataDir and closes its stores when the test ends.
func newRegistry(t *testing.T, dataDir string) *Registry {
	t.Helper()
	registry := NewRegistry(dataDir)
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	return registry
}

// openRaw opens path outside the store cache with the same PRAGMAs, for setting up and
// inspecting databases.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", connectionString(path, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// masterRows returns every sqlite_master row as type, name, tbl_name and sql, in rowid
// order, in the shape that encoding/json gives the checked-in fixture.
func masterRows(t *testing.T, database *sql.DB) [][]any {
	t.Helper()
	rows, err := database.Query("SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result [][]any
	for rows.Next() {
		var kind, name, table string
		var text sql.NullString
		if err := rows.Scan(&kind, &name, &table, &text); err != nil {
			t.Fatal(err)
		}
		var statement any
		if text.Valid {
			statement = text.String
		}
		result = append(result, []any{kind, name, table, statement})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// fixtureRows reads a sqlite_master dump that the Python core wrote at commit 17eb9ce.
func fixtureRows(t *testing.T, name string) [][]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var row []any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	return result
}

// userVersion reads PRAGMA user_version.
func userVersion(t *testing.T, database *sql.DB) int {
	t.Helper()
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// compareRows reports the first differing row of two sqlite_master dumps.
func compareRows(t *testing.T, got, want [][]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%d sqlite_master rows, want %d", len(got), len(want))
	}
	for index := range min(len(got), len(want)) {
		if !reflect.DeepEqual(got[index], want[index]) {
			t.Errorf("row %d:\n got %q\nwant %q", index, got[index], want[index])
		}
	}
}

// TestNewStoreMatchesPythonSchema compares a Go-created store with the sqlite_master
// dump of a Python-created one, recorded at commit 17eb9ce.
func TestNewStoreMatchesPythonSchema(t *testing.T) {
	directory := newDataDir(t)
	store, err := newRegistry(t, directory).Open(context.Background(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if store.Path() != filepath.Join(directory, testKey+".sqlite3") {
		t.Errorf("Path = %s", store.Path())
	}
	database := openRaw(t, store.Path())
	compareRows(t, masterRows(t, database), fixtureRows(t, "python_master.jsonl"))
	if version := userVersion(t, database); version != SchemaVersion {
		t.Errorf("user_version = %d, want %d", version, SchemaVersion)
	}
}

func TestNewLedgerMatchesPythonSchema(t *testing.T) {
	directory := newDataDir(t)
	ledger, err := newRegistry(t, directory).OpenLedger(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ledger.Path() != filepath.Join(directory, "token_ledger.sqlite3") {
		t.Errorf("Path = %s", ledger.Path())
	}
	database := openRaw(t, ledger.Path())
	compareRows(t, masterRows(t, database), fixtureRows(t, "python_ledger_master.jsonl"))
	if version := userVersion(t, database); version != LedgerSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, LedgerSchemaVersion)
	}
}

// TestFullTextTriggersFire checks that the replayed triggers index new briefs and
// messages.
func TestFullTextTriggersFire(t *testing.T) {
	ctx := context.Background()
	store, err := newRegistry(t, newDataDir(t)).Open(ctx, testKey)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Write(ctx, func(tx *sql.Tx) error {
		statements := []string{
			"INSERT INTO conversation (id, brief, open) VALUES ('r1', 'zebra brief', 1)",
			"INSERT INTO participant (id, room, label, role) VALUES (1, 'r1', 'Lead', 1)",
			"INSERT INTO message (id, room, author, body, created) VALUES (7, 'r1', 1, 'giraffe body', 1)",
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = store.Read(ctx, func(tx *sql.Tx) error {
		var messageID sql.NullInt64
		row := tx.QueryRowContext(ctx, "SELECT message_id FROM conversation_search WHERE conversation_search MATCH ?", "giraffe")
		if err := row.Scan(&messageID); err != nil {
			return err
		}
		if messageID.Int64 != 7 {
			t.Errorf("message_id = %v, want 7", messageID)
		}
		row = tx.QueryRowContext(ctx, "SELECT message_id FROM conversation_search WHERE conversation_search MATCH ?", "zebra")
		if err := row.Scan(&messageID); err != nil {
			return err
		}
		if messageID.Valid {
			t.Errorf("brief row message_id = %v, want NULL", messageID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestOtherVersionsAreRefused covers a version 14 store that Python would migrate, a
// newer store, and a newer ledger.
func TestOtherVersionsAreRefused(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{14, 1, 16, -1} {
		directory := newDataDir(t)
		path := filepath.Join(directory, testKey+".sqlite3")
		if _, err := openRaw(t, path).Exec("PRAGMA user_version=" + strconv.Itoa(version)); err != nil {
			t.Fatal(err)
		}
		_, err := newRegistry(t, directory).Open(ctx, testKey)
		if !errors.Is(err, ErrUnsupportedVersion) || err.Error() != "Unsupported bridge database version" {
			t.Errorf("version %d: err = %v", version, err)
		}
		if version := userVersion(t, openRaw(t, path)); version == SchemaVersion {
			t.Errorf("refused store was changed to version %d", version)
		}
	}
	directory := newDataDir(t)
	if _, err := openRaw(t, filepath.Join(directory, LedgerFilename)).Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	_, err := newRegistry(t, directory).OpenLedger(ctx)
	if err == nil || err.Error() != "Unsupported token ledger database version" {
		t.Errorf("ledger err = %v", err)
	}
}

func TestInvalidWorkspaceKeysAreRefused(t *testing.T) {
	directory := newDataDir(t)
	for _, key := range []string{"", "../" + testKey[3:], strings.ToUpper(testKey), testKey + "0", testKey[1:]} {
		if _, err := newRegistry(t, directory).Open(context.Background(), key); !errors.Is(err, ErrInvalidWorkspaceKey) {
			t.Errorf("Open(%q) err = %v", key, err)
		}
		if NewRegistry(directory).Exists(key) {
			t.Errorf("Exists(%q) = true", key)
		}
	}
	if entries, _ := os.ReadDir(directory); len(entries) != 0 {
		t.Errorf("files created: %v", entries)
	}
}

func TestOpenReturnsTheCachedStore(t *testing.T) {
	directory := newDataDir(t)
	registry := newRegistry(t, directory)
	if registry.Exists(testKey) {
		t.Fatal("store exists before Open")
	}
	var group sync.WaitGroup
	stores := make([]*Store, 8)
	for index := range stores {
		group.Go(func() {
			store, err := registry.Open(context.Background(), testKey)
			if err != nil {
				t.Error(err)
			}
			stores[index] = store
		})
	}
	group.Wait()
	for _, store := range stores[1:] {
		if store != stores[0] {
			t.Fatal("Open returned different stores for one key")
		}
	}
	if !registry.Exists(testKey) {
		t.Error("Exists = false after Open")
	}
}

// TestConcurrentFirstOpenCreatesOnce opens one new file through separate pools at once,
// as separate processes would, bypassing the per-process cache.
func TestConcurrentFirstOpenCreatesOnce(t *testing.T) {
	path := filepath.Join(newDataDir(t), testKey+".sqlite3")
	var group sync.WaitGroup
	stores := make([]*Store, 6)
	errs := make([]error, len(stores))
	for index := range stores {
		group.Go(func() {
			stores[index], errs[index] = openDatabase(context.Background(), path, SchemaVersion, workspaceSchema, ErrUnsupportedVersion)
		})
	}
	group.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("open %d: %v", index, err)
		}
		defer stores[index].close()
	}
	database := openRaw(t, path)
	compareRows(t, masterRows(t, database), fixtureRows(t, "python_master.jsonl"))
	if version := userVersion(t, database); version != SchemaVersion {
		t.Errorf("user_version = %d", version)
	}
}

// TestPathsWithSpecialCharacters opens stores under directories whose names SQLite
// would misread in a plain "file:" URI.
func TestPathsWithSpecialCharacters(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"with space", "hash#mark", "per%25cent", "per%cent", "question?mark", "all #%?&=;"} {
		directory := filepath.Join(newDataDir(t), name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		store, err := newRegistry(t, directory).Open(ctx, testKey)
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		err = store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO conversation (id, brief, open) VALUES ('r', 'b', 1)")
			return err
		})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range entries {
			found = found || entry.Name() == testKey+".sqlite3"
		}
		if !found {
			t.Errorf("%q: store file missing, directory holds %v", name, entries)
		}
		parent, _ := os.ReadDir(filepath.Dir(directory))
		if len(parent) != 1 {
			t.Errorf("%q: stray files beside the directory: %v", name, parent)
		}
	}
}

func TestWriteIsVisibleToRead(t *testing.T) {
	ctx := context.Background()
	store, err := newRegistry(t, newDataDir(t)).Open(ctx, testKey)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO conversation (id, brief, open) VALUES (?, ?, 1)", "r1", "first")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("abort")
	err = store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO conversation (id, brief, open) VALUES ('r2', 'rolled back', 1)"); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the work error", err)
	}
	var briefs []string
	err = store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT brief FROM conversation ORDER BY id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var brief string
			if err := rows.Scan(&brief); err != nil {
				return err
			}
			briefs = append(briefs, brief)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(briefs, []string{"first"}) {
		t.Errorf("briefs = %q", briefs)
	}
}

// holdWriteLock takes the write lock of path from another connection pool, as another
// process would, and releases it after hold.
func holdWriteLock(t *testing.T, path, id string, hold time.Duration) <-chan struct{} {
	t.Helper()
	database, err := sql.Open("sqlite", connectionString(path, true))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO conversation (id, brief, open) VALUES (?, 'b', 1)", id); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(hold)
		_ = tx.Commit()
		_ = database.Close()
		close(released)
	}()
	return released
}

// TestWriteWaitsForAnotherWriter checks the 5 second busy timeout: a short hold is
// waited out, a long one ends in "database is locked".
func TestWriteWaitsForAnotherWriter(t *testing.T) {
	ctx := context.Background()
	store, err := newRegistry(t, newDataDir(t)).Open(ctx, testKey)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id string) error {
		return store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO conversation (id, brief, open) VALUES (?, 'b', 1)", id)
			return err
		})
	}
	released := holdWriteLock(t, store.Path(), "short-hold", time.Second)
	started := time.Now()
	if err := insert("after-short-hold"); err != nil {
		t.Fatalf("write after a 1 s hold: %v", err)
	}
	if waited := time.Since(started); waited < 900*time.Millisecond {
		t.Errorf("write waited %v, want about 1 s", waited)
	}
	<-released
	released = holdWriteLock(t, store.Path(), "long-hold", 7*time.Second)
	started = time.Now()
	err = insert("during-long-hold")
	waited := time.Since(started)
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("write during a 7 s hold: err = %v", err)
	}
	if waited < 4500*time.Millisecond || waited > 6500*time.Millisecond {
		t.Errorf("write gave up after %v, want about 5 s", waited)
	}
	<-released
}

// TestNewFilesArePrivate checks that the store, ledger and their WAL files get mode 0600
// even under a permissive umask, as Python's daemon gets from its umask 077.
func TestNewFilesArePrivate(t *testing.T) {
	previous := syscall.Umask(0o022)
	defer syscall.Umask(previous)
	ctx := context.Background()
	directory := newDataDir(t)
	store, err := newRegistry(t, directory).Open(ctx, testKey)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newRegistry(t, directory).OpenLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{store.Path(), ledger.Path()} {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			info, err := os.Stat(path + suffix)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %o, want 600", filepath.Base(path+suffix), info.Mode().Perm())
			}
		}
	}
}

func TestWorkspaceKeysSkipsOtherFiles(t *testing.T) {
	directory := newDataDir(t)
	other := strings.Repeat("0", 64)
	names := []string{
		testKey + ".sqlite3", other + ".sqlite3", "token_ledger.sqlite3",
		testKey + ".sqlite3-wal", strings.ToUpper(testKey) + ".sqlite3",
		testKey[1:] + ".sqlite3", testKey + "0.sqlite3", "tokens.json", "core.sock",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(directory, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := NewRegistry(directory).WorkspaceKeys()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{other, testKey}; !reflect.DeepEqual(keys, want) {
		t.Errorf("WorkspaceKeys = %q, want %q", keys, want)
	}
}
