package tokens

import (
	"path/filepath"

	"github.com/witseko/agentcy/internal/paths"
)

// SettingsFilename is the name of the token settings file inside the data directory.
const SettingsFilename = "tokens.json"

// SettingsFile loads tokens.json and keeps the parsed result while the file's
// modification time, size and inode stay the same (port plan fix L5). The file's safety
// checks run on every Load. It is safe for concurrent use.
type SettingsFile struct {
	file *paths.PrivateConfigFile[*Settings]
}

// NewSettingsFile returns the loader of tokens.json inside dataDir, the directory that
// paths.DataDir returns.
func NewSettingsFile(dataDir string) *SettingsFile {
	return &SettingsFile{
		file: paths.NewPrivateConfigFile(
			filepath.Join(dataDir, SettingsFilename),
			MaxSettingsBytes,
			ErrSettingsUnsafe,
			ErrSettingsInvalid,
			ParseSettings,
		),
	}
}

// Load returns the settings, or nil and no error when tokens.json does not exist.
func (file *SettingsFile) Load() (*Settings, error) {
	return file.file.Load()
}
