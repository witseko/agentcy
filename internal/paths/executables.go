package paths

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// defaultSearchPath is the search path that Python's os.get_exec_path uses when PATH is
// unset (os.defpath on POSIX).
const defaultSearchPath = "/bin:/usr/bin"

// AbsoluteSearchPath keeps the configured search order of PATH while dropping relative
// directories. pathValue and pathSet are the value of PATH and whether it is set.
//
// Port of absolute_search_path in src/agentcy/executables.py.
func AbsoluteSearchPath(pathValue string, pathSet bool) []string {
	if !pathSet {
		pathValue = defaultSearchPath
	}
	var directories []string
	for _, directory := range strings.Split(pathValue, string(os.PathListSeparator)) {
		if filepath.IsAbs(directory) {
			directories = append(directories, directory)
		}
	}
	return directories
}

// FindOnPath finds a bare program name in the absolute entries of PATH and returns its
// path, or "" when there is none. A name with a directory part is never looked up, so a
// relative name cannot resolve against the working directory.
//
// Port of find_on_path in src/agentcy/executables.py, which uses shutil.which: the first
// entry whose file exists, is executable by the user, and is not a directory wins.
// exec.LookPath is not used because it also accepts relative PATH entries.
func FindOnPath(name, pathValue string, pathSet bool) string {
	if name == "" || strings.Contains(name, "/") {
		return ""
	}
	seen := map[string]bool{}
	for _, directory := range AbsoluteSearchPath(pathValue, pathSet) {
		if seen[directory] {
			continue
		}
		seen[directory] = true
		candidate := directory + "/" + name
		if strings.HasSuffix(directory, "/") {
			candidate = directory + name
		}
		if isExecutableFile(candidate) {
			return candidate
		}
	}
	return ""
}

// isExecutableFile reports whether path exists, is not a directory, and is executable by
// the current user.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	const executeAccess = 1 // X_OK from unistd.h
	return syscall.Access(path, executeAccess) == nil
}
