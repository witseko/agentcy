package wire

import (
	"bufio"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCoreVariable makes the test binary act as a core: listen on the socket in
// AGENTCY_HOME and answer each request with the environment it was started with.
const fakeCoreVariable = "AGENTCY_TEST_FAKE_CORE"

// TestMain runs the fake core when the test binary is started as one.
func TestMain(m *testing.M) {
	if os.Getenv(fakeCoreVariable) == "1" {
		runFakeCore()
		return
	}
	os.Exit(m.Run())
}

// runFakeCore serves one connection, then exits.
func runFakeCore() {
	listener, err := net.Listen("unix", filepath.Join(os.Getenv("AGENTCY_HOME"), "core.sock"))
	if err != nil {
		os.Exit(3)
	}
	connection, err := listener.Accept()
	if err != nil {
		os.Exit(3)
	}
	_, _ = bufio.NewReader(connection).ReadString('\n')
	environment := TextList(os.Environ())
	_, _ = connection.Write([]byte(`{"result": ` + environment.Compact() + "}\n"))
	connection.Close()
	time.Sleep(200 * time.Millisecond)
}

// shortHome returns a private directory short enough for a Unix socket path.
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "agw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return home
}

func TestDaemonStartCommandDefaultsToThePythonCoreBesideTheBinary(t *testing.T) {
	clone := t.TempDir()
	binary := filepath.Join(clone, "bin", "agentcy")
	python := filepath.Join(clone, ".venv", "bin", "python")
	for _, path := range []string{binary, python} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	command, err := CoreCommand("", false, binary)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(clone, "bin", "..", ".venv", "bin", "python"), "-I", "-m", "agentcy.mcp.core"}
	if strings.Join(command, " ") != strings.Join(want, " ") {
		t.Errorf("command = %v, want %v", command, want)
	}
	installed, err := CoreCommand("", false, python)
	if err != nil || installed[0] != python {
		t.Errorf("binary inside .venv/bin: command = %v, err = %v", installed, err)
	}
	if _, err := CoreCommand("", false, filepath.Join(t.TempDir(), "agentcy")); !errors.Is(err, ErrNoPythonCore) {
		t.Errorf("no Python core: err = %v", err)
	}
}

func TestCoreBinSelectsAnotherCore(t *testing.T) {
	coreBin := filepath.Join(t.TempDir(), "agentcy")
	if err := os.WriteFile(coreBin, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	command, err := CoreCommand(coreBin, true, "")
	if err != nil || strings.Join(command, " ") != coreBin+" core" {
		t.Errorf("command = %v, err = %v", command, err)
	}
}

func TestCoreBinRefusesAnythingButAnAbsoluteFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "agentcy"), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, configured := range []string{"agentcy", "./agentcy", "", filepath.Join(root, "missing"), filepath.Join(root, "directory")} {
		_, err := CoreCommand(configured, true, "")
		if !errors.Is(err, ErrInvalidCoreBin) {
			t.Errorf("AGENTCY_CORE_BIN=%q: err = %v", configured, err)
		}
		if strings.Contains(err.Error(), configured) && configured != "" {
			t.Errorf("the error repeats the configured value %q", configured)
		}
	}
}

// TestSpawnUsesExplicitEnvironment starts the fake core through the Connector and checks
// that it received exactly the given environment plus AGENTCY_HOME, not the test's.
func TestSpawnUsesExplicitEnvironment(t *testing.T) {
	home := shortHome(t)
	t.Setenv("AGENTCY_TEST_INHERITED", "must-not-leak")
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	connector := &Connector{
		DataDir:     home,
		Environment: []string{fakeCoreVariable + "=1", "AGENTCY_HOME=/wrong", "MARKER=kept"},
		Command:     func() ([]string, error) { return []string{testBinary, "-test.run=^$"}, nil },
	}
	client := &Client{Connect: connector.Connect}
	result, err := client.Exchange(validRead())
	if err != nil {
		t.Fatalf("Exchange through a started core: %v", err)
	}
	environment := result.Compact()
	for _, want := range []string{`"MARKER=kept"`, `"AGENTCY_HOME=` + home + `"`} {
		if !strings.Contains(environment, want) {
			t.Errorf("core environment %s lacks %s", environment, want)
		}
	}
	for _, unwanted := range []string{"AGENTCY_TEST_INHERITED", "/wrong"} {
		if strings.Contains(environment, unwanted) {
			t.Errorf("core environment %s holds %s", environment, unwanted)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "start.lock")); err != nil {
		t.Errorf("start lock: %v", err)
	}
}

func TestStartFailureNamesTheLog(t *testing.T) {
	home := shortHome(t)
	connector := &Connector{
		DataDir:     home,
		Environment: []string{},
		Command:     func() ([]string, error) { return []string{"/usr/bin/false"}, nil },
	}
	_, err := connector.Connect()
	if err == nil || !strings.Contains(err.Error(), filepath.Join(home, "core.log")) {
		t.Errorf("err = %v", err)
	}
}
