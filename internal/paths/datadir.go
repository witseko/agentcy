package paths

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// HomeVariable names the environment variable that overrides the data directory.
const HomeVariable = "AGENTCY_HOME"

// ErrDataDirUnsafe is the one error for a data directory that other users could reach.
var ErrDataDirUnsafe = errors.New("Bridge data directory must belong to you and have mode 0700")

// DataDir returns the private directory that holds the socket and the stores, creating it
// with mode 0700 when it is missing. home and homeSet are the value of AGENTCY_HOME and
// whether it is set; as in Python, a set but empty value means the working directory.
//
// Port of data_dir in the former Python core's storage/database.py: the directory must not be a
// symbolic link, must belong to the current user and must have no group or other
// permission bits. The returned path has every symbolic link resolved.
func DataDir(home string, homeSet bool) (string, error) {
	path := home
	if homeSet && path == "" {
		path = "."
	}
	if !homeSet {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(userHome, ".agentcy")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	if err := checkPrivateDirectory(path); err != nil {
		return "", err
	}
	return RealPath(path, true)
}

// checkPrivateDirectory rejects a symbolic link, a non-directory, a directory owned by
// another user, and any group or other permission bit using a single Lstat call.
func checkPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrDataDirUnsafe
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(status.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return ErrDataDirUnsafe
	}
	return nil
}
