//go:build conformance

package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// freshRunTimeout bounds each wait for the worker. The goldens come from the former Python
// worker, which scanned every second, and the Go worker inspects every 5 s, so the waits
// below only look for states that both reach and then keep.
const freshRunTimeout = 30 * time.Second

// registerFixturePlugin writes plugins.json with tests/fixtures/fake_run_plugin.py run by
// the project's .venv Python, or skips the test without that Python.
func registerFixturePlugin(s *scenario) {
	s.t.Helper()
	root := repositoryRoot(s.t)
	python := filepath.Join(root, ".venv", "bin", "python")
	if _, err := os.Stat(python); err != nil {
		s.t.Skipf("the fixture plugin needs %s: run `uv sync --locked` first", python)
	}
	settings, _ := json.Marshal(map[string]any{"plugins": map[string]any{"fixture": map[string]any{
		"command": []string{python, filepath.Join(root, "tests", "fixtures", "fake_run_plugin.py")},
		"model":   "test/model",
	}}})
	writeSetting(s, "plugins.json", string(settings), 0o600)
}

// waitForLaunchState polls the lead's status, without recording the polls, until each
// named slot shows the launch state.
func (s *scenario) waitForLaunchState(token, state string, names ...string) {
	s.t.Helper()
	line, _ := encodeRequest(fields{"action": "status", "workspace": s.workspace, "token": token})
	deadline := time.Now().Add(freshRunTimeout)
	for time.Now().Before(deadline) {
		reply, err := exchange(s.daemon.socket, line, false)
		if err != nil {
			s.t.Fatalf("status poll: %v", err)
		}
		var decoded struct {
			Result struct {
				Members map[string]struct {
					LaunchState string `json:"launch_state"`
				} `json:"members"`
			} `json:"result"`
		}
		_ = json.Unmarshal([]byte(reply), &decoded)
		reached := true
		for _, name := range names {
			reached = reached && decoded.Result.Members[name].LaunchState == state
		}
		if reached {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("%v did not reach launch state %q. Daemon output:\n%s", names, state, s.daemon.output.String())
}

// From test_fresh_runs.py and test_mcp_tasks_stdio.py: a lead's launch, a repeated launch,
// an MCP-task launch with status and cancel, an operator launch, and a close that cancels
// the remaining runs. Only steps whose answer does not depend on worker timing are
// recorded: each recorded step comes after a wait for a state that both cores reach and
// then keep (running after the start, cancelled after a stop).
func TestFreshRunLifecycle(t *testing.T) {
	s := newScenario(t)
	registerFixturePlugin(s)
	created := s.member("lead creates", "create", fields{"brief": "Fresh runs", "initiator": "Lead", "invitees": []any{"Worker"}})
	lead := created.text("token")
	s.member("worker joins", "join", fields{"code": created.text("invitations", "Worker")})
	s.member("lead lists plugins", "plugins", fields{"token": lead})
	s.member("lead launches Fresh", "launch", fields{"token": lead, "name": "Fresh", "plugin": "fixture", "directory": s.directory})
	s.waitForLaunchState(lead, "running", "Fresh")
	s.member("status shows the running launch", "status", fields{"token": lead})
	s.member("a repeated launch returns the same assignment", "launch", fields{"token": lead, "name": "fresh", "plugin": "fixture", "directory": s.directory})
	s.member("a launch with another plugin is refused", "launch", fields{"token": lead, "name": "Fresh", "plugin": "other", "directory": s.directory})
	s.member("a manual invite cannot take the reserved slot", "invite", fields{"token": lead, "name": "Fresh"})
	s.member("a launch outside the workspace is refused", "launch", fields{"token": lead, "name": "Other", "plugin": "fixture", "directory": "/"})
	s.member("a launch cannot take the lead slot", "launch", fields{"token": lead, "name": "Lead", "plugin": "fixture", "directory": s.directory})

	task := s.member("lead launches Tasked as an MCP task", "launch", fields{"token": lead, "name": "Tasked", "plugin": "fixture", "directory": s.directory, "mcp_task": true})
	taskID := task.text("mcp_task", "task_id")
	s.waitForLaunchState(lead, "running", "Tasked")
	s.member("the MCP task is running", "mcp_task_status", fields{"task_id": taskID})
	s.member("the host cancels the MCP task", "mcp_task_cancel", fields{"task_id": taskID})
	s.waitForLaunchState(lead, "cancelled", "Tasked")
	s.member("the MCP task is cancelled", "mcp_task_status", fields{"task_id": taskID})
	s.member("cancelling a finished MCP task changes nothing", "mcp_task_cancel", fields{"task_id": taskID})

	conversation := created.text("conversation")
	s.operator("operator launches Third", "operator_launch", fields{"conversation": conversation, "name": "Third", "plugin": "fixture"})
	s.waitForLaunchState(lead, "running", "Third")
	s.operator("operator repeats the launch", "operator_launch", fields{"conversation": conversation, "name": "Third", "plugin": "fixture"})
	s.operator("operator cannot relaunch an ended run", "operator_launch", fields{"conversation": conversation, "name": "Tasked", "plugin": "fixture"})
	s.operator("operator launch of an unconfigured plugin", "operator_launch", fields{"conversation": conversation, "name": "Fourth", "plugin": "missing"})

	s.member("lead closes", "close", fields{"token": lead})
	s.waitForLaunchState(lead, "cancelled", "Fresh", "Third")
	s.member("status after the close", "status", fields{"token": lead})
	s.member("the transcript shows the cancellations", "read", fields{"token": lead})
	s.operator("operator launch after the close", "operator_launch", fields{"conversation": conversation, "name": "Fifth", "plugin": "fixture"})
}

// TestFreshRunRelaunch checks relaunching an ended plugin run while preserving open tasks
// and refusing active-run, live-manual-chat, or unknown-slot relaunches.
func TestFreshRunRelaunch(t *testing.T) {
	s := newScenario(t)
	registerFixturePlugin(s)
	created := s.member("lead creates", "create", fields{"brief": "Relaunch runs", "initiator": "Lead", "invitees": []any{"Manual"}})
	lead := created.text("token")
	conversation := created.text("conversation")
	s.member("manual worker joins", "join", fields{"code": created.text("invitations", "Manual")})

	task := s.member("lead launches Tasked as an MCP task", "launch", fields{"token": lead, "name": "Tasked", "plugin": "fixture", "directory": s.directory, "mcp_task": true})
	taskID := task.text("mcp_task", "task_id")
	s.waitForLaunchState(lead, "running", "Tasked")
	s.post("lead assigns a task to Tasked", lead, fields{"body": "Keep this task across relaunch.", "kind": "task", "to": "Tasked", "due_minutes": 30})
	s.member("relaunch is refused while the run is active", "launch", fields{"token": lead, "name": "Tasked", "plugin": "fixture", "directory": s.directory, "relaunch": true})
	s.operator("operator relaunch is refused while the run is active", "operator_launch", fields{"conversation": conversation, "name": "Tasked", "plugin": "fixture", "relaunch": true})
	s.member("relaunch of an unknown slot is refused", "launch", fields{"token": lead, "name": "Unknown", "plugin": "fixture", "directory": s.directory, "relaunch": true})
	s.operator("operator relaunch of a live manual chat is refused", "operator_launch", fields{"conversation": conversation, "name": "Manual", "plugin": "fixture", "relaunch": true})

	s.member("the host cancels the MCP task", "mcp_task_cancel", fields{"task_id": taskID})
	s.waitForLaunchState(lead, "cancelled", "Tasked")
	s.member("repeated member launch without relaunch returns the ended assignment", "launch", fields{"token": lead, "name": "Tasked", "plugin": "fixture", "directory": s.directory})
	s.member("lead relaunches Tasked", "launch", fields{"token": lead, "name": "Tasked", "plugin": "fixture", "directory": s.directory, "relaunch": true})
	s.waitForLaunchState(lead, "running", "Tasked")
	s.member("status shows the relaunched run and the preserved open task", "status", fields{"token": lead})

	s.member("lead closes", "close", fields{"token": lead})
	s.waitForLaunchState(lead, "cancelled", "Tasked")
	s.member("status after the close", "status", fields{"token": lead})
}

// From test_fresh_runs.py: a running fresh run survives a daemon restart, the new
// daemon's startup reconciliation keeps it running, and a close then cancels it.
func TestFreshRunSurvivesRestart(t *testing.T) {
	s := newScenario(t)
	registerFixturePlugin(s)
	created := s.member("lead creates", "create", fields{"brief": "Restart", "initiator": "Lead", "invitees": []any{"Worker"}})
	lead := created.text("token")
	s.member("lead launches Fresh", "launch", fields{"token": lead, "name": "Fresh", "plugin": "fixture", "directory": s.directory})
	s.waitForLaunchState(lead, "running", "Fresh")
	s.restartDaemon()
	s.waitForLaunchState(lead, "running", "Fresh")
	s.member("the restarted daemon keeps the run", "status", fields{"token": lead})
	s.member("lead closes", "close", fields{"token": lead})
	s.waitForLaunchState(lead, "cancelled", "Fresh")
	s.member("the transcript shows the cancellation", "read", fields{"token": lead})
}

// restartDaemon stops the scenario's daemon and starts the same command on the same data
// directory.
func (s *scenario) restartDaemon() {
	s.t.Helper()
	s.daemon.stop()
	s.daemon = startDaemonInHome(s.t, strings.Fields(os.Getenv(coreCommandVariable)), s.daemon.home)
	s.operatorKey = s.daemon.operatorKey(s.t)
	s.normalizer.addLiteral(s.operatorKey, "<operator-key>")
}
