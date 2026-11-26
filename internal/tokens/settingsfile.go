package tokens

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// SettingsFilename is the name of the token settings file inside the data directory.
const SettingsFilename = "tokens.json"

// SettingsFile loads tokens.json and keeps the parsed result while the file's
// modification time, size and inode stay the same (port plan fix L5). The file's safety
// checks run on every Load. It is safe for concurrent use.
type SettingsFile struct {
	path   string
	mutex  sync.Mutex
	cached *loadedSettings
}

// loadedSettings is the result of parsing one version of the file.
type loadedSettings struct {
	identity fileIdentity
	settings *Settings
	err      error
}

// fileIdentity tells whether the file changed since it was parsed.
type fileIdentity struct {
	modified int64
	size     int64
	inode    uint64
}

// NewSettingsFile returns the loader of tokens.json inside dataDir, the directory that
// paths.DataDir returns.
func NewSettingsFile(dataDir string) *SettingsFile {
	return &SettingsFile{path: filepath.Join(dataDir, SettingsFilename)}
}

// Load returns the settings, or nil and no error when tokens.json does not exist. It
// returns ErrSettingsUnsafe for anything but a regular file that belongs to the user,
// has no group or other permission bits and is at most MaxSettingsBytes, and
// ErrSettingsInvalid for a file that cannot be read or validated (port of read_settings).
//
// Python's status call logs a warning and continues without settings on either error;
// callers that port it treat an error as nil settings.
func (file *SettingsFile) Load() (*Settings, error) {
	file.mutex.Lock()
	defer file.mutex.Unlock()
	info, err := os.Lstat(file.path)
	if errors.Is(err, fs.ErrNotExist) {
		file.cached = nil
		return nil, nil
	}
	if err != nil {
		return nil, ErrSettingsInvalid
	}
	identity, err := privateFileIdentity(info)
	if err != nil {
		return nil, err
	}
	if file.cached != nil && file.cached.identity == identity {
		return file.cached.settings, file.cached.err
	}
	loaded, err := file.read()
	if err != nil {
		return nil, err
	}
	if loaded == nil {
		file.cached = nil
		return nil, nil
	}
	file.cached = loaded
	return loaded.settings, loaded.err
}

// read opens the file without following a symbolic link, checks the opened file again
// (it may have been replaced since the Lstat) and parses it. It returns nil when the file
// is gone. The returned error is a failure to get the file; a parse failure is kept in
// loadedSettings for the cache.
func (file *SettingsFile) read() (*loadedSettings, error) {
	handle, err := os.OpenFile(file.path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if errors.Is(err, syscall.ELOOP) {
		return nil, ErrSettingsUnsafe
	}
	if err != nil {
		return nil, ErrSettingsInvalid
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, ErrSettingsInvalid
	}
	identity, err := privateFileIdentity(info)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(handle, MaxSettingsBytes+1))
	if err != nil {
		return nil, ErrSettingsInvalid
	}
	if len(data) > MaxSettingsBytes {
		return nil, ErrSettingsUnsafe
	}
	settings, err := ParseSettings(data)
	return &loadedSettings{identity: identity, settings: settings, err: err}, nil
}

// privateFileIdentity checks that info describes a regular file owned by the user with
// no group or other permission bits and at most MaxSettingsBytes, and returns its
// identity.
func privateFileIdentity(info fs.FileInfo) (fileIdentity, error) {
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || int(status.Uid) != os.Getuid() ||
		info.Mode().Perm()&0o077 != 0 || info.Size() > MaxSettingsBytes {
		return fileIdentity{}, ErrSettingsUnsafe
	}
	return fileIdentity{modified: info.ModTime().UnixNano(), size: info.Size(), inode: uint64(status.Ino)}, nil
}
