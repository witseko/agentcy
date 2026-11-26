package wire

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// CoreBinVariable selects another core executable, run as `<path> core`.
const CoreBinVariable = "AGENTCY_CORE_BIN"

// Start timing, from core_connection in src/agentcy/mcp/transport.py.
const (
	startDeadline     = 8 * time.Second
	startPollInterval = 50 * time.Millisecond
)

// The startup errors. They never contain the configured value.
var (
	ErrInvalidCoreBin = errors.New("Bridge startup failed. AGENTCY_CORE_BIN must be an absolute path to a file.")
	ErrNoExecutable   = errors.New("Bridge startup failed. The path of this program is unknown; set AGENTCY_CORE_BIN.")
)

// CoreCommand returns the argument list that starts the core daemon.
//
// AGENTCY_CORE_BIN (coreBin, when coreBinSet) must be an absolute path to an existing
// regular file and runs as `<path> core`; anything else fails closed. Without it the
// command is this program itself, `<executable> core`, where executable is this
// program's own path with symbolic links resolved.
func CoreCommand(coreBin string, coreBinSet bool, executable string) ([]string, error) {
	if coreBinSet {
		info, err := os.Stat(coreBin)
		if !filepath.IsAbs(coreBin) || err != nil || !info.Mode().IsRegular() {
			return nil, ErrInvalidCoreBin
		}
		return []string{coreBin, "core"}, nil
	}
	if executable == "" {
		return nil, ErrNoExecutable
	}
	return []string{executable, "core"}, nil
}

// Connector opens connections to the core in one data directory and starts the core on
// demand, serialised by a lock file so that several shims starting together spawn one.
type Connector struct {
	// DataDir is the checked bridge data directory.
	DataDir string
	// Environment is the shim's environment in os.Environ form. The core inherits it,
	// with AGENTCY_HOME set to DataDir.
	Environment []string
	// Command returns the core's argument list; it is called only when a start is needed.
	Command func() ([]string, error)
}

// Connect returns a connection to the running core, starting the core when no core
// listens. Port of core_connection in src/agentcy/mcp/transport.py.
func (connector *Connector) Connect() (net.Conn, error) {
	socketPath := filepath.Join(connector.DataDir, "core.sock")
	connection, err := net.Dial("unix", socketPath)
	if err == nil || !isNotListening(err) {
		return connection, err
	}
	lock, err := os.OpenFile(filepath.Join(connector.DataDir, "start.lock"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	connection, err = net.Dial("unix", socketPath)
	if err == nil || !isNotListening(err) {
		return connection, err
	}
	return connector.startCore(socketPath)
}

// startCore spawns the core in its own session and waits up to 8 seconds for its socket.
func (connector *Connector) startCore(socketPath string) (net.Conn, error) {
	arguments, err := connector.Command()
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(connector.DataDir, "core.log")
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	command := exec.Command(arguments[0], arguments[1:]...)
	command.Env = withVariable(connector.Environment, "AGENTCY_HOME", connector.DataDir)
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("Bridge startup failed. See %s", logPath)
	}
	exited := make(chan struct{})
	// The core outlives this call; waiting in the background reaps it if it exits.
	go func() {
		_ = command.Wait()
		close(exited)
	}()
	deadline := time.Now().Add(startDeadline)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return nil, fmt.Errorf("Bridge startup failed. See %s", logPath)
		default:
		}
		connection, err := net.Dial("unix", socketPath)
		if err == nil {
			return connection, nil
		}
		if !isNotListening(err) {
			return nil, err
		}
		time.Sleep(startPollInterval)
	}
	return nil, fmt.Errorf("Bridge startup failed. See %s", logPath)
}

// isNotListening reports a missing socket file or a socket that nobody accepts on.
func isNotListening(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// withVariable returns a copy of environment with one variable set to value.
func withVariable(environment []string, name, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, name+"=") {
			result = append(result, entry)
		}
	}
	return append(result, name+"="+value)
}
