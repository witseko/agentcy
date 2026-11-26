package core

import (
	"errors"
	"path/filepath"

	"github.com/witseko/agentcy/internal/paths"
)

// launchOutsideWorkspace is the answer to a launch whose checkout belongs to another
// workspace or is not an absolute path.
const launchOutsideWorkspace = "Fresh-run checkout is outside the requested workspace"

// CheckLaunchCheckout checks the directory of a member "launch" request and returns it
// with every symbolic link resolved. The Handler calls it before it queues a fresh run;
// queueing and dispatch are milestone M4.
//
// Port of the checks in Core.launch in core.py: the directory must be absolute and its
// workspace key, from keyOf (usually paths.WorkspaceKeyCache.Key), must equal the
// request's workspace. A path that is not a directory gets the key's own message, as the
// Python ValueError does. A missing path is an internal error, as the Python OSError is.
func CheckLaunchCheckout(directory, workspace string, keyOf func(directory string) (string, error)) (string, error) {
	if !filepath.IsAbs(directory) {
		return "", Reject(launchOutsideWorkspace)
	}
	key, err := keyOf(directory)
	if errors.Is(err, paths.ErrNotDirectory) {
		return "", Reject(paths.ErrNotDirectory.Error())
	}
	if err != nil {
		return "", err
	}
	if key != workspace {
		return "", Reject(launchOutsideWorkspace)
	}
	return paths.RealPath(directory, true)
}
