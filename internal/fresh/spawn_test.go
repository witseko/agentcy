package fresh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// One test per security rule of plugin spawns (see spawn.go).

func TestSpawnUsesArgumentListNeverShell(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "shell-ran")
	client, plugin := fakeClient(t, "", nil)
	plugin.Command = append(plugin.Command, "; touch "+marker, "$(touch "+marker+")", "`touch "+marker+"`")
	if _, err := client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("3", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Error("a shell interpreted the plugin arguments")
	}
}

func TestSpawnRequiresAbsoluteCommand(t *testing.T) {
	client, plugin := fakeClient(t, "", nil)
	plugin.Command = []string{filepath.Base(fakePluginPath)}
	if _, err := client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("4", 32)); err == nil {
		t.Error("a relative plugin command ran")
	}
	if _, err := ParseSettings([]byte(`{"plugins": {"p": {"command": ["fakeplugin"], "model": "m"}}}`)); err == nil {
		t.Error("plugins.json accepted a relative command")
	}
}

func TestSpawnSetsEnvironmentExplicitly(t *testing.T) {
	output := filepath.Join(t.TempDir(), "environment")
	client, plugin := fakeClient(t, "", fakeEnvironment{
		"FAKE_ENV_OUT":    output,
		"HOME":            "/home/test",
		"SHELL":           "() { exported function; }",
		"ALLOWED_TOKEN":   "allowed-value",
		"PROVIDER_SECRET": "not-allowlisted",
		"AGENTCY_HOME":    "/inherited/home",
	})
	plugin.Environment = append(plugin.Environment, "ALLOWED_TOKEN", "bad-name", "AGENTCY_HOME", "UNSET_NAME")
	// A variable of the test process itself must not reach the plugin either.
	t.Setenv("AGENTCY_PROCESS_ONLY", "leak")
	if _, err := client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("5", 32)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(string(data), "\n")
	sort.Strings(got)
	want := []string{
		"AGENTCY_BIN=/opt/agentcy/bin/agentcy",
		"AGENTCY_HOME=" + client.DataDir,
		"ALLOWED_TOKEN=allowed-value",
		"FAKE_ENV_OUT=" + output,
		"FAKE_MODE=",
		"HOME=/home/test",
		"PATH=/usr/bin:/bin",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("plugin environment:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSpawnEnvironmentIsNeverNil(t *testing.T) {
	environment := pluginEnvironment(Plugin{}, spawnSettings{dataDir: "/data", lookupEnv: fakeEnvironment{}.lookup})
	if environment == nil || len(environment) != 1 || environment[0] != "AGENTCY_HOME=/data" {
		t.Errorf("environment %q", environment)
	}
}

func TestSpawnDiscardsStderr(t *testing.T) {
	_, plugin := fakeClient(t, "", nil)
	process, err := startPlugin(plugin, []string{"FAKE_MODE=slow"})
	if err != nil {
		t.Fatal(err)
	}
	defer process.stop(time.Now())
	// A nil Stderr connects the null device; anything else could leak plugin output.
	if process.command.Stderr != nil {
		t.Error("plugin standard error is connected")
	}
}

func TestSpawnKillsAndReapsOnTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	client, plugin := fakeClient(t, "grandchild", fakeEnvironment{"FAKE_PID_OUT": pidFile})
	client.Timeout = 500 * time.Millisecond
	if _, err := client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("6", 32)); err == nil {
		t.Fatal("a hanging plugin succeeded")
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	for _, field := range fields {
		pid, _ := strconv.Atoi(field)
		if !processGone(pid) {
			t.Errorf("process %d is still alive", pid)
		}
	}
}

// processGone waits up to two seconds for a process to disappear. An orphan is reaped
// by launchd or init shortly after it dies.
func processGone(pid int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestSpawnBoundsOutputSize(t *testing.T) {
	client, plugin := fakeClient(t, "oversize", nil)
	started := time.Now()
	if _, err := client.Operate(context.Background(), plugin, ToolInspect, strings.Repeat("7", 32)); err == nil {
		t.Fatal("an oversized reply was accepted")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("oversized reply took %s", elapsed)
	}
}
