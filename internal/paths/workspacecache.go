package paths

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/singleflight"
)

// WorkspaceKeyLifetime is how long a cached workspace key stays valid (plan fix L7).
const WorkspaceKeyLifetime = 60 * time.Second

// WorkspaceKeyCache remembers the workspace key of each resolved directory, so operator
// requests, which name the checkout on every call, do not run Git each time.
//
// A key stays valid for WorkspaceKeyLifetime and is dropped early when the status of
// <directory>/.git changes, for example after `git init` or when a worktree is linked.
// A change further up, such as `git init` in a parent directory, shows after at most the
// lifetime. Failures are never cached.
type WorkspaceKeyCache struct {
	now     func() time.Time
	compute func(root string) (string, error)

	mutex   sync.Mutex
	entries map[string]cachedWorkspaceKey
	flight  singleflight.Group
}

// cachedWorkspaceKey is one remembered key with its expiry and the .git status it saw.
type cachedWorkspaceKey struct {
	key     string
	expires time.Time
	git     gitStatus
}

// gitStatus is what the cache compares of <directory>/.git: whether it exists, and its
// identity, type, size, and modification time.
type gitStatus struct {
	exists   bool
	device   uint64
	inode    uint64
	mode     uint32
	size     int64
	modified int64
}

// NewWorkspaceKeyCache returns an empty cache. environment is passed to WorkspaceKey, and
// now is the clock (time.Now when nil).
func NewWorkspaceKeyCache(environment []string, now func() time.Time) *WorkspaceKeyCache {
	if now == nil {
		now = time.Now
	}
	return &WorkspaceKeyCache{
		now:     now,
		compute: func(root string) (string, error) { return workspaceKeyResolved(root, environment) },
		entries: map[string]cachedWorkspaceKey{},
	}
}

// Key returns the workspace key of directory, as WorkspaceKey does, from the cache when a
// fresh entry exists for the resolved directory and its .git status is unchanged.
func (cache *WorkspaceKeyCache) Key(directory string) (string, error) {
	root, err := RealPath(directory, true)
	if err != nil {
		return "", err
	}
	status := statGit(root)
	now := cache.now()
	cache.mutex.Lock()
	entry, found := cache.entries[root]
	cache.mutex.Unlock()
	if found && now.Before(entry.expires) && entry.git == status {
		return entry.key, nil
	}
	// Git runs outside the lock, deduplicated per root by singleflight so concurrent
	// misses share one subprocess and one slow checkout does not block the others.
	value, err, _ := cache.flight.Do(root, func() (any, error) {
		key, err := cache.compute(root)
		if err != nil {
			return "", err
		}
		computedAt := cache.now()
		cache.mutex.Lock()
		defer cache.mutex.Unlock()
		cache.dropExpired(computedAt)
		cache.entries[root] = cachedWorkspaceKey{key: key, expires: computedAt.Add(WorkspaceKeyLifetime), git: status}
		return key, nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

// dropExpired removes expired entries, so the cache holds only recently used checkouts.
// The caller holds the mutex.
func (cache *WorkspaceKeyCache) dropExpired(now time.Time) {
	for root, entry := range cache.entries {
		if !now.Before(entry.expires) {
			delete(cache.entries, root)
		}
	}
}

// statGit reads the status of root/.git without following a symbolic link.
func statGit(root string) gitStatus {
	info, err := os.Lstat(filepath.Join(root, ".git"))
	if err != nil {
		return gitStatus{}
	}
	status := gitStatus{exists: true, mode: uint32(info.Mode()), size: info.Size(), modified: info.ModTime().UnixNano()}
	if system, ok := info.Sys().(*syscall.Stat_t); ok {
		status.device = uint64(system.Dev)
		status.inode = system.Ino
	}
	return status
}
