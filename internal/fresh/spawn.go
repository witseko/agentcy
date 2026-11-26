package fresh

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// This file starts and stops one plugin process. The security rules of plugin spawns
// live here, each with a test named after it in spawn_test.go:
//
//   - the command is an argument list, never a shell;
//   - the executable is an absolute path;
//   - the environment is always set explicitly (a nil exec.Cmd.Env inherits everything);
//   - standard error is discarded;
//   - the process and its process group are killed and reaped on timeout;
//   - the bytes read from standard output are bounded.

// The variables that the daemon adds to every plugin's environment.
const (
	// HomeVariable names the bridge data directory, as in plugin_client.py.
	HomeVariable = "AGENTCY_HOME"
	// BinaryVariable names the absolute path of the running agentcy executable, so a
	// plugin can start `agentcy fresh-worker` instead of the Python module.
	BinaryVariable = "AGENTCY_BIN"
)

// inheritedVariables are the variables that the Python MCP SDK passes to every stdio
// server from the daemon's own environment (DEFAULT_INHERITED_ENV_VARS for POSIX in
// mcp/client/stdio.py). The plugins need PATH and HOME to find their CLIs.
var inheritedVariables = []string{"HOME", "LOGNAME", "PATH", "SHELL", "TERM", "USER"}

// maxReplyBytes bounds everything read from one plugin process's standard output.
const maxReplyBytes = 1 << 20

// exitGrace is how long a plugin may take to exit after its standard input closes before
// its process group is killed (PROCESS_TERMINATION_TIMEOUT in mcp/client/stdio.py).
const exitGrace = 2 * time.Second

// errCommunication is the error of a plugin process that cannot be started, stops early,
// breaks the JSON-RPC framing or runs out of time. It never repeats the plugin's output.
var errCommunication = errors.New("Fresh-run plugin communication failed")

// spawnSettings is what a plugin process gets from the daemon besides its command.
type spawnSettings struct {
	dataDir   string
	binary    string
	lookupEnv func(name string) (string, bool)
}

// pluginEnvironment builds the complete environment of a plugin process in os.Environ
// form: the SDK's inherited variables, the plugin's allowlisted names that are set and
// valid, then AGENTCY_HOME and, when known, AGENTCY_BIN. A later entry of the same name
// replaces an earlier one, as the Python dict merge does.
func pluginEnvironment(plugin Plugin, settings spawnSettings) []string {
	values := map[string]string{}
	order := []string{}
	set := func(name, value string) {
		if _, found := values[name]; !found {
			order = append(order, name)
		}
		values[name] = value
	}
	for _, name := range inheritedVariables {
		// The SDK skips exported shell functions, whose values start with "()".
		if value, found := settings.lookupEnv(name); found && !strings.HasPrefix(value, "()") {
			set(name, value)
		}
	}
	for _, name := range plugin.Environment {
		if !environmentNamePattern.MatchString(name) {
			continue
		}
		if value, found := settings.lookupEnv(name); found {
			set(name, value)
		}
	}
	set(HomeVariable, settings.dataDir)
	if settings.binary != "" {
		set(BinaryVariable, settings.binary)
	}
	environment := make([]string, 0, len(order))
	for _, name := range order {
		environment = append(environment, name+"="+values[name])
	}
	return environment
}

// pluginProcess is one running plugin with its pipes. Lines from its standard output
// arrive on lines from a reader goroutine, which ends when the output ends.
type pluginProcess struct {
	command *exec.Cmd
	stdin   *os.File
	stdout  *os.File
	lines   chan outputLine
	// abandon tells the reader goroutine to stop sending once nobody receives.
	abandon    chan struct{}
	readerDone chan struct{}
}

// outputLine is one line of plugin output, or the error that ended the output.
type outputLine struct {
	text []byte
	err  error
}

// startPlugin starts the plugin's command without a shell, in its own process group,
// with an explicit environment and standard error sent to the null device.
func startPlugin(plugin Plugin, environment []string) (*pluginProcess, error) {
	if len(plugin.Command) == 0 || !filepath.IsAbs(plugin.Command[0]) {
		return nil, errCommunication
	}
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return nil, errCommunication
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		return nil, errCommunication
	}
	command := exec.Command(plugin.Command[0], plugin.Command[1:]...)
	command.Env = environment
	command.Stdin = stdinRead
	command.Stdout = stdoutWrite
	// A nil Stderr connects the null device, so plugin diagnostics are discarded.
	command.Stderr = nil
	// Its own process group lets a timeout kill the plugin's children with it, as the
	// Python SDK does with start_new_session.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		return nil, errCommunication
	}
	// Close the child ends in the parent so EOF propagates as soon as the child exits
	// and Wait never races with reads from stdoutRead.
	_ = stdinRead.Close()
	_ = stdoutWrite.Close()
	process := &pluginProcess{
		command:    command,
		stdin:      stdinWrite,
		stdout:     stdoutRead,
		lines:      make(chan outputLine),
		abandon:    make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	go process.readLines(stdoutRead)
	return process, nil
}

// readLines sends each output line until the output ends, fails or exceeds
// maxReplyBytes, then sends the error that ended it.
func (process *pluginProcess) readLines(stdout io.Reader) {
	defer close(process.readerDone)
	scanner := bufio.NewScanner(io.LimitReader(stdout, maxReplyBytes))
	scanner.Buffer(make([]byte, 0, 4096), maxReplyBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if !process.send(outputLine{text: line}) {
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	process.send(outputLine{err: err})
}

// send hands one line to the receiver, or reports false once the process is abandoned.
func (process *pluginProcess) send(line outputLine) bool {
	select {
	case process.lines <- line:
		return true
	case <-process.abandon:
		return false
	}
}

// write sends one JSON-RPC message line to the plugin.
func (process *pluginProcess) write(message []byte) error {
	if _, err := process.stdin.Write(append(message, '\n')); err != nil {
		return errCommunication
	}
	return nil
}

// stop closes the plugin's standard input and waits until deadline (at most exitGrace)
// for it to exit, then kills its process group. It always reaps the process and ends the
// reader goroutine, so no zombie and no goroutine remains. It reports whether the
// plugin exited before the deadline.
func (process *pluginProcess) stop(deadline time.Time) bool {
	close(process.abandon)
	_ = process.stdin.Close()
	exited := make(chan struct{})
	go func() {
		_ = process.command.Wait()
		close(exited)
	}()
	grace := min(max(time.Until(deadline), 0), exitGrace)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	clean := true
	select {
	case <-exited:
	case <-timer.C:
		clean = false
		process.kill()
		<-exited
	}
	_ = process.stdout.Close()
	<-process.readerDone
	return clean
}

// kill sends SIGKILL to the plugin's process group. The group still exists, because
// the leader is only reaped after this.
func (process *pluginProcess) kill() {
	_ = syscall.Kill(-process.command.Process.Pid, syscall.SIGKILL)
}
