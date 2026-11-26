package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataDirIsCreatedWithMode0700(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	directory, err := DataDir(home, true)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %o, want 700", info.Mode().Perm())
	}
}

func TestDataDirRejectsGroupOrOtherBits(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := DataDir(home, true); err != ErrDataDirUnsafe {
		t.Errorf("err = %v, want ErrDataDirUnsafe", err)
	}
}

func TestDataDirRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := DataDir(link, true); err != ErrDataDirUnsafe {
		t.Errorf("err = %v, want ErrDataDirUnsafe", err)
	}
}

func TestDataDirResolvesParentLinks(t *testing.T) {
	root, err := RealPath(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	directory, err := DataDir(filepath.Join(root, "alias", "home"), true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "home"); directory != want {
		t.Errorf("DataDir = %s, want %s", directory, want)
	}
}
