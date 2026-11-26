package fresh_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/conversation"
	"github.com/witseko/agentcy/internal/fresh"
	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/wire"
)

// Port of tests/mcp/test_fresh_runs.py: the real conversation rules and store, the real
// plugin client, and the fixture plugin tests/fixtures/fake_run_plugin.py run by the
// project's .venv Python. The worker is driven step by step with a fake clock.

// fixturePython returns the .venv Python and the fixture plugin, or skips the test.
func fixturePython(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(root, ".venv", "bin", "python")
	if _, err := os.Stat(python); err != nil {
		t.Skipf("the fixture plugin needs %s: run `uv sync --locked` first", python)
	}
	return python, filepath.Join(root, "tests", "fixtures", "fake_run_plugin.py")
}

// testClock is the fake time of both the requests and the worker.
type testClock struct {
	mutex sync.Mutex
	now   time.Time
}

func (clock *testClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *testClock) Advance(duration time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.now = clock.now.Add(duration)
}

// hookedProvider wraps the real client so a test can change answers, like the Python
// tests' monkeypatching of plugin_client.
type hookedProvider struct {
	inner        fresh.Provider
	capabilities func() error
	operate      func(tool string, call func() (fresh.RunResult, error)) (fresh.RunResult, error)
}

func (provider *hookedProvider) CheckCapabilities(ctx context.Context, plugin fresh.Plugin) error {
	if provider.capabilities != nil {
		return provider.capabilities()
	}
	return provider.inner.CheckCapabilities(ctx, plugin)
}

func (provider *hookedProvider) Start(ctx context.Context, plugin fresh.Plugin, arguments fresh.StartArguments) (fresh.RunResult, error) {
	call := func() (fresh.RunResult, error) { return provider.inner.Start(ctx, plugin, arguments) }
	if provider.operate != nil {
		return provider.operate(fresh.ToolStart, call)
	}
	return call()
}

func (provider *hookedProvider) Operate(ctx context.Context, plugin fresh.Plugin, tool, id string) (fresh.RunResult, error) {
	call := func() (fresh.RunResult, error) { return provider.inner.Operate(ctx, plugin, tool, id) }
	if provider.operate != nil {
		return provider.operate(tool, call)
	}
	return call()
}

// bridge is a private data directory with the fixture plugin, a workspace, a handler and
// a worker.
type bridge struct {
	t           *testing.T
	home        string
	shared      conversation.Shared
	directory   string
	workspace   string
	clock       *testClock
	environment map[string]string
	provider    *hookedProvider
	worker      *fresh.Worker
	handler     *conversation.Handler
	lead        string
	worker1     string
}

