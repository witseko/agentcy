//go:build bench

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// session owns the temporary directories and every process the benchmark starts.
type session struct {
	cfg config
	// home is the temporary AGENTCY_HOME. It sits directly under /tmp because a Unix
	// socket path is limited to about 104 bytes on macOS.
	home         string
	workspace    string
	workspaceKey string
	// gitLog counts the daemon's git spawns, one line each, through a PATH wrapper.
	// It is empty when git was not found, and the count is then not measured.
	gitLog     string
	wrapperDir string

	daemon *daemonProcess

	mu    sync.Mutex
	shims map[*shimProcess]bool
}

// newSession creates the private home and workspace directories and the git wrapper.
func newSession(cfg config) (*session, error) {
	home, err := os.MkdirTemp("/tmp", "acb")
	if err != nil {
		return nil, fmt.Errorf("create AGENTCY_HOME: %w", err)
	}
	s := &session{cfg: cfg, home: home, shims: map[*shimProcess]bool{}}
	if err := os.Chmod(home, 0o700); err != nil {
		s.close()
		return nil, err
	}
	s.workspace, err = os.MkdirTemp("", "agentcy-bench-workspace")
	if err != nil {
		s.close()
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	s.workspaceKey, err = workspaceKey(s.workspace)
	if err != nil {
		s.close()
		return nil, err
	}
	if err := s.installGitWrapper(); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// workspaceKey reproduces the core's workspace key: the SHA-256 of the resolved Git common
// directory, or of the resolved directory itself outside a repository. The dataset step
// checks the result against the daemon, which derives the key from the directory.
func workspaceKey(directory string) (string, error) {
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	if git, err := exec.LookPath("git"); err == nil {
		command := exec.Command(git, "-C", root, "rev-parse", "--git-common-dir")
		command.Env = environmentWithout("GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR")
		if output, err := command.Output(); err == nil {
			common := strings.TrimRight(string(output), "\n")
			if !filepath.IsAbs(common) {
				common = filepath.Join(root, common)
			}
			if root, err = filepath.EvalSymlinks(common); err != nil {
				return "", err
			}
		}
	}
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:]), nil
}

// installGitWrapper writes a `git` script that logs one line per call and then runs the
// real git. Only the daemon's PATH starts with the wrapper directory.
func (s *session) installGitWrapper() error {
	realGit, err := exec.LookPath("git")
	if err != nil {
		return nil
	}
	realGit, err = filepath.Abs(realGit)
	if err != nil {
		return err
	}
	s.wrapperDir = filepath.Join(s.home, "bench-bin")
	gitLog := filepath.Join(s.home, "git-spawns.log")
	if strings.ContainsRune(realGit+gitLog, '\'') {
		// The script quotes both paths in single quotes.
		return nil
	}
	if err := os.Mkdir(s.wrapperDir, 0o700); err != nil {
		return err
	}
	script := fmt.Sprintf("#!/bin/sh\necho spawn >> '%s'\nexec '%s' \"$@\"\n", gitLog, realGit)
	if err := os.WriteFile(filepath.Join(s.wrapperDir, "git"), []byte(script), 0o700); err != nil {
		return err
	}
	s.gitLog = gitLog
	return nil
}

// gitSpawns returns how many times the daemon ran git so far, or -1 when not measured.
func (s *session) gitSpawns() int {
	if s.gitLog == "" {
		return -1
	}
	content, err := os.ReadFile(s.gitLog)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		return -1
	}
	return strings.Count(string(content), "\n")
}

// socketPath returns the daemon socket inside the temporary home.
func (s *session) socketPath() string {
	return filepath.Join(s.home, "core.sock")
}

// databasePath returns the workspace store the daemon creates for the temporary workspace.
func (s *session) databasePath() string {
	return filepath.Join(s.home, s.workspaceKey+".sqlite3")
}

// processEnvironment returns this process's environment with AGENTCY_HOME replaced, so no
// child ever touches the user's real ~/.agentcy.
func (s *session) processEnvironment() []string {
	return append(environmentWithout("AGENTCY_HOME"), "AGENTCY_HOME="+s.home)
}

// daemonEnvironment adds the git wrapper directory in front of PATH.
func (s *session) daemonEnvironment() []string {
	environment := environmentWithout("AGENTCY_HOME", "PATH")
	path := os.Getenv("PATH")
	if s.wrapperDir != "" {
		path = s.wrapperDir + string(os.PathListSeparator) + path
	}
	return append(environment, "AGENTCY_HOME="+s.home, "PATH="+path)
}

// environmentWithout returns this process's environment minus the named variables.
func environmentWithout(names ...string) []string {
	var kept []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		drop := false
		for _, unwanted := range names {
			if name == unwanted {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, entry)
		}
	}
	return kept
}

// trackShim records a live shim so close can stop it after an error.
func (s *session) trackShim(shim *shimProcess, live bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if live {
		s.shims[shim] = true
	} else {
		delete(s.shims, shim)
	}
}

// close stops every shim and daemon the benchmark started and removes the temporary
// directories. It reports, but does not fail on, processes it could not stop.
func (s *session) close() {
	s.mu.Lock()
	shims := make([]*shimProcess, 0, len(s.shims))
	for shim := range s.shims {
		shims = append(shims, shim)
	}
	s.mu.Unlock()
	for _, shim := range shims {
		shim.stop()
	}
	if err := s.stopDaemon(); err != nil {
		fmt.Fprintln(os.Stderr, "bench: cleanup:", err)
	}
	if s.workspace != "" {
		os.RemoveAll(s.workspace)
	}
	os.RemoveAll(s.home)
}
