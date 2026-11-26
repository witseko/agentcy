package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// workspaceKeyPattern matches a workspace key: the SHA-256 hex digest that
// paths.WorkspaceKey returns.
var workspaceKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// openStores is the per-process cache of open databases by file path. It is the one
// piece of global state in this package, like the lru_cache on Python's database().
var openStores = struct {
	sync.Mutex
	byPath map[string]*Store
}{byPath: map[string]*Store{}}

// Open returns the store of a workspace key inside dataDir, the directory that
// paths.DataDir returns. The first call in a process opens the file
// <dataDir>/<key>.sqlite3, creating it at SchemaVersion when it is missing or empty; later
// calls return the same *Store. A store at any other version is refused with
// ErrUnsupportedVersion.
func Open(ctx context.Context, dataDir, key string) (*Store, error) {
	if !workspaceKeyPattern.MatchString(key) {
		return nil, ErrInvalidWorkspaceKey
	}
	path := filepath.Join(dataDir, key+storeSuffix)
	return cachedOpen(ctx, path, SchemaVersion, workspaceSchema, ErrUnsupportedVersion)
}

// OpenLedger returns the user-level token ledger inside dataDir, creating it at
// LedgerSchemaVersion when it is missing. Later calls return the same *Store.
func OpenLedger(ctx context.Context, dataDir string) (*Store, error) {
	path := filepath.Join(dataDir, LedgerFilename)
	return cachedOpen(ctx, path, LedgerSchemaVersion, ledgerSchema, ErrUnsupportedLedgerVersion)
}

// cachedOpen returns the cached store of path or opens it. The cache lock is held while
// opening, so concurrent first opens in one process share one *Store. A failed open is
// not cached.
func cachedOpen(ctx context.Context, path string, version int, schema string, unsupported error) (*Store, error) {
	openStores.Lock()
	defer openStores.Unlock()
	if store, found := openStores.byPath[path]; found {
		return store, nil
	}
	store, err := openDatabase(ctx, path, version, schema, unsupported)
	if err != nil {
		return nil, err
	}
	openStores.byPath[path] = store
	return store, nil
}

// CloseAll closes every cached store and empties the cache. Call it at shutdown, after
// the last Read or Write has returned.
func CloseAll() error {
	openStores.Lock()
	defer openStores.Unlock()
	var errs []error
	for path, store := range openStores.byPath {
		errs = append(errs, store.close())
		delete(openStores.byPath, path)
	}
	return errors.Join(errs...)
}

// Exists reports whether dataDir holds a store for key, without creating one. Like
// Python's database_exists, a symbolic link to a regular file counts.
func Exists(dataDir, key string) bool {
	if !workspaceKeyPattern.MatchString(key) {
		return false
	}
	info, err := os.Stat(filepath.Join(dataDir, key+storeSuffix))
	return err == nil && info.Mode().IsRegular()
}

// WorkspaceKeys lists the keys of the workspace stores in dataDir, in name order. Only
// files named as 64 lowercase hex characters plus ".sqlite3" count, so the token ledger
// and other SQLite files are skipped (port of workspace_keys).
func WorkspaceKeys(dataDir string) ([]string, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for _, entry := range entries {
		key, found := strings.CutSuffix(entry.Name(), storeSuffix)
		if found && workspaceKeyPattern.MatchString(key) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