// newBridge registers the fixture plugin and starts a ready worker.
func newBridge(t *testing.T) *bridge {
	t.Helper()
	python, fixture := fixturePython(t)
	home, err := os.MkdirTemp("", "agentcy-fresh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	settings, _ := json.Marshal(map[string]any{"plugins": map[string]any{"fixture": map[string]any{
		"command": []string{python, fixture}, "model": "test/model", "timeout_seconds": 120,
		"environment": []string{"TEST_FRESH_RESULT"},
	}}})
	if err := os.WriteFile(filepath.Join(home, fresh.SettingsFilename), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	workspace, err := paths.WorkspaceKey(directory, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	logger := log.New(io.Discard, "", 0)
	b := &bridge{t: t, home: home, shared: conversation.NewShared(home, logger), directory: directory,
		workspace: workspace, clock: &testClock{now: time.Now()}, environment: map[string]string{}}
	t.Cleanup(func() { _ = b.shared.Stores.Close() })
	b.provider = &hookedProvider{inner: &fresh.Client{DataDir: home, LookupEnv: b.lookupEnv}}
	b.worker = b.newWorker()
	b.worker.ReconcileForTest(context.Background(), func(string, string) {})
	b.handler = conversation.NewHandler(b.shared, os.Environ(), b.worker)
	return b
}

// lookupEnv is the daemon environment the plugin client sees.
func (b *bridge) lookupEnv(name string) (string, bool) {
	value, found := b.environment[name]
	return value, found
}

// newWorker builds a worker on the bridge's store, provider and clock, as a restarted
// daemon would. It is not ready until reconciled.
func (b *bridge) newWorker() *fresh.Worker {
	return fresh.NewWorker(fresh.WorkerConfig{
		Store:    conversation.NewAssignmentStore(b.shared),
		Provider: b.provider,
		Plugins:  b.shared.Plugins.Plugin,
		Now:      b.clock.Now,
	})
}

// restart reconciles the stores with a new worker, as the next daemon does.
func (b *bridge) restart() {
	b.newWorker().ReconcileForTest(context.Background(), func(string, string) {})
}

// member sends a member request and returns its result, failing on an error.
func (b *bridge) member(request wire.Request) wire.Value {
	b.t.Helper()
	outcome, err := b.handler.Member(context.Background(), request, b.clock.Now())
	if err != nil {
		b.t.Fatalf("%s: %v", request.Action(), err)
	}
	return outcome.Result
}

// memberError sends a member request that must fail and returns its message.
func (b *bridge) memberError(request wire.Request) string {
	b.t.Helper()
	_, err := b.handler.Member(context.Background(), request, b.clock.Now())
	if err == nil {
		b.t.Fatalf("%s succeeded", request.Action())
	}
	return err.Error()
}

// team creates a conversation with a joined worker.
func (b *bridge) team(sealed bool) {
	created := b.member(&wire.Create{Workspace: b.workspace, Brief: "Work", Initiator: "Lead", Invitees: []wire.Slot{{Name: "Worker"}}, Sealed: sealed})
	b.lead = text(created, "token")
	invitations, _ := created.Get("invitations")
	code, _ := invitations.Get("Worker")
	b.worker1 = text(b.member(&wire.Join{Workspace: b.workspace, Code: code.Text}), "token")
}

// launch asks for the "Fresh" worker with the fixture plugin.
func (b *bridge) launch(token string, mcpTask bool) wire.Value {
	b.t.Helper()
	return b.member(&wire.Launch{Workspace: b.workspace, Token: token, Name: "Fresh", Plugin: "fixture", Directory: b.directory, McpTask: mcpTask})
}

// step runs one worker iteration: the queued dispatches and the due checks.
func (b *bridge) step() {
	b.worker.StepForTest(context.Background())
}

// state reads the state of the only assignment.
func (b *bridge) state() string {
	b.t.Helper()
	var state string
	b.query("SELECT state FROM assignment", &state)
	return state
}

// query reads one row from the workspace store.
func (b *bridge) query(query string, targets ...any) {
	b.t.Helper()
	workspaceStore, err := b.shared.Stores.Open(context.Background(), b.workspace)
	if err != nil {
		b.t.Fatal(err)
	}
	err = workspaceStore.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(query).Scan(targets...)
	})
	if err != nil {
		b.t.Fatal(err)
	}
}

// bodies returns the bodies of the messages the lead can read.
func (b *bridge) bodies() string {
	page := b.member(&wire.Read{Workspace: b.workspace, Token: b.lead})
	messages, _ := page.Get("messages")
	var bodies []string
	for _, message := range messages.Items {
		body, _ := message.Get("body")
		bodies = append(bodies, body.Text)
	}
	return strings.Join(bodies, "\n")
}

// launchState reads Fresh's launch_state from the lead's status.
func (b *bridge) launchState() string {
	status := b.member(&wire.Status{Workspace: b.workspace, Token: b.lead})
	members, _ := status.Get("members")
	fresh, _ := members.Get("Fresh")
	state, _ := fresh.Get("launch_state")
	return state.Text
}

func text(value wire.Value, key string) string {
	member, _ := value.Get(key)
	return member.Text
}

func TestFreshWorkerSurvivesRestartAndStopsOnClose(t *testing.T) {
	b := newBridge(t)
	b.team(false)
	first := b.launch(b.lead, false)
	if again := b.launch(b.lead, false); again.Compact() != first.Compact() {
		t.Errorf("a repeated launch gave %s, want %s", again.Compact(), first.Compact())
	}
	if b.state() != fresh.StateQueued || b.launchState() != fresh.StateQueued {
		t.Fatalf("state %q", b.state())
	}
	b.step()
	if b.state() != fresh.StateRunning || b.launchState() != fresh.StateRunning {
		t.Fatalf("state after dispatch %q", b.state())
	}
	b.restart()
	if b.state() != fresh.StateRunning {
		t.Fatalf("state after restart %q", b.state())
	}
	b.member(&wire.Close{Workspace: b.workspace, Token: b.lead})
	b.restart()
	if b.state() != fresh.StateCancelled {
		t.Errorf("state after close %q", b.state())
	}
	if !strings.Contains(b.bodies(), "Fresh had the fresh run cancelled.") {
		t.Errorf("messages %q", b.bodies())
	}
}

