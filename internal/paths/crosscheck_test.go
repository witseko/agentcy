package paths

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pythonWorkspaceKey runs the Python workspace_key through uv, or skips the test when uv
// or the project environment is unavailable.
func pythonWorkspaceKey(t *testing.T, workspace string) string {
	t.Helper()
	pathValue, pathSet := os.LookupEnv("PATH")
	uv := FindOnPath("uv", pathValue, pathSet)
	if uv == "" {
		t.Skip("uv is unavailable")
	}
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(uv, "run", "--locked", "--project", repository, "python", "-c",
		"import sys; from pathlib import Path; from agentcy.storage.database import workspace_key; print(workspace_key(Path(sys.argv[1])))",
		workspace)
	command.Env = os.Environ()
	output, err := command.Output()
	if err != nil {
		t.Skipf("Python workspace_key unavailable: %v", err)
	}
	return strings.TrimSpace(string(output))
}

// TestWorkspaceKeyMatchesPython compares the Go and Python keys for a plain directory, a
// Git repository, a linked worktree, and a path through a symbolic link.
func TestWorkspaceKeyMatchesPython(t *testing.T) {
	git, project, _ := gitRepositories(t)
	root := filepath.Dir(project)
	plain := filepath.Join(root, "plain")
	if err := os.Mkdir(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	runGit(t, git, project, "worktree", "add", "--orphan", "-b", "linked", linked)
	symlinked := filepath.Join(root, "symlinked")
	if err := os.Symlink(linked, symlinked); err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{plain, project, linked, symlinked, symlinked + "/../plain"} {
		want := pythonWorkspaceKey(t, workspace)
		if got := mustKey(t, workspace, os.Environ()); got != want {
			t.Errorf("%s: Go key %s, Python key %s", workspace, got, want)
		}
	}
}
