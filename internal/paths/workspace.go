package paths

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitTimeout bounds the one Git call that finds the common directory of a checkout.
const gitTimeout = 5 * time.Second

// ErrNotDirectory is returned for a workspace path that is not a directory.
var ErrNotDirectory = errors.New("Workspace must be a directory")

// gitVariables are removed from Git's environment so that the caller's checkout settings
// cannot redirect the lookup to another repository.
var gitVariables = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"}

// WorkspaceKey returns the storage key of a workspace: the SHA-256 hex digest of the
// canonical path of its Git common directory, or of the directory itself outside Git.
// Linked worktrees of one repository therefore share a key. environment is the process
// environment in os.Environ form; it supplies PATH and Git's environment.
//
// Port of workspace_key in src/agentcy/storage/database.py. Python spells the path in
// UTF-8 with surrogate escapes; Go hashes the same bytes.
func WorkspaceKey(workspace string, environment []string) (string, error) {
	root, err := RealPath(workspace, true)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", ErrNotDirectory
	}
	pathValue, pathSet := lookupVariable(environment, "PATH")
	if git := FindOnPath("git", pathValue, pathSet); git != "" {
		common, found, err := gitCommonDirectory(git, root, environment)
		if err != nil {
			return "", err
		}
		if found {
			root, err = RealPath(joinWorkspacePath(root, common), false)
			if err != nil {
				return "", err
			}
		}
	}
	digest := sha256.Sum256([]byte(root))
	return hex.EncodeToString(digest[:]), nil
}

// gitCommonDirectory runs `git -C root rev-parse --git-common-dir` and returns its output
// without trailing newlines. found is false when Git exits with an error, for example
// outside a repository.
func gitCommonDirectory(git, root string, environment []string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, git, "-C", root, "rev-parse", "--git-common-dir")
	command.Env = withoutVariables(environment, gitVariables)
	output, err := command.Output()
	if ctx.Err() != nil {
		return "", false, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimRight(string(output), "\n"), true, nil
}

// joinWorkspacePath joins Git's answer to the checkout like pathlib does: an absolute
// answer replaces the checkout path, and an empty answer leaves it unchanged.
func joinWorkspacePath(root, common string) string {
	if strings.HasPrefix(common, "/") {
		return common
	}
	if common == "" {
		return root
	}
	return root + "/" + common
}

// lookupVariable returns the value of one variable of an os.Environ style list and
// whether it is set. The last assignment wins, as in a process environment.
func lookupVariable(environment []string, name string) (string, bool) {
	value, found := "", false
	for _, entry := range environment {
		key, entryValue, ok := strings.Cut(entry, "=")
		if ok && key == name {
			value, found = entryValue, true
		}
	}
	return value, found
}

// withoutVariables returns a copy of environment without the named variables.
func withoutVariables(environment []string, names []string) []string {
	kept := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if !containsString(names, key) {
			kept = append(kept, entry)
		}
	}
	return kept
}

// containsString reports whether values holds value.
func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
