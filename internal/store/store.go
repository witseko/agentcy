package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	// Registers the CGO-free "sqlite" driver.
	_ "modernc.org/sqlite"
)

// Store is one open SQLite database with a write pool and a read pool.
type Store struct {
	path string
	// write has a single connection, and every transaction on it is BEGIN IMMEDIATE.
	write *sql.DB
	read  *sql.DB
	// writeMutex lets one Write at a time into the database (port plan fix L9; it
	// replaces the 64 lock stripes that Python shares between all workspaces).
	writeMutex sync.Mutex
}

// Path returns the file of the database.
func (store *Store) Path() string {
	return store.path
}

// Write runs work in an immediate transaction under the store's write mutex. It commits
// when work returns nil and rolls back otherwise. When another process holds the write
// lock, the transaction waits up to the 5 second busy timeout and then fails with
// "database is locked". work must not call Write on the same store.
func (store *Store) Write(ctx context.Context, work func(*sql.Tx) error) error {
	store.writeMutex.Lock()
	defer store.writeMutex.Unlock()
	return runTransaction(ctx, store.write, work, true)
}

// Read runs work in a transaction on the read pool, which sees one consistent snapshot
// and never blocks writers. The transaction is always rolled back, so writes made by
// work are discarded.
func (store *Store) Read(ctx context.Context, work func(*sql.Tx) error) error {
	return runTransaction(ctx, store.read, work, false)
}

// runTransaction begins a transaction on pool, runs work and then commits (when commit
// is true and work succeeded) or rolls back.
func runTransaction(ctx context.Context, pool *sql.DB, work func(*sql.Tx) error, commit bool) error {
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// After a commit the rollback is a no-op; it also runs when work panics.
	defer func() { _ = tx.Rollback() }()
	if err := work(tx); err != nil {
		return err
	}
	if !commit {
		return nil
	}
	return tx.Commit()
}

// close closes both pools.
func (store *Store) close() error {
	return errors.Join(store.write.Close(), store.read.Close())
}

// openDatabase opens the database at path and makes sure it has the given schema
// version. A missing file is created with mode 0600; a file at version 0 gets schema
// replayed inside one immediate transaction, so two processes opening a new store at
// once create it only once. Any other version returns unsupported.
func openDatabase(ctx context.Context, path string, version int, schema string, unsupported error) (*Store, error) {
	if err := createPrivateFile(path); err != nil {
		return nil, err
	}
	write, err := sql.Open("sqlite", connectionString(path, true))
	if err != nil {
		return nil, err
	}
	write.SetMaxOpenConns(1)
	read, err := sql.Open("sqlite", connectionString(path, false))
	if err != nil {
		return nil, errors.Join(err, write.Close())
	}
	store := &Store{path: path, write: write, read: read}
	if err := store.initialise(ctx, version, schema, unsupported); err != nil {
		return nil, errors.Join(err, store.close())
	}
	return store, nil
}

// The retry budget of initialise, about as long as the busy timeout.
const (
	openAttempts   = 50
	openRetryDelay = 100 * time.Millisecond
)

// initialise runs ensureSchema in an immediate transaction. When several connections
// open a new file at once, SQLite can fail one of them with SQLITE_BUSY at once instead
// of waiting (seen while the connections switch the file to WAL), so a busy failure here
// is retried for about 5 seconds.
func (store *Store) initialise(ctx context.Context, version int, schema string, unsupported error) error {
	var err error
	for range openAttempts {
		err = store.Write(ctx, func(tx *sql.Tx) error {
			return ensureSchema(ctx, tx, version, schema, unsupported)
		})
		if !isBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(openRetryDelay):
		}
	}
	return err
}

// isBusy recognizes the error of a database locked by another connection.
func isBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLITE_BUSY")
}

// createPrivateFile creates an empty database file readable only by the user, unless it
// exists. SQLite gives the -wal and -shm files the mode of the database file, so they
// are private too, whatever the umask. Python gets the same modes from the umask 077
// that its daemon sets.
func createPrivateFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.Join(file.Chmod(0o600), file.Close())
}

// ensureSchema reads the version inside the open immediate transaction and creates the
// schema when the database is new.
func ensureSchema(ctx context.Context, tx *sql.Tx, version int, schema string, unsupported error) error {
	var current int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current == version {
		return nil
	}
	if current != 0 {
		return unsupported
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	// PRAGMA takes no bound parameters; version is a constant of this package.
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", version))
	return err
}
