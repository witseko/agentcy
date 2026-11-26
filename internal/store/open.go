package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"
)

// workspaceKeyPattern matches a workspace key: the SHA-256 hex digest that
// paths.WorkspaceKey returns.
var workspaceKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Registry opens the stores of one data directory and keeps each open for reuse, like
// the lru_cache on Python's database(). Create one per process with NewRegistry and
// close it with Close at shutdown. It is safe for concurrent use.
type Registry struct {
	dataDir string
	mutex   sync.Mutex
	byPath  map[string]*Store
	flight  singleflight.Group
}

// NewRegistry returns an empty registry for dataDir, the directory that paths.DataDir
// returns.
func NewRegistry(dataDir string) *Registry {
	return &Registry{dataDir: dataDir, byPath: map[string]*Store{}}
}

// DataDir returns the data directory of the registry.
func (registry *Registry) DataDir() string {
	return registry.dataDir
}

// Open returns the store of a workspace key. The first call opens the file
// <dataDir>/<key>.sqlite3, creating it at SchemaVersion when it is missing or empty; later
// calls return the same *Store. A store at any other version is refused with
// ErrUnsupportedVersion.
func (registry *Registry) Open(ctx context.Context, key string) (*Store, error) {
	if !workspaceKeyPattern.MatchString(key) {
		return nil, ErrInvalidWorkspaceKey
	}
	path := filepath.Join(registry.dataDir, key+storeSuffix)
	return registry.cachedOpen(ctx, path, SchemaVersion, workspaceSchema, ErrUnsupportedVersion)
}

// OpenLedger returns the user-level token ledger, creating it at LedgerSchemaVersion
// when it is missing. Later calls return the same *Store.
func (registry *Registry) OpenLedger(ctx context.Context) (*Store, error) {
	path := filepath.Join(registry.dataDir, LedgerFilename)
	return registry.cachedOpen(ctx, path, LedgerSchemaVersion, ledgerSchema, ErrUnsupportedLedgerVersion)
}

// cached returns the open store of path, if any.
func (registry *Registry) cached(path string) (*Store, bool) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	store, found := registry.byPath[path]
	return store, found
}

// cachedOpen returns the cached store of path or opens it. Concurrent first opens for
// the same path share one open via singleflight without holding the registry lock
// across disk I/O or SQLite busy retries. A failed open is not cached.
func (registry *Registry) cachedOpen(ctx context.Context, path string, version int, schema string, unsupported error) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store, found := registry.cached(path); found {
		return store, nil
	}
	value, err, _ := registry.flight.Do(path, func() (any, error) {
		if store, found := registry.cached(path); found {
			return store, nil
		}
		opened, err := openDatabase(ctx, path, version, schema, unsupported)
		if err != nil {
			return nil, err
		}
		registry.mutex.Lock()
		defer registry.mutex.Unlock()
		if existing, found := registry.byPath[path]; found {
			_ = opened.close()
			return existing, nil
		}
		registry.byPath[path] = opened
		return opened, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*Store), nil
}

// Close closes every open store and empties the registry. Call it at shutdown, after
// the last Read or Write has returned.
func (registry *Registry) Close() error {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	var errs []error
	for path, store := range registry.byPath {
		errs = append(errs, store.close())
		delete(registry.byPath, path)
	}
	return errors.Join(errs...)
}

// Exists reports whether the data directory holds a store for key, without creating
// one. Like Python's database_exists, a symbolic link to a regular file counts.
func (registry *Registry) Exists(key string) bool {
	if !workspaceKeyPattern.MatchString(key) {
		return false
	}
	info, err := os.Stat(filepath.Join(registry.dataDir, key+storeSuffix))
	return err == nil && info.Mode().IsRegular()
}

// WorkspaceKeys lists the keys of the workspace stores in the data directory, in name
// order. Only files named as 64 lowercase hex characters plus ".sqlite3" count, so the
// token ledger and other SQLite files are skipped (port of workspace_keys).
func (registry *Registry) WorkspaceKeys() ([]string, error) {
	entries, err := os.ReadDir(registry.dataDir)
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
