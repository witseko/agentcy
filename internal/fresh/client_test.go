package fresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakePluginPath is the fake plugin compiled once from testdata/fakeplugin by TestMain.
var fakePluginPath string

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "agentcy-fake-plugin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakePluginPath = filepath.Join(directory, "fakeplugin")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", fakePluginPath, "./testdata/fakeplugin")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build fake plugin: %v\n%s", err, output)
		os.Exit(1)
	}
	status := m.Run()
	_ = os.RemoveAll(directory)
	os.Exit(status)
}

// fakeEnvironment is the daemon environment a test client sees.
type fakeEnvironment map[string]string

// lookup reads one variable.
func (environment fakeEnvironment) lookup(name string) (string, bool) {
	value, found := environment[name]
	return value, found
}

// fakeClient returns a client for the fake plugin in mode, with a short timeout. The
// FAKE_ variables reach the plugin through its allowlist.
func fakeClient(t *testing.T, mode string, extra fakeEnvironment) (*Client, Plugin) {
	t.Helper()
	environment := fakeEnvironment{"FAKE_MODE": mode, "PATH": "/usr/bin:/bin"}
	for name, value := range extra {
		environment[name] = value
	}
	client := &Client{DataDir: t.TempDir(), Binary: "/opt/agentcy/bin/agentcy", LookupEnv: environment.lookup, Timeout: 2 * time.Second}
	plugin := Plugin{Name: "fake", Command: []string{fakePluginPath}, Model: "test/model",
		Environment: []string{"FAKE_MODE", "FAKE_LOG", "FAKE_ENV_OUT", "FAKE_PID_OUT"}}
	return client, plugin
}

// loggedMessages reads the JSON-RPC lines the fake plugin received.
func loggedMessages(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var messages []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		messages = append(messages, message)
	}
	return messages
}

func TestClientHandshakeMatchesThePythonClient(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "wire.log")
	client, plugin := fakeClient(t, "", fakeEnvironment{"FAKE_LOG": logPath})
	result, err := client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("a", 32))
	if err != nil || result.State != StateRunning || result.ExitCode != nil {
		t.Fatalf("Operate = %+v, %v", result, err)
	}
	messages := loggedMessages(t, logPath)
	encoded, _ := json.Marshal(messages)
	want := `[{"id":1,"jsonrpc":"2.0","method":"initialize","params":{"capabilities":{},"clientInfo":{"name":"agentcy","version":"0.1.0"},"protocolVersion":"2025-11-25"}},` +
		`{"jsonrpc":"2.0","method":"notifications/initialized"},` +
		`{"id":2,"jsonrpc":"2.0","method":"tools/call","params":{"arguments":{"assignment_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"name":"inspect"}}]`
	if string(encoded) != want {
		t.Errorf("wire:\n%s\nwant:\n%s", encoded, want)
	}
}

func TestClientStartLeavesOutNilCaps(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "wire.log")
	client, plugin := fakeClient(t, "", fakeEnvironment{"FAKE_LOG": logPath})
	arguments := StartArguments{AssignmentID: strings.Repeat("b", 32), Workspace: "/w", BridgeHome: "/h", JoinCode: "code", Model: "m", TimeoutSeconds: 30}
	if _, err := client.Start(context.Background(), plugin, arguments); err != nil {
		t.Fatal(err)
	}
	call := loggedMessages(t, logPath)[2]["params"].(map[string]any)
	sent := call["arguments"].(map[string]any)
	if call["name"] != ToolStart || len(sent) != 6 || sent["timeout_seconds"] != float64(30) {
		t.Errorf("start call %v", call)
	}
}

func TestClientChecksCapabilitiesAndCaps(t *testing.T) {
	client, plugin := fakeClient(t, "", nil)
	if err := client.CheckCapabilities(context.Background(), plugin); err != nil {
		t.Fatalf("plain plugin: %v", err)
	}
	limit := int64(1000)
	plugin.TokenCap = &limit
	if err := client.CheckCapabilities(context.Background(), plugin); !errors.Is(err, errCapUnsupported) {
		t.Errorf("token cap without support: %v", err)
	}
	client, capped := fakeClient(t, "token-cap", nil)
	capped.TokenCap = &limit
	if err := client.CheckCapabilities(context.Background(), capped); err != nil {
		t.Errorf("token cap with support: %v", err)
	}
	capped.CostCapMicroUSD = &limit
	if err := client.CheckCapabilities(context.Background(), capped); !errors.Is(err, errCapUnsupported) {
		t.Errorf("cost cap without support: %v", err)
	}
}

func TestClientSkipsServerNotifications(t *testing.T) {
	client, plugin := fakeClient(t, "notify-first", nil)
	if _, err := client.Operate(context.Background(), plugin, ToolCancel, strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
}

func TestClientRefusesBadReplies(t *testing.T) {
	for _, mode := range []string{"bad-version", "malformed", "tool-error", "rpc-error", "no-structured", "bad-state", "exit"} {
		client, plugin := fakeClient(t, mode, nil)
		started := time.Now()
		_, err := client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("d", 32))
		if !errors.Is(err, errStateNotConfirmed) {
			t.Errorf("%s: %v", mode, err)
		}
		if strings.Contains(fmt.Sprint(err), "secret-reply-text") {
			t.Errorf("%s: the error repeats the reply", mode)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Errorf("%s took %s", mode, elapsed)
		}
	}
}

func TestClientAcceptsAReplyBeforeANonZeroExit(t *testing.T) {
	// The Python client ignores the exit status once it has the reply.
	client, plugin := fakeClient(t, "exit-after-reply", nil)
	if result, err := client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("e", 32)); err != nil || result.State != StateRunning {
		t.Errorf("Operate = %+v, %v", result, err)
	}
}

func TestClientTimesOutAndStopsThePlugin(t *testing.T) {
	client, plugin := fakeClient(t, "slow", nil)
	client.Timeout = 300 * time.Millisecond
	started := time.Now()
	if _, err := client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("f", 32)); err == nil {
		t.Fatal("a plugin that never answers succeeded")
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Errorf("timeout took %s", elapsed)
	}
}

func TestClientStopsALingeringPluginAfterTheGrace(t *testing.T) {
	client, plugin := fakeClient(t, "linger", nil)
	client.Timeout = 4 * time.Second
	started := time.Now()
	if _, err := client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("0", 32)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < exitGrace || elapsed > exitGrace+time.Second {
		t.Errorf("stop took %s, want about %s", elapsed, exitGrace)
	}
}

func TestClientCallEndsWithTheDaemonContext(t *testing.T) {
	client, plugin := fakeClient(t, "slow", nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	started := time.Now()
	if _, err := client.Operate(ctx, plugin, ToolInspect, strings.Repeat("1", 32)); err == nil {
		t.Fatal("cancelled call succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("cancel took %s", elapsed)
	}
}

func TestClientLeavesNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	for _, mode := range []string{"", "slow", "malformed", "exit"} {
		client, plugin := fakeClient(t, mode, nil)
		client.Timeout = 200 * time.Millisecond
		_, _ = client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("2", 32))
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines: %d before, %d after", before, after)
	}
}
