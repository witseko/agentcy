//go:build conformance

package conformance

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	coreCommandVariable = "AGENTCY_CORE_CMD"
	startupTimeout      = 30 * time.Second
	stopTimeout         = 5 * time.Second
)

// daemon is one core process that serves a private data directory for one scenario.
type daemon struct {
	command *exec.Cmd
	home    string
	socket  string
	exited  chan struct{}
	output  lockedBuffer
}

// lockedBuffer collects the daemon's output for failure messages.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.String()
}

// startDaemon starts the core from AGENTCY_CORE_CMD with a fresh AGENTCY_HOME and waits
// until its socket accepts connections. The test stops the daemon and removes the
// directory when it ends.
func startDaemon(t *testing.T) *daemon {
	t.Helper()
	arguments := strings.Fields(os.Getenv(coreCommandVariable))
	if len(arguments) == 0 {
		t.Skipf("set %s to the daemon command, for example %q", coreCommandVariable,
			"uv run python -I -m agentcy.mcp.core")
	}
	home := shortTempDir(t, "agentcy-home-")
	command := exec.Command(arguments[0], arguments[1:]...)
	command.Dir = repositoryRoot(t)
	command.Env = daemonEnvironment(home)
	// A process group lets stop() reach the daemon behind a wrapper such as `uv run`.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	server := &daemon{
		command: command,
		home:    home,
		socket:  filepath.Join(home, "core.sock"),
		exited:  make(chan struct{}),
	}
	command.Stdout = &server.output
	command.Stderr = &server.output
	if err := command.Start(); err != nil {
		t.Fatalf("start %s: %v", coreCommandVariable, err)
	}
	go func() {
		_ = command.Wait()
		close(server.exited)
	}()
	t.Cleanup(server.stop)
	server.waitForSocket(t)
	return server
}

// shortTempDir makes a private directory under /tmp. A Unix socket path must stay under
// about 104 bytes on macOS, and the default test temp directory can be longer.
func shortTempDir(t *testing.T, prefix string) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		t.Fatalf("make temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatalf("chmod temp directory: %v", err)
	}
	return directory
}

// repositoryRoot returns the repository root, so a relative daemon command works there.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("find repository root: %v", err)
	}
	return root
}

// daemonEnvironment passes the caller's environment with AGENTCY_HOME replaced, so the
// daemon never touches the real ~/.agentcy. Git location variables are removed because
// they would change the workspace key.
func daemonEnvironment(home string) []string {
	removed := []string{"AGENTCY_HOME=", "GIT_DIR=", "GIT_WORK_TREE=", "GIT_COMMON_DIR="}
	environment := []string{"AGENTCY_HOME=" + home}
	for _, entry := range os.Environ() {
		if !hasAnyPrefix(entry, removed) {
			environment = append(environment, entry)
		}
	}
	return environment
}

func hasAnyPrefix(text string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// waitForSocket polls the socket until a connection succeeds or the daemon exits.
func (d *daemon) waitForSocket(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-d.exited:
			t.Fatalf("daemon exited before serving its socket. Output:\n%s", d.output.String())
		default:
		}
		connection, err := net.DialTimeout("unix", d.socket, time.Second)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon socket not ready after %s. Output:\n%s", startupTimeout, d.output.String())
}

// operatorKey reads the key the daemon wrote at start, as an operator client does.
func (d *daemon) operatorKey(t *testing.T) string {
	t.Helper()
	key, err := os.ReadFile(filepath.Join(d.home, "operator.key"))
	if err != nil {
		t.Fatalf("read operator key: %v", err)
	}
	return string(key)
}

// stop sends SIGTERM to the daemon's process group and SIGKILL if it does not exit in time.
func (d *daemon) stop() {
	group := -d.command.Process.Pid
	_ = syscall.Kill(group, syscall.SIGTERM)
	select {
	case <-d.exited:
	case <-time.After(stopTimeout):
		_ = syscall.Kill(group, syscall.SIGKILL)
		<-d.exited
	}
}
