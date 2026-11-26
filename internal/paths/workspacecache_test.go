package paths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// countingCache returns a cache with a settable clock whose key computation counts its
// calls and returns the resolved path as the key.
func countingCache(t *testing.T) (*WorkspaceKeyCache, *time.Time, *int) {
	t.Helper()
	clock := time.Unix(1_000_000, 0)
	calls := 0
	cache := NewWorkspaceKeyCache(nil, func() time.Time { return clock })
	cache.compute = func(root string) (string, error) {
		calls++
		return "key of " + root, nil
	}
	return cache, &clock, &calls
}

// mustCachedKey returns a key from the cache or fails the test.
func mustCachedKey(t *testing.T, cache *WorkspaceKeyCache, directory string) string {
	t.Helper()
	key, err := cache.Key(directory)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestWorkspaceKeyCacheHitsForTheSameResolvedDirectory(t *testing.T) {
	cache, clock, calls := countingCache(t)
	directory := realTempDir(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	first := mustCachedKey(t, cache, directory)
	*clock = clock.Add(WorkspaceKeyLifetime - time.Second)
	second := mustCachedKey(t, cache, alias)
	if first != second || *calls != 1 {
		t.Errorf("keys %q and %q after %d computations, want one shared key", first, second, *calls)
	}
}

func TestWorkspaceKeyCacheExpiresAfterItsLifetime(t *testing.T) {
	cache, clock, calls := countingCache(t)
	directory := realTempDir(t)
	mustCachedKey(t, cache, directory)
	*clock = clock.Add(WorkspaceKeyLifetime)
	mustCachedKey(t, cache, directory)
	if *calls != 2 {
		t.Errorf("%d computations, want 2 after the lifetime", *calls)
	}
	if len(cache.entries) != 1 {
		t.Errorf("%d entries, want the expired one replaced", len(cache.entries))
	}
}

func TestWorkspaceKeyCacheDropsAnEntryWhenGitChanges(t *testing.T) {
	cache, _, calls := countingCache(t)
	directory := realTempDir(t)
	mustCachedKey(t, cache, directory)
	gitDirectory := filepath.Join(directory, ".git")
	if err := os.Mkdir(gitDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	mustCachedKey(t, cache, directory)
	if *calls != 2 {
		t.Fatalf("%d computations after creating .git, want 2", *calls)
	}
	mustCachedKey(t, cache, directory)
	if err := os.Remove(gitDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitDirectory, []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCachedKey(t, cache, directory)
	if *calls != 3 {
		t.Errorf("%d computations after replacing .git with a file, want 3", *calls)
	}
}

func TestWorkspaceKeyCacheDoesNotCacheFailures(t *testing.T) {
	cache, _, _ := countingCache(t)
	failures := 0
	cache.compute = func(string) (string, error) {
		failures++
		return "", errors.New("git failed")
	}
	directory := realTempDir(t)
	for range 2 {
		if _, err := cache.Key(directory); err == nil {
			t.Fatal("expected the computation error")
		}
	}
	if failures != 2 || len(cache.entries) != 0 {
		t.Errorf("%d computations and %d entries, want 2 and 0", failures, len(cache.entries))
	}
	if _, err := cache.Key(filepath.Join(directory, "missing")); err == nil {
		t.Error("a missing directory must fail")
	}
}

func TestWorkspaceKeyCacheMatchesWorkspaceKey(t *testing.T) {
	_, project, _ := gitRepositories(t)
	cache := NewWorkspaceKeyCache(os.Environ(), nil)
	if got, want := mustCachedKey(t, cache, project), mustKey(t, project, os.Environ()); got != want {
		t.Errorf("cached key %s, WorkspaceKey %s", got, want)
	}
}
