package tokens

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/paths"
)

// newDataDir returns a fresh private data directory.
func newDataDir(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "bridge")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

// writeSettings writes tokens.json with mode and returns its path.
func writeSettings(t *testing.T, directory, text string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(directory, SettingsFilename)
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// Ported from test_missing_tokens_config_returns_none.
func TestMissingSettingsReturnNil(t *testing.T) {
	settings, err := NewSettingsFile(newDataDir(t)).Load()
	if settings != nil || err != nil {
		t.Errorf("Load = %v, %v; want nil, nil", settings, err)
	}
}

// Ported from test_valid_tokens_config_resolves_models_case_insensitively.
func TestValidSettingsResolveModelsCaseInsensitively(t *testing.T) {
	directory := newDataDir(t)
	writeSettings(t, directory, `{
		"default_initial_tokens": 500000, "warn_percent": 80, "imbalance_gap_percent": 25,
		"models": {
			"claude-sonnet-4-6": {"initial_tokens": 200000, "warn_percent": 70, "reset_window_hours": 24},
			"gemini-2.5-pro": {"initial_tokens": 1000000}
		}}`, 0o600)
	settings, err := NewSettingsFile(directory).Load()
	if err != nil || settings == nil {
		t.Fatalf("Load = %v, %v", settings, err)
	}
	override := int64(150_000)
	integerCases := []struct {
		name string
		got  *int64
		want *int64
	}{
		{"initial exact", settings.InitialTokensFor("claude-sonnet-4-6", nil), pointer(200_000)},
		{"initial folded", settings.InitialTokensFor("CLAUDE-SONNET-4-6", nil), pointer(200_000)},
		{"initial override", settings.InitialTokensFor("claude-sonnet-4-6", &override), pointer(150_000)},
		{"initial default", settings.InitialTokensFor("unknown-model", nil), pointer(500_000)},
		{"window set", settings.ResetWindowSecondsFor("claude-sonnet-4-6"), pointer(86_400)},
		{"window unset", settings.ResetWindowSecondsFor("gemini-2.5-pro"), nil},
		{"warn model", pointer(settings.WarnPercentFor("claude-sonnet-4-6")), pointer(70)},
		{"warn global", pointer(settings.WarnPercentFor("gemini-2.5-pro")), pointer(80)},
	}
	for _, test := range integerCases {
		if !equalPointers(test.got, test.want) {
			t.Errorf("%s = %v, want %v", test.name, show(test.got), show(test.want))
		}
	}
}

// TestBudgetForUsesUnicodeCaseFolding matches Python's str.casefold, under which the
// Kelvin sign and the long s fold to ASCII letters.
func TestBudgetForUsesUnicodeCaseFolding(t *testing.T) {
	settings, err := ParseSettings([]byte(`{"models": {"k-ss": {"initial_tokens": 5}, "K-SS": {"initial_tokens": 6}}}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		model string
		want  *int64
	}{
		{"K-SS", pointer(6)}, {"k-ss", pointer(5)}, {"K-ſs", pointer(5)}, {"k-ß", pointer(5)}, {"", nil}, {"k", nil},
	}
	for _, test := range cases {
		budget := settings.BudgetFor(test.model)
		var got *int64
		if budget != nil {
			got = &budget.InitialTokens
		}
		if !equalPointers(got, test.want) {
			t.Errorf("BudgetFor(%q) = %v, want %v", test.model, show(got), show(test.want))
		}
	}
}

// Ported from test_rejects_world_readable_or_symlink_tokens_config, plus a directory,
// an oversized file and a dangling link.
func TestUnsafeSettingsFilesAreRejected(t *testing.T) {
	valid := `{"default_initial_tokens": 100000}`
	directory := newDataDir(t)
	path := writeSettings(t, directory, valid, 0o644)
	file := NewSettingsFile(directory)
	expectError(t, file, ErrSettingsUnsafe, "world readable")
	writeSettings(t, directory, valid, 0o640)
	expectError(t, file, ErrSettingsUnsafe, "group readable")
	writeSettings(t, directory, valid+strings.Repeat(" ", MaxSettingsBytes), 0o600)
	expectError(t, file, ErrSettingsUnsafe, "oversized")
	removeFile(t, path)
	target := filepath.Join(t.TempDir(), "real.json")
	if err := os.WriteFile(target, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	expectError(t, file, ErrSettingsUnsafe, "symbolic link")
	removeFile(t, path)
	if err := os.Symlink(filepath.Join(directory, "missing"), path); err != nil {
		t.Fatal(err)
	}
	expectError(t, file, ErrSettingsUnsafe, "dangling link")
	removeFile(t, path)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	expectError(t, file, ErrSettingsUnsafe, "directory")
}

// Ported from test_rejects_invalid_thresholds_or_unknown_fields.
func TestInvalidSettingsAreRejected(t *testing.T) {
	directory := newDataDir(t)
	file := NewSettingsFile(directory)
	for _, text := range []string{`{"warn_percent": 150}`, `{"unexpected": 1}`, `not json`} {
		writeSettings(t, directory, text, 0o600)
		expectError(t, file, ErrSettingsInvalid, text)
	}
	if ErrSettingsInvalid.Error() != "Invalid token configuration" || ErrSettingsUnsafe.Error() != "Token configuration must be a private file" {
		t.Error("error texts differ from Python")
	}
}

// TestSettingsCacheHitAndInvalidation checks fix L5: an unchanged file is parsed once, a
// change of modification time, size or inode is parsed again, and the safety checks run
// on every Load.
func TestSettingsCacheHitAndInvalidation(t *testing.T) {
	directory := newDataDir(t)
	path := writeSettings(t, directory, `{"warn_percent": 10}`, 0o600)
	file := NewSettingsFile(directory)
	first := mustLoad(t, file)
	if second := mustLoad(t, file); second != first {
		t.Fatal("unchanged file was parsed again")
	}
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	// Same size, same inode, same modification time: the cache still answers.
	rewriteInPlace(t, path, `{"warn_percent": 20}`, stamp)
	cached := mustLoad(t, file)
	rewriteInPlace(t, path, `{"warn_percent": 30}`, stamp)
	if mustLoad(t, file) != cached {
		t.Error("identical identity was parsed again")
	}
	// A new modification time.
	rewriteInPlace(t, path, `{"warn_percent": 40}`, stamp.Add(time.Second))
	if got := mustLoad(t, file).WarnPercent; got != 40 {
		t.Errorf("after mtime change warn_percent = %d, want 40", got)
	}
	// A new size with the same modification time.
	rewriteInPlace(t, path, `{"warn_percent": 50 }`, stamp.Add(time.Second))
	if got := mustLoad(t, file).WarnPercent; got != 50 {
		t.Errorf("after size change warn_percent = %d, want 50", got)
	}
	// A new inode with the same size and modification time.
	replacement := filepath.Join(directory, "next.json")
	writeSettings(t, directory, `{"warn_percent": 60 }`, 0o600)
	if err := os.Rename(path, replacement); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, directory, `{"warn_percent": 70 }`, 0o600)
	if err := os.Chtimes(path, stamp.Add(time.Second), stamp.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t, file).WarnPercent; got != 70 {
		t.Errorf("after inode change warn_percent = %d, want 70", got)
	}
	// The permission check runs even when the cache would answer.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	expectError(t, file, ErrSettingsUnsafe, "cached then world readable")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	mustLoad(t, file)
	removeFile(t, path)
	if settings, err := file.Load(); settings != nil || err != nil {
		t.Errorf("after removal Load = %v, %v", settings, err)
	}
}

// TestCachedParseFailureIsReturnedAgain keeps an invalid file invalid without reparsing.
func TestCachedParseFailureIsReturnedAgain(t *testing.T) {
	directory := newDataDir(t)
	writeSettings(t, directory, `{"warn_percent": 0}`, 0o600)
	file := NewSettingsFile(directory)
	expectError(t, file, ErrSettingsInvalid, "first load")
	expectError(t, file, ErrSettingsInvalid, "cached load")
}

// settingsCases are tokens.json documents with the outcome Python gives them.
var settingsCases = []struct {
	text  string
	valid bool
}{
	{`{}`, true},
	{`{"models": {}}`, true},
	{`{"default_initial_tokens": null}`, true},
	{`{"default_initial_tokens": 1000000000}`, true},
	{`{"default_initial_tokens": 1000000001}`, false},
	{`{"default_initial_tokens": 0}`, false},
	{`{"default_initial_tokens": 5.0}`, false},
	{`{"default_initial_tokens": "5"}`, false},
	{`{"default_initial_tokens": true}`, false},
	{`{"warn_percent": null}`, false},
	{`{"warn_percent": 1}`, true},
	{`{"warn_percent": 99}`, true},
	{`{"warn_percent": 100}`, false},
	{`{"imbalance_gap_percent": 4}`, false},
	{`{"imbalance_gap_percent": 90}`, true},
	{`{"imbalance_gap_percent": 1e1}`, false},
	{`{"models": null}`, false},
	{`{"models": []}`, false},
	{`{"models": {"m": null}}`, false},
	{`{"models": {"m": {}}}`, false},
	{`{"models": {"m": {"initial_tokens": 1}}}`, true},
	{`{"models": {"m": {"initial_tokens": 1, "warn_percent": null, "imbalance_gap_percent": null, "reset_window_hours": null}}}`, true},
	{`{"models": {"m": {"initial_tokens": 1, "reset_window_hours": 720}}}`, true},
	{`{"models": {"m": {"initial_tokens": 1, "reset_window_hours": 721}}}`, false},
	{`{"models": {"m": {"initial_tokens": 1, "extra": 1}}}`, false},
	{`{"models": {"bad name": {"initial_tokens": 1}}}`, false},
	{`{"models": {"m\n": {"initial_tokens": 1}}}`, false},
	{`{"models": {"": {"initial_tokens": 1}}}`, false},
	{`{"models": {"zai/glm-4.5:free_x": {"initial_tokens": 1}}}`, true},
	{`{"models": {"` + strings.Repeat("a", 128) + `": {"initial_tokens": 1}}}`, true},
	{`{"models": {"` + strings.Repeat("a", 129) + `": {"initial_tokens": 1}}}`, false},
	{`{"models": {"é": {"initial_tokens": 1}}}`, false},
	{`{"warn_percent": 10, "warn_percent": 20}`, true},
	{`[]`, false},
	{`null`, false},
	{``, false},
	{`{"warn_percent": 10} x`, false},
}

func TestParseSettingsCases(t *testing.T) {
	for _, test := range settingsCases {
		_, err := ParseSettings([]byte(test.text))
		if (err == nil) != test.valid {
			t.Errorf("ParseSettings(%s) err = %v, want valid %v", test.text, err, test.valid)
		}
		if err != nil && !errors.Is(err, ErrSettingsInvalid) {
			t.Errorf("ParseSettings(%s) err = %v, want ErrSettingsInvalid", test.text, err)
		}
	}
}

// pythonValidate prints, for each JSON document in a JSON list, whether TokenSettings
// accepts it.
const pythonValidate = `
import json, sys
from pydantic import ValidationError
from agentcy.tokens.settings import TokenSettings
out = []
for text in json.load(sys.stdin):
    try:
        TokenSettings.model_validate_json(text)
        out.append(True)
    except ValidationError:
        out.append(False)
print(json.dumps(out))
`

// TestSettingsCasesMatchPython checks the outcomes in settingsCases against pydantic
// through uv, and skips when uv is unavailable.
func TestSettingsCasesMatchPython(t *testing.T) {
	pathValue, pathSet := os.LookupEnv("PATH")
	uv := paths.FindOnPath("uv", pathValue, pathSet)
	if uv == "" {
		t.Skip("uv is unavailable")
	}
	texts := make([]string, 0, len(settingsCases))
	for _, test := range settingsCases {
		texts = append(texts, test.text)
	}
	input, _ := json.Marshal(texts)
	repository, _ := filepath.Abs("../..")
	command := exec.Command(uv, "run", "--locked", "--project", repository, "python", "-I", "-c", pythonValidate)
	command.Env = os.Environ()
	command.Stdin = strings.NewReader(string(input))
	output, err := command.Output()
	if err != nil {
		t.Skipf("Python unavailable: %v", err)
	}
	var outcomes []bool
	if err := json.Unmarshal(output, &outcomes); err != nil {
		t.Fatal(err)
	}
	for index, test := range settingsCases {
		if outcomes[index] != test.valid {
			t.Errorf("%s: Python valid %v, table says %v", test.text, outcomes[index], test.valid)
		}
	}
}

// mustLoad loads settings that must be valid.
func mustLoad(t *testing.T, file *SettingsFile) *Settings {
	t.Helper()
	settings, err := file.Load()
	if err != nil || settings == nil {
		t.Fatalf("Load = %v, %v", settings, err)
	}
	return settings
}

// expectError loads settings that must fail with want.
func expectError(t *testing.T, file *SettingsFile, want error, label string) {
	t.Helper()
	settings, err := file.Load()
	if settings != nil || !errors.Is(err, want) {
		t.Errorf("%s: Load = %v, %v; want %v", label, settings, err, want)
	}
}

// rewriteInPlace overwrites path without changing its inode and sets its times.
func rewriteInPlace(t *testing.T, path, text string, stamp time.Time) {
	t.Helper()
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

// removeFile removes path or fails the test.
func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// pointer returns a pointer to number.
func pointer(number int64) *int64 {
	return &number
}

// equalPointers compares two optional integers.
func equalPointers(left, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

// show formats an optional integer for a failure message.
func show(number *int64) any {
	if number == nil {
		return nil
	}
	return *number
}
