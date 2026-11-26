package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/witseko/agentcy/internal/paths"
)

// keyByPath returns a key function that answers WorkspaceKey's results for a plain
// directory without running Git.
func keyByPath(t *testing.T) func(string) (string, error) {
	t.Helper()
	return func(directory string) (string, error) {
		return paths.WorkspaceKey(directory, []string{"PATH="})
	}
}

func TestCheckLaunchCheckout(t *testing.T) {
	root, err := paths.RealPath(shortDataDir(t), true)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(checkout, alias); err != nil {
		t.Fatal(err)
	}
	keyOf := keyByPath(t)
	workspace, err := keyOf(checkout)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := CheckLaunchCheckout(alias, workspace, keyOf)
	if err != nil || resolved != checkout {
		t.Errorf("matching checkout: %q, %v, want %q", resolved, err, checkout)
	}
	rejections := map[string]struct{ directory, message string }{
		"relative path":     {"checkout", launchOutsideWorkspace},
		"another workspace": {root, launchOutsideWorkspace},
		"a file":            {file, paths.ErrNotDirectory.Error()},
	}
	for name, entry := range rejections {
		_, err := CheckLaunchCheckout(entry.directory, workspace, keyOf)
		var rejection *Rejection
		if !errors.As(err, &rejection) || rejection.Message != entry.message {
			t.Errorf("%s: error %v, want rejection %q", name, err, entry.message)
		}
	}
	_, err = CheckLaunchCheckout(filepath.Join(root, "missing"), workspace, keyOf)
	var rejection *Rejection
	if err == nil || errors.As(err, &rejection) {
		t.Errorf("missing directory: error %v, want an internal error", err)
	}
}
