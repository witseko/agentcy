package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// touchExecutable creates an empty executable file.
func touchExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestFindOnPathPreservesAbsoluteOrderAndSymlink(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, directory := range []string{first, second} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(second, "example")
	touchExecutable(t, executable)
	alias := filepath.Join(first, "example")
	if err := os.Symlink(executable, alias); err != nil {
		t.Fatal(err)
	}
	touchExecutable(t, filepath.Join(root, "example"))
	t.Chdir(root)
	pathValue := strings.Join([]string{"", "second", first, second}, ":")
	if got := FindOnPath("example", pathValue, true); got != alias {
		t.Errorf("FindOnPath = %q, want %q", got, alias)
	}
}

func TestFindOnPathRequiresAnAbsoluteDirectory(t *testing.T) {
	for _, pathValue := range []string{"", "bin", ":.:bin"} {
		root := t.TempDir()
		t.Chdir(root)
		touchExecutable(t, filepath.Join(root, "example"))
		if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
			t.Fatal(err)
		}
		touchExecutable(t, filepath.Join(root, "bin", "example"))
		if got := FindOnPath("example", pathValue, true); got != "" {
			t.Errorf("PATH %q: found %q", pathValue, got)
		}
		if got := FindOnPath("./example", pathValue, true); got != "" {
			t.Errorf("PATH %q: found %q for a relative name", pathValue, got)
		}
	}
}

func TestFindOnPathUsesTheOSDefaultWhenPathIsUnset(t *testing.T) {
	if got := AbsoluteSearchPath("", false); strings.Join(got, ":") != defaultSearchPath {
		t.Errorf("unset PATH searches %v, want %s", got, defaultSearchPath)
	}
}

func TestFindOnPathSkipsDirectoriesAndNonExecutables(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "example"), 0o700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "example"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := FindOnPath("example", root+":"+other, true); got != "" {
		t.Errorf("found %q", got)
	}
}