func TestCloseWakesTheWorkerToCancel(t *testing.T) {
	b := newBridge(t)
	b.team(false)
	b.launch(b.lead, false)
	b.step()
	b.member(&wire.Close{Workspace: b.workspace, Token: b.lead})
	// No time passes: the close itself wakes the worker.
	b.step()
	if b.state() != fresh.StateCancelled {
		t.Errorf("state %q", b.state())
	}
}

func TestTransientProviderFailureReconcilesSameIDAfterRestart(t *testing.T) {
	for _, stop := range []string{"close", "deadline"} {
		t.Run(stop, func(t *testing.T) {
			b := newBridge(t)
			b.team(false)
			b.launch(b.lead, false)
			b.step()
			var attempts []string
			b.provider.operate = func(tool string, call func() (fresh.RunResult, error)) (fresh.RunResult, error) {
				seen := strings.Contains(strings.Join(attempts, ","), tool)
				attempts = append(attempts, tool)
				if !seen {
					return fresh.RunResult{}, errors.New("provider is temporarily unavailable")
				}
				return call()
			}
			b.clock.Advance(fresh.DefaultInspectInterval)
			b.step()
			if b.state() != fresh.StateRunning {
				t.Fatalf("state %q", b.state())
			}
			var outcome string
			if stop == "close" {
				b.member(&wire.Close{Workspace: b.workspace, Token: b.lead})
				outcome = fresh.StateCancelled
			} else {
				b.clock.Advance(121 * time.Second)
				outcome = fresh.StateTimedOut
			}
			b.step()
			if b.state() != fresh.StateRunning {
				t.Fatalf("state after the failed cancel %q", b.state())
			}
			b.restart()
			if b.state() != outcome {
				t.Errorf("state %q, want %q", b.state(), outcome)
			}
			if strings.Join(attempts, ",") != "inspect,cancel,cancel" {
				t.Errorf("attempts %v", attempts)
			}
			bodies := b.bodies()
			if !strings.Contains(bodies, "could not confirm the fresh-run state") || !strings.Contains(bodies, "could not confirm cancellation") ||
				strings.Contains(bodies, "uncertain fresh-run state") {
				t.Errorf("messages %q", bodies)
			}
		})
	}
}

func TestLostStartResponseIsReconciledWithoutSecondStart(t *testing.T) {
	b := newBridge(t)
	b.team(false)
	b.launch(b.lead, false)
	var attempts []string
	b.provider.operate = func(tool string, call func() (fresh.RunResult, error)) (fresh.RunResult, error) {
		attempts = append(attempts, tool)
		result, err := call()
		if len(attempts) <= 2 {
			return fresh.RunResult{}, errors.New("provider response was lost")
		}
		return result, err
	}
	b.step()
	if b.state() != fresh.StateStarting {
		t.Fatalf("state %q", b.state())
	}
	b.restart()
	if b.state() != fresh.StateRunning || strings.Join(attempts, ",") != "start,inspect,inspect" {
		t.Errorf("state %q attempts %v", b.state(), attempts)
	}
}

func TestDispatchFailureIsVisibleAndReissueIsSafe(t *testing.T) {
	cases := map[string][2]string{
		"failed":  {fresh.StateFailed, "could not complete the fresh run"},
		"unknown": {fresh.StateStarting, "could not confirm the fresh-run state"},
	}
	for outcome, expected := range cases {
		t.Run(outcome, func(t *testing.T) {
			b := newBridge(t)
			b.environment["TEST_FRESH_RESULT"] = outcome
			b.team(false)
			first := b.launch(b.lead, false)
			b.step()
			if b.state() != expected[0] {
				t.Fatalf("state %q", b.state())
			}
			retry := b.launch(b.lead, false)
			if text(retry, "assignment_id") != text(first, "assignment_id") || text(retry, "state") != expected[0] {
				t.Errorf("retry %s", retry.Compact())
			}
			if !strings.Contains(b.bodies(), expected[1]) {
				t.Errorf("messages %q", b.bodies())
			}
		})
	}
}

func TestExpiredRunningAssignmentCancelsProvider(t *testing.T) {
	b := newBridge(t)
	b.team(false)
	b.launch(b.lead, false)
	b.step()
	b.clock.Advance(121 * time.Second)
	b.restart()
	if b.state() != fresh.StateTimedOut {
		t.Errorf("state %q", b.state())
	}
}

func TestLaunchIsLeadOnly(t *testing.T) {
	b := newBridge(t)
	b.team(false)
	if message := b.memberError(&wire.Launch{Workspace: b.workspace, Token: b.worker1, Name: "Fresh", Plugin: "fixture", Directory: b.directory}); !strings.Contains(message, "Only a lead or co-lead") {
		t.Errorf("error %q", message)
	}
}

