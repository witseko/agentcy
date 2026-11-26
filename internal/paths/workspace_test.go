package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitForTests returns the installed Git or skips the test.
func gitForTests(t *testing.T) string {
	t.Helper()
	pathValue, pathSet := os.LookupEnv("PATH")
	git := FindOnPath("git", pathValue, pathSet)
	if git == "" {
		t.Skip("Git is unavailable")
	}
	return git
}

// runGit runs a Git fixture command with no inherited repository or user config.
func runGit(t *testing.T, git, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command(git, append([]string{"-C", directory}, arguments...)...)
	command.Env = []string{"PATH=/bin:/usr/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return string(output)
}

// realTempDir returns a temporary directory with symbolic links resolved.
func realTempDir(t *testing.T) string {
	t.Helper()
	directory, err := RealPath(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

// gitRepositories creates two independent empty repositories.
func gitRepositories(t *testing.T) (string, string, string) {
	t.Helper()
	git := gitForTests(t)
	root := realTempDir(t)
	project, other := filepath.Join(root, "project"), filepath.Join(root, "other")
	for _, repository := range []string{project, other} {
		if err := os.Mkdir(repository, 0o700); err != nil {
			t.Fatal(err)
		}
		runGit(t, git, repository, "init", "--quiet", "--template=")
	}
	return git, project, other
}

// repositoryEnvironment returns an environment whose Git variables point at another
// legitimate repository.
func repositoryEnvironment(other string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_DIR=" + filepath.Join(other, ".git"),
		"GIT_WORK_TREE=" + other,
		"GIT_COMMON_DIR=" + filepath.Join(other, ".git"),
	}
}

// keyOf returns the expected key of a canonical path.
func keyOf(path string) string {
	digest := sha256.Sum256([]byte(path))
	return hex.EncodeToString(digest[:])
}

// mustKey computes a workspace key or fails the test.
func mustKey(t *testing.T, workspace string, environment []string) string {
	t.Helper()
	key, err := WorkspaceKey(workspace, environment)
	if err != nil {
		t.Fatalf("WorkspaceKey(%q): %v", workspace, err)
	}
	return key
}

func TestWorkspaceKeyUsesTheRequestedRepository(t *testing.T) {
	_, project, other := gitRepositories(t)
	environment := repositoryEnvironment(other)
	if got, want := mustKey(t, project, environment), keyOf(filepath.Join(project, ".git")); got != want {
		t.Errorf("key = %s, want %s", got, want)
	}
	if mustKey(t, project, environment) == mustKey(t, other, environment) {
		t.Error("two repositories share a key")
	}
}

func TestLinkedWorktreeSharesRequestedRepositoryKey(t *testing.T) {
	git, project, other := gitRepositories(t)
	command := exec.Command(git, "-C", project, "worktree", "add", "-h")
	command.Env = []string{"PATH=/bin:/usr/bin"}
	help, _ := command.CombinedOutput()
	if !strings.Contains(string(help), "--orphan") {
		t.Skip("Git lacks worktree add --orphan")
	}
	linked := filepath.Join(filepath.Dir(project), "linked")
	runGit(t, git, project, "worktree", "add", "--orphan", "-b", "linked", linked)
	environment := repositoryEnvironment(other)
	if mustKey(t, linked, environment) != mustKey(t, project, environment) {
		t.Error("linked worktree and main checkout have different keys")
	}
	if mustKey(t, linked, environment) == mustKey(t, other, environment) {
		t.Error("linked worktree shares the other repository's key")
	}
}

func TestWorkspaceKeyFallsBackToCanonicalPath(t *testing.T) {
	_, project, other := gitRepositories(t)
	directory := filepath.Join(filepath.Dir(project), "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := mustKey(t, directory, repositoryEnvironment(other)); got != keyOf(directory) {
		t.Errorf("key = %s, want the digest of %s", got, directory)
	}
}

func TestWorkspaceKeyWithoutGitUsesCanonicalPath(t *testing.T) {
	directory := realTempDir(t)
	if got := mustKey(t, directory, []string{"PATH="}); got != keyOf(directory) {
		t.Errorf("key = %s, want the digest of %s", got, directory)
	}
}

func TestWorkspaceKeyResolvesSymbolicLinks(t *testing.T) {
	root := realTempDir(t)
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if got := mustKey(t, link, []string{"PATH="}); got != keyOf(target) {
		t.Errorf("key through a link = %s, want the target's key", got)
	}
}

func TestWorkspaceKeyRejectsFilesAndMissingPaths(t *testing.T) {
	root := realTempDir(t)
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkspaceKey(file, nil); err != ErrNotDirectory {
		t.Errorf("file: err = %v, want ErrNotDirectory", err)
	}
	if _, err := WorkspaceKey(filepath.Join(root, "missing"), nil); err == nil {
		t.Error("missing path: no error")
	}
}

func TestRealPathClimbsFromTheLinkTarget(t *testing.T) {
	root := realTempDir(t)
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(deep, link); err != nil {
		t.Fatal(err)
	}
	got, err := RealPath(link+"/..", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "a"); got != want {
		t.Errorf("RealPath(link/..) = %s, want %s (Python climbs from the target)", got, want)
	}
	missing, err := RealPath(filepath.Join(root, "missing", "child"), false)
	if err != nil || missing != filepath.Join(root, "missing", "child") {
		t.Errorf("non-strict missing path = %q, %v", missing, err)
	}
	if _, err := RealPath(filepath.Join(root, "missing"), true); err == nil {
		t.Error("strict missing path: no error")
	}
}
