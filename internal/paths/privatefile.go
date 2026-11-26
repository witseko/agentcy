package paths

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"
	"syscall"
)

// PrivateConfigFile loads a user-private JSON configuration file and caches the parsed
// value while the file's modification time, size and inode stay unchanged. Safety checks
// (regular file, owned by current user, no group/other bits, at most MaxBytes) run on
// every Load. It is safe for concurrent use.
type PrivateConfigFile[T any] struct {
	path       string
	maxBytes   int64
	errUnsafe  error
	errInvalid error
	parse      func([]byte) (T, error)

	mutex  sync.Mutex
	cached *loadedPrivateConfig[T]
}

type loadedPrivateConfig[T any] struct {
	identity privateFileIdentity
	value    T
	err      error
}

type privateFileIdentity struct {
	modified int64
	size     int64
	inode    uint64
}

// NewPrivateConfigFile returns a cached loader for the private config file at path.
func NewPrivateConfigFile[T any](path string, maxBytes int64, errUnsafe, errInvalid error, parse func([]byte) (T, error)) *PrivateConfigFile[T] {
	return &PrivateConfigFile[T]{
		path:       path,
		maxBytes:   maxBytes,
		errUnsafe:  errUnsafe,
		errInvalid: errInvalid,
		parse:      parse,
	}
}

// Load returns the parsed configuration, or the zero value of T and nil error when the
// file does not exist.
func (file *PrivateConfigFile[T]) Load() (T, error) {
	var zero T
	file.mutex.Lock()
	defer file.mutex.Unlock()
	info, err := os.Lstat(file.path)
	if errors.Is(err, fs.ErrNotExist) {
		file.cached = nil
		return zero, nil
	}
	if err != nil {
		return zero, file.errInvalid
	}
	identity, err := file.checkIdentity(info)
	if err != nil {
		return zero, err
	}
	if file.cached != nil && file.cached.identity == identity {
		return file.cached.value, file.cached.err
	}
	loaded, err := file.read()
	if err != nil {
		return zero, err
	}
	if loaded == nil {
		file.cached = nil
		return zero, nil
	}
	file.cached = loaded
	return loaded.value, loaded.err
}

// read opens the file without following a symbolic link, re-verifies its identity, and
// parses it. A parse error is cached alongside the file identity.
func (file *PrivateConfigFile[T]) read() (*loadedPrivateConfig[T], error) {
	handle, err := os.OpenFile(file.path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if errors.Is(err, syscall.ELOOP) {
		return nil, file.errUnsafe
	}
	if err != nil {
		return nil, file.errInvalid
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, file.errInvalid
	}
	identity, err := file.checkIdentity(info)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(handle, file.maxBytes+1))
	if err != nil {
		return nil, file.errInvalid
	}
	if int64(len(data)) > file.maxBytes {
		return nil, file.errUnsafe
	}
	value, err := file.parse(data)
	return &loadedPrivateConfig[T]{identity: identity, value: value, err: err}, nil
}

// checkIdentity verifies that info is a regular file owned by the current user with no
// group or other permission bits and size at most maxBytes.
func (file *PrivateConfigFile[T]) checkIdentity(info fs.FileInfo) (privateFileIdentity, error) {
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || int(status.Uid) != os.Getuid() ||
		info.Mode().Perm()&0o077 != 0 || info.Size() > file.maxBytes {
		return privateFileIdentity{}, file.errUnsafe
	}
	return privateFileIdentity{
		modified: info.ModTime().UnixNano(),
		size:     info.Size(),
		inode:    uint64(status.Ino),
	}, nil
}