func TestLaunchWaitsForTheReconciliation(t *testing.T) {
	b := newBridge(t)
	b.team(false)
	b.handler = conversation.NewHandler(b.shared, os.Environ(), b.newWorker())
	message := b.memberError(&wire.Launch{Workspace: b.workspace, Token: b.lead, Name: "Fresh", Plugin: "fixture", Directory: b.directory})
	if message != "Fresh-run worker is reconciling earlier runs. Retry shortly" {
		t.Errorf("error %q", message)
	}
}

func TestCloseDuringCapabilityCheckPreventsDispatch(t *testing.T) {
	b := newBridge(t)
	b.team(false)
	b.provider.capabilities = func() error {
		b.member(&wire.Close{Workspace: b.workspace, Token: b.lead})
		return nil
	}
	b.provider.operate = func(string, func() (fresh.RunResult, error)) (fresh.RunResult, error) {
		t.Error("a closed room started a provider run")
		return fresh.RunResult{}, errors.New("unexpected")
	}
	b.launch(b.lead, false)
	b.step()
	if b.state() != fresh.StateCancelled {
		t.Errorf("state %q", b.state())
	}
}

func TestActiveLaunchProtectsItsJoinCode(t *testing.T) {
	b := newBridge(t)
	b.team(false)
	b.launch(b.lead, false)
	message := b.memberError(&wire.Invite{Workspace: b.workspace, Token: b.lead, Name: "fresh"})
	if !strings.Contains(message, "reserved for an active fresh run") {
		t.Errorf("error %q", message)
	}
}

func TestRoundReleaseCancelsRoundWorker(t *testing.T) {
	b := newBridge(t)
	b.team(true)
	b.launch(b.lead, false)
	b.step()
	b.member(&wire.Release{Workspace: b.workspace, Token: b.lead})
	b.step()
	if b.state() != fresh.StateCancelled {
		t.Errorf("state %q", b.state())
	}
}

func TestMcpTaskCancelStopsTheRun(t *testing.T) {
	b := newBridge(t)
	b.team(false)
	launched := b.launch(b.lead, true)
	task, _ := launched.Get("mcp_task")
	taskID := text(task, "task_id")
	if taskID == "" || text(task, "launch_state") != fresh.StateQueued || text(task, "participant") != "Fresh" {
		t.Fatalf("launch %s", launched.Compact())
	}
	b.step()
	status := b.member(&wire.McpTaskStatus{Workspace: b.workspace, TaskID: taskID})
	if text(status, "launch_state") != fresh.StateRunning {
		t.Errorf("status %s", status.Compact())
	}
	b.member(&wire.McpTaskCancel{Workspace: b.workspace, TaskID: taskID})
	b.step()
	status = b.member(&wire.McpTaskStatus{Workspace: b.workspace, TaskID: taskID})
	if text(status, "launch_state") != fresh.StateCancelled {
		t.Errorf("status after cancel %s", status.Compact())
	}
	var stored sql.NullString
	b.query("SELECT mcp_task_hash FROM assignment", &stored)
	if stored.String == taskID || !stored.Valid {
		t.Error("the store holds the MCP task ID instead of its digest")
	}
}

func TestOperatorLaunchUsesTheSameDispatch(t *testing.T) {
	b := newBridge(t)
	b.team(false)
	var conversationID string
	b.query("SELECT id FROM conversation", &conversationID)
	key := wire.OperatorHeader{Key: "unused"}
	launch := &wire.OperatorLaunch{
		ConversationHeader: wire.ConversationHeader{CheckoutHeader: wire.CheckoutHeader{OperatorHeader: key, Directory: b.directory}, Conversation: conversationID},
		Name:               "Fresh", Plugin: "fixture",
	}
	outcome, err := b.handler.Operator(context.Background(), launch, b.clock.Now())
	if err != nil || text(outcome.Result, "state") != fresh.StateQueued || len(outcome.Notify) != 1 {
		t.Fatalf("operator launch %s, %v", outcome.Result.Compact(), err)
	}
	b.step()
	if b.launchState() != fresh.StateRunning {
		t.Errorf("launch state %q", b.launchState())
	}
	b.member(&wire.Retire{Workspace: b.workspace, Token: b.lead, Name: "Fresh"})
	b.step()
	if b.state() != fresh.StateCancelled {
		t.Errorf("state after retire %q", b.state())
	}
	if _, err := b.handler.Operator(context.Background(), launch, b.clock.Now()); err == nil || !strings.Contains(err.Error(), "has ended") {
		t.Errorf("relaunch error %v", err)
	}
}
