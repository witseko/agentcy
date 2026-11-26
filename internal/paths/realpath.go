package paths

import (
	"os"
	"strings"
	"syscall"
)

// pathPart is one entry of the RealPath work stack: a path component to resolve, or a
// marker that the symbolic link named in link has been resolved to the current path.
type pathPart struct {
	name     string
	resolved bool
	link     string
}

// RealPath returns the canonical absolute form of path with every symbolic link resolved,
// component by component from the left.
//
// Port of posixpath.realpath (Python 3.14), which Path.resolve uses. It differs from
// filepath.EvalSymlinks: ".." after a symbolic link climbs from the link's target, not
// from the link's parent, and in non-strict mode a missing component is kept as written.
// In strict mode a missing component, a file used as a directory, or a link loop is an
// error.
func RealPath(path string, strict bool) (string, error) {
	resolved := "/"
	if !strings.HasPrefix(path, "/") {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return "", err
		}
		resolved = workingDirectory
	}
	rest := reversedParts(path)
	remaining := len(rest)
	seen := map[string]*string{}
	for remaining > 0 {
		part := rest[len(rest)-1]
		rest = rest[:len(rest)-1]
		if part.resolved {
			target := resolved
			seen[part.link] = &target
			continue
		}
		remaining--
		if part.name == "" || part.name == "." {
			continue
		}
		if part.name == ".." {
			resolved = parentPath(resolved)
			continue
		}
		next := joinPath(resolved, part.name)
		target, isLink, err := readComponent(next, strict && remaining > 0)
		if err != nil {
			if strict {
				return "", err
			}
			resolved = next
			continue
		}
		if !isLink {
			resolved = next
			continue
		}
		if cached, ok := seen[next]; ok {
			if cached != nil {
				resolved = *cached
				continue
			}
			if strict {
				return "", &os.PathError{Op: "realpath", Path: next, Err: syscall.ELOOP}
			}
			resolved = next
			continue
		}
		if strings.HasPrefix(target, "/") {
			resolved = "/"
		}
		seen[next] = nil
		rest = append(rest, pathPart{resolved: true, link: next})
		targetParts := reversedParts(target)
		rest = append(rest, targetParts...)
		remaining += len(targetParts)
	}
	return resolved, nil
}

// readComponent reports whether path is a symbolic link and, if so, its target. When
// mustBeDirectory is set, a component that is neither a link nor a directory is an error.
func readComponent(path string, mustBeDirectory bool) (string, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if mustBeDirectory && !info.IsDir() {
			return "", false, &os.PathError{Op: "realpath", Path: path, Err: syscall.ENOTDIR}
		}
		return "", false, nil
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", false, err
	}
	return target, true, nil
}

// reversedParts splits a path on "/" into a stack whose top is the first component.
func reversedParts(path string) []pathPart {
	names := strings.Split(path, "/")
	parts := make([]pathPart, 0, len(names))
	for index := len(names) - 1; index >= 0; index-- {
		parts = append(parts, pathPart{name: names[index]})
	}
	return parts
}

// parentPath drops the last component of an absolute path, stopping at the root.
func parentPath(path string) string {
	parent := path[:strings.LastIndex(path, "/")]
	if parent == "" {
		return "/"
	}
	return parent
}

// joinPath appends one component to an absolute path.
func joinPath(path, name string) string {
	if path == "/" {
		return path + name
	}
	return path + "/" + name
}
