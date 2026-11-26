package paths

import (
	"os"
	"strings"
	"syscall"
)

// maxSymlinkHops bounds symbolic link expansions (POSIX SYMLOOP_MAX / Linux MAXSYMLINKS).
const maxSymlinkHops = 40

// pathPart is one entry of the RealPath work stack: a path component to resolve, or a
// marker that the symbolic link named in link has been resolved to the current path.
type pathPart struct {
	name     string
	resolved bool
	link     string
}

// realPathResolver holds the state of one RealPath walk.
type realPathResolver struct {
	strict    bool
	resolved  string
	rest      []pathPart
	remaining int
	seen      map[string]*string
	hops      int
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
	rest := reversedParts(path)
	if !strings.HasPrefix(path, "/") {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return "", err
		}
		rest = append(rest, reversedParts(workingDirectory)...)
	}
	resolver := realPathResolver{
		strict:    strict,
		resolved:  "/",
		rest:      rest,
		remaining: len(rest),
		seen:      map[string]*string{},
	}
	return resolver.run()
}

// run processes the work stack until every component is resolved.
func (r *realPathResolver) run() (string, error) {
	for r.remaining > 0 {
		part := r.rest[len(r.rest)-1]
		r.rest = r.rest[:len(r.rest)-1]
		if part.resolved {
			target := r.resolved
			r.seen[part.link] = &target
			continue
		}
		r.remaining--
		if err := r.step(part.name); err != nil {
			return "", err
		}
	}
	return r.resolved, nil
}

// step resolves one non-marker path component.
func (r *realPathResolver) step(name string) error {
	switch name {
	case "", ".":
		return nil
	case "..":
		r.resolved = parentPath(r.resolved)
		return nil
	}
	next := joinPath(r.resolved, name)
	target, isLink, err := readComponent(next, r.strict && r.remaining > 0)
	if err != nil {
		if r.strict {
			return err
		}
		r.resolved = next
		return nil
	}
	if !isLink {
		r.resolved = next
		return nil
	}
	return r.expandLink(next, target)
}

// expandLink resolves a symbolic link component using the seen cache and hop cap.
func (r *realPathResolver) expandLink(link, target string) error {
	if cached, ok := r.seen[link]; ok {
		if cached != nil {
			r.resolved = *cached
			return nil
		}
		if r.strict {
			return &os.PathError{Op: "realpath", Path: link, Err: syscall.ELOOP}
		}
		r.resolved = link
		return nil
	}
	r.hops++
	if r.hops > maxSymlinkHops {
		if r.strict {
			return &os.PathError{Op: "realpath", Path: link, Err: syscall.ELOOP}
		}
		r.resolved = link
		return nil
	}
	if strings.HasPrefix(target, "/") {
		r.resolved = "/"
	}
	r.seen[link] = nil
	r.rest = append(r.rest, pathPart{resolved: true, link: link})
	targetParts := reversedParts(target)
	r.rest = append(r.rest, targetParts...)
	r.remaining += len(targetParts)
	return nil
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
