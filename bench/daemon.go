//go:build bench

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	daemonStartTimeout = 30 * time.Second
	processStopTimeout = 10 * time.Second
)

// daemonProcess is a daemon the benchmark started from AGENTCY_CORE_CMD.
type daemonProcess struct {
	command *exec.Cmd
	log     *os.File
	exited  chan struct{}
}

// processGroup returns the daemon's process group, which holds the command and its children.
func (d *daemonProcess) processGroup() int {
	return d.command.Process.Pid
}

// startDaemon runs AGENTCY_CORE_CMD in its own process group and waits for its socket.
func (s *session) startDaemon() error {
	if s.daemon != nil {
		return errors.New("daemon already running")
	}
	log, err := os.OpenFile(filepath.Join(s.home, "bench-core.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	command := exec.Command(s.cfg.coreCommand[0], s.cfg.coreCommand[1:]...)
	command.Env = s.daemonEnvironment()
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		log.Close()
		return fmt.Errorf("start daemon: %w", err)
	}
	daemon := &daemonProcess{command: command, log: log, exited: make(chan struct{})}
	go func() {
		command.Wait()
		close(daemon.exited)
	}()
	s.daemon = daemon
	if err := waitForSocket(s.socketPath(), daemon.exited); err != nil {
		s.stopDaemon()
		return fmt.Errorf("%w; daemon log: %s", err, logTail(log.Name()))
	}
	return nil
}

// waitForSocket polls the socket until a connection succeeds, the daemon exits, or time runs out.
func waitForSocket(path string, exited <-chan struct{}) error {
	deadline := time.Now().Add(daemonStartTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return errors.New("daemon exited before it opened its socket")
		default:
		}
		connection, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			connection.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("daemon did not open its socket in time")
}

// stopDaemon stops the daemon the benchmark started and any other process that holds the
// daemon lock in the temporary home, such as a daemon a shim started.
func (s *session) stopDaemon() error {
	if s.daemon != nil {
		daemon := s.daemon
		s.daemon = nil
		syscall.Kill(-daemon.processGroup(), syscall.SIGTERM)
		select {
		case <-daemon.exited:
		case <-time.After(processStopTimeout):
			syscall.Kill(-daemon.processGroup(), syscall.SIGKILL)
			<-daemon.exited
		}
		daemon.log.Close()
	}
	return s.stopLockHolders()
}

// lockHolders lists the processes that hold core.lock open. Every daemon holds this lock
// while it runs, whichever implementation and whoever started it.
func (s *session) lockHolders() ([]int, error) {
	lockPath := filepath.Join(s.home, "core.lock")
	if _, err := os.Stat(lockPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	output, err := exec.Command("lsof", "-t", lockPath).Output()
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 && len(output) == 0 {
		// lsof exits with 1 when no process has the file open.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lsof is needed to find the daemon: %w", err)
	}
	var pids []int
	for _, field := range strings.Fields(string(output)) {
		pid, err := strconv.Atoi(field)
		if err == nil && pid != os.Getpid() {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// stopLockHolders terminates every process holding core.lock and waits until each is gone.
func (s *session) stopLockHolders() error {
	pids, err := s.lockHolders()
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if err := stopProcess(pid); err != nil {
			return err
		}
	}
	return nil
}

// stopProcess sends SIGTERM, then SIGKILL after a timeout, to a process this program did
// not start directly, and waits until the process no longer exists.
func stopProcess(pid int) error {
	syscall.Kill(pid, syscall.SIGTERM)
	if waitUntilGone(pid, processStopTimeout) {
		return nil
	}
	syscall.Kill(pid, syscall.SIGKILL)
	if waitUntilGone(pid, processStopTimeout) {
		return nil
	}
	return fmt.Errorf("process %d did not stop", pid)
}

// waitUntilGone polls until the process no longer exists or the timeout passes.
func waitUntilGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// logTail returns the last lines of a log file for an error message.
func logTail(path string) string {
	content, err := os.ReadFile(path)
	if err != nil || len(content) == 0 {
		return "(empty)"
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}
