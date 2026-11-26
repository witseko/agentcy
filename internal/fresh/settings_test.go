package fresh

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSettings writes plugins.json into dataDir with mode.
func writeSettings(t *testing.T, dataDir, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dataDir, SettingsFilename)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestMissingSettingsFileMeansNoPlugins(t *testing.T) {
	settings, err := NewSettingsFile(t.TempDir()).Load()
	if settings != nil || err != nil {
		t.Errorf("Load() = %v, %v; want nil, nil", settings, err)
	}
}

func TestSettingsAreSortedWithDefaults(t *testing.T) {
	dataDir := t.TempDir()
	writeSettings(t, dataDir, `{"plugins": {
		"zeta": {"command": ["/bin/zeta"], "model": "z/model"},
		"alpha": {"command": ["/bin/alpha", "--flag"], "model": "a/model", "timeout_seconds": 60, "token_cap": 5, "environment": ["API_KEY"]}}}`, 0o600)
	settings, err := NewSettingsFile(dataDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Plugins) != 2 || settings.Plugins[0].Name != "alpha" || settings.Plugins[1].Name != "zeta" {
		t.Fatalf("plugins = %+v", settings.Plugins)
	}
	if settings.Plugins[1].TimeoutSeconds != defaultTimeoutSeconds || settings.Plugins[0].TimeoutSeconds != 60 {
		t.Errorf("timeouts = %d, %d", settings.Plugins[0].TimeoutSeconds, settings.Plugins[1].TimeoutSeconds)
	}
	if settings.Find("alpha") == nil || settings.Find("missing") != nil {
		t.Error("Find does not resolve plugins by name")
	}
}

func TestUnsafeOrInvalidSettingsAreRefused(t *testing.T) {
	cases := []struct {
		name    string
		content string
		mode    os.FileMode
		want    error
	}{
		{"group readable", `{"plugins": {}}`, 0o640, ErrSettingsUnsafe},
		{"relative command", `{"plugins": {"a": {"command": ["relative"], "model": "m"}}}`, 0o600, ErrSettingsInvalid},
		{"unknown field", `{"plugins": {"a": {"command": ["/bin/a"], "model": "m", "extra": 1}}}`, 0o600, ErrSettingsInvalid},
		{"bad name", `{"plugins": {"a b": {"command": ["/bin/a"], "model": "m"}}}`, 0o600, ErrSettingsInvalid},
		{"timeout out of range", `{"plugins": {"a": {"command": ["/bin/a"], "model": "m", "timeout_seconds": 3601}}}`, 0o600, ErrSettingsInvalid},
		{"bad environment name", `{"plugins": {"a": {"command": ["/bin/a"], "model": "m", "environment": ["lower"]}}}`, 0o600, ErrSettingsInvalid},
		{"not JSON", `{not json`, 0o600, ErrSettingsInvalid},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dataDir := t.TempDir()
			writeSettings(t, dataDir, testCase.content, testCase.mode)
			if _, err := NewSettingsFile(dataDir).Load(); !errors.Is(err, testCase.want) {
				t.Errorf("Load() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestSymbolicLinkIsRefused(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "real.json")
	if err := os.WriteFile(target, []byte(`{"plugins": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dataDir, SettingsFilename)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSettingsFile(dataDir).Load(); !errors.Is(err, ErrSettingsUnsafe) {
		t.Errorf("Load() error = %v, want ErrSettingsUnsafe", err)
	}
}

func TestSafetyChecksRunOnEveryLoadAndChangesAreSeen(t *testing.T) {
	dataDir := t.TempDir()
	file := NewSettingsFile(dataDir)
	writeSettings(t, dataDir, `{"plugins": {"a": {"command": ["/bin/a"], "model": "one"}}}`, 0o600)
	if settings, err := file.Load(); err != nil || settings.Plugins[0].Model != "one" {
		t.Fatalf("first Load() = %v, %v", settings, err)
	}
	path := filepath.Join(dataDir, SettingsFilename)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Load(); !errors.Is(err, ErrSettingsUnsafe) {
		t.Errorf("a cached file that became readable by others: err = %v", err)
	}
	writeSettings(t, dataDir, `{"plugins": {"a": {"command": ["/bin/a"], "model": "two!"}}}`, 0o600)
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if settings, err := file.Load(); err != nil || settings.Plugins[0].Model != "two!" {
		t.Errorf("Load() after a change = %v, %v", settings, err)
	}
}
