package fresh

import (
	"bytes"
	"context"
	"errors"
	"log"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testWorkspace = "workspace-one"
	testRunID     = "0123456789abcdef0123456789abcdef"
	testJoinCode  = "secret-join-code.0123456789ab"
)

// workerFixture is a worker on fake parts, driven step by step.
type workerFixture struct {
	worker   *Worker
	store    *fakeStore
	provider *fakeProvider
	clock    *fakeClock
	logs     *bytes.Buffer
	notified []string
}

// newWorkerFixture builds a worker whose clock starts at Unix second 1000.
func newWorkerFixture(t *testing.T, store *fakeStore, provider *fakeProvider) *workerFixture {
	t.Helper()
	fixture := &workerFixture{store: store, provider: provider, clock: &fakeClock{now: mustTime(1000)}, logs: &bytes.Buffer{}}
	fixture.worker = NewWorker(WorkerConfig{
		Store:    store,
		Provider: provider,
		Plugins: func(name string) (*Plugin, error) {
			if name != "fake" {
				return nil, ErrPluginNotConfigured
			}
			return &Plugin{Name: "fake", Command: []string{"/bin/false"}}, nil
		},
		Now:    fixture.clock.Now,
		Logger: log.New(fixture.logs, "", 0),
	})
	fixture.worker.state.notify = func(workspace, room string) {
		fixture.notified = append(fixture.notified, workspace+"/"+room)
	}
	return fixture
}

// openRun is a pending run of an open conversation with a deadline 300 s after the
// fixture's start.
func openRun(state string) Run {
	return Run{Workspace: testWorkspace, ID: testRunID, Room: "room", Slot: 2, Label: "Fresh", Plugin: "fake",
		InviteHash: "hash", State: state, Deadline: 1300, Open: true}
}

// job is the dispatch of the open run.
func job() Dispatch {
	return Dispatch{Workspace: testWorkspace, Plugin: Plugin{Name: "fake"}, Start: StartArguments{
		AssignmentID: testRunID, JoinCode: testJoinCode, TimeoutSeconds: 300,
	}}
}

// step runs one worker iteration.
func (fixture *workerFixture) step() {
	fixture.worker.step(context.Background())
}

func TestWorkerDispatchStartsTheRun(t *testing.T) {
	fixture := newWorkerFixture(t, newFakeStore(openRun(StateQueued)), &fakeProvider{answer: states(map[string]string{ToolStart: StateRunning})})
	fixture.clock.Advance(500 * time.Millisecond)
	fixture.worker.Dispatch(job())
	fixture.step()
	if state := fixture.store.state(testRunID); state != StateRunning {
		t.Errorf("state %q", state)
	}
	// ceil(1300 - 1000.5) = 300.
	if calls := fixture.provider.calls; len(calls) != 1 || calls[0].timeout != 300 {
		t.Errorf("calls %+v", calls)
	}
	if len(fixture.notified) != 0 || len(fixture.store.bodies()) != 0 {
		t.Errorf("a start added events %v or wakes %v", fixture.store.bodies(), fixture.notified)
	}
}

func TestWorkerFinishesNaturally(t *testing.T) {
	provider := &fakeProvider{answer: states(map[string]string{ToolStart: StateRunning, ToolInspect: StateRunning})}
	fixture := newWorkerFixture(t, newFakeStore(openRun(StateQueued)), provider)
	fixture.worker.Dispatch(job())
	fixture.step()
	due, found := fixture.worker.nextDue()
	if !found || !due.Equal(mustTime(1005)) {
		t.Fatalf("next check %v, %v; want the inspect interval", due, found)
	}
	fixture.step()
	if tools := provider.tools(); len(tools) != 1 {
		t.Fatalf("inspected before the interval: %v", tools)
	}
	fixture.clock.Advance(DefaultInspectInterval)
	fixture.step()
	provider.answer = func(string) (RunResult, error) {
		exit := int64(0)
		input, output := int64(10), int64(20)
		return RunResult{State: StateFinished, ExitCode: &exit, InputTokens: &input, OutputTokens: &output}, nil
	}
	fixture.clock.Advance(DefaultInspectInterval)
	fixture.step()
	if state := fixture.store.state(testRunID); state != StateFinished {
		t.Errorf("state %q", state)
	}
	if !reflect.DeepEqual(provider.tools(), []string{ToolStart, ToolInspect, ToolInspect}) {
		t.Errorf("tools %v", provider.tools())
	}
	if len(fixture.store.usage) != 1 || len(fixture.store.bodies()) != 0 {
		t.Errorf("usage %v events %v", fixture.store.usage, fixture.store.bodies())
	}
	if _, found := fixture.worker.nextDue(); found {
		t.Error("a finished run is still scheduled")
	}
}

func TestWorkerFailedStartIsVisible(t *testing.T) {
	provider := &fakeProvider{answer: func(string) (RunResult, error) {
		exit := int64(1)
		return RunResult{State: StateFailed, ExitCode: &exit}, nil
	}}
	fixture := newWorkerFixture(t, newFakeStore(openRun(StateQueued)), provider)
	fixture.worker.Dispatch(job())
	fixture.step()
	if state := fixture.store.state(testRunID); state != StateFailed {
		t.Errorf("state %q", state)
	}
	if bodies := fixture.store.bodies(); len(bodies) != 1 || bodies[0] != "Fresh could not complete the fresh run." {
		t.Errorf("events %v", bodies)
	}
	if !reflect.DeepEqual(fixture.notified, []string{testWorkspace + "/room"}) {
		t.Errorf("wakes %v", fixture.notified)
	}
}

func TestWorkerRejectedCapabilitiesFailTheRun(t *testing.T) {
	provider := &fakeProvider{capabilities: func() error { return errCapUnsupported }, answer: states(nil)}
	fixture := newWorkerFixture(t, newFakeStore(openRun(StateQueued)), provider)
	fixture.worker.Dispatch(job())
	fixture.step()
	if state := fixture.store.state(testRunID); state != StateFailed || len(provider.tools()) != 0 {
		t.Errorf("state %q tools %v", state, provider.tools())
	}
}

func TestWorkerLostStartIsInspectedWithoutSecondStart(t *testing.T) {
	provider := &fakeProvider{answer: states(map[string]string{ToolInspect: StateRunning})}
	fixture := newWorkerFixture(t, newFakeStore(openRun(StateQueued)), provider)
	fixture.worker.Dispatch(job())
	fixture.step()
	if state := fixture.store.state(testRunID); state != StateRunning {
		t.Errorf("state %q", state)
	}
	if !reflect.DeepEqual(provider.tools(), []string{ToolStart, ToolInspect}) {
		t.Errorf("tools %v", provider.tools())
	}
}

func TestWorkerRetryBacksOff(t *testing.T) {
	provider := &fakeProvider{answer: states(nil)}
	fixture := newWorkerFixture(t, newFakeStore(openRun(StateRunning)), provider)
	fixture.worker.reconcile(context.Background())
	attempts := []int64{}
	record := func() {
		attempts = append(attempts, fixture.clock.Now().Unix())
	}
	record()
	for second := 0; second < 20; second++ {
		fixture.clock.Advance(time.Second)
		before := len(provider.tools())
		fixture.step()
		if len(provider.tools()) > before {
			record()
		}
	}
	// Delays of 1, 2, 4, 5, 5 ... seconds after each failure (RETRY_MAX_SECONDS).
	want := []int64{1000, 1001, 1003, 1007, 1012, 1017}
	if !reflect.DeepEqual(attempts, want) {
		t.Errorf("attempts at %v, want %v", attempts, want)
	}
	if bodies := fixture.store.bodies(); len(bodies) != 1 || !strings.Contains(bodies[0], "could not confirm the fresh-run state") {
		t.Errorf("events %v", bodies)
	}
	if len(fixture.notified) != 1 || fixture.store.state(testRunID) != StateRunning {
		t.Errorf("wakes %v state %q", fixture.notified, fixture.store.state(testRunID))
	}
}

func TestWorkerCancelsAtOnceOnStop(t *testing.T) {
	stops := map[string]func(run *Run){
		"close":  func(run *Run) { run.Open = false },
		"retire": func(run *Run) { run.Retired = true },
		"cancel": func(run *Run) { run.CancelRequested = true },
		"round":  func(run *Run) { run.CurrentRound = "critique" },
	}
	for name, stop := range stops {
		t.Run(name, func(t *testing.T) {
			run := openRun(StateRunning)
			run.Round, run.CurrentRound = "sealed", "sealed"
			provider := &fakeProvider{answer: states(map[string]string{ToolInspect: StateRunning, ToolCancel: StateCancelled})}
			fixture := newWorkerFixture(t, newFakeStore(run), provider)
			fixture.worker.reconcile(context.Background())
			fixture.store.change(testRunID, stop)
			fixture.worker.Wake(testWorkspace)
			fixture.step()
			if state := fixture.store.state(testRunID); state != StateCancelled {
				t.Errorf("state %q", state)
			}
			if !reflect.DeepEqual(provider.tools(), []string{ToolInspect, ToolCancel}) {
				t.Errorf("tools %v", provider.tools())
			}
			if bodies := fixture.store.bodies(); len(bodies) != 1 || bodies[0] != "Fresh had the fresh run cancelled." {
				t.Errorf("events %v", bodies)
			}
		})
	}
}

func TestWorkerIgnoresWakesOfOtherWorkspaces(t *testing.T) {
	provider := &fakeProvider{answer: states(map[string]string{ToolInspect: StateRunning})}
	fixture := newWorkerFixture(t, newFakeStore(openRun(StateRunning)), provider)
	fixture.worker.reconcile(context.Background())
	fixture.store.change(testRunID, func(run *Run) { run.Open = false })
	fixture.worker.Wake("another-workspace")
	fixture.step()
	if len(provider.tools()) != 1 {
		t.Errorf("tools %v", provider.tools())
	}
}

func TestWorkerTimesOutAtTheDeadline(t *testing.T) {
	run := openRun(StateRunning)
	run.Deadline = 1003
	provider := &fakeProvider{answer: states(map[string]string{ToolInspect: StateRunning, ToolCancel: StateCancelled})}
	fixture := newWorkerFixture(t, newFakeStore(run), provider)
	fixture.worker.reconcile(context.Background())
	if due, _ := fixture.worker.nextDue(); !due.Equal(mustTime(1003)) {
		t.Fatalf("next check %v, want the deadline", due)
	}
	fixture.clock.Advance(3 * time.Second)
	fixture.step()
	if state := fixture.store.state(testRunID); state != StateTimedOut {
		t.Errorf("state %q", state)
	}
}

func TestWorkerBoundsFailedCancels(t *testing.T) {
	run := openRun(StateRunning)
	run.Open = false
	provider := &fakeProvider{answer: states(nil)}
	fixture := newWorkerFixture(t, newFakeStore(run), provider)
	for poll := 0; poll < 10; poll++ {
		fixture.worker.serviceWorkspace(context.Background(), testWorkspace, false)
		fixture.clock.Advance(10 * time.Second)
	}
	if state := fixture.store.state(testRunID); state != StateUnknown {
		t.Errorf("state %q", state)
	}
	if calls := len(provider.tools()); calls != cancelMaxFailures {
		t.Errorf("%d cancel calls", calls)
	}
	bodies := strings.Join(fixture.store.bodies(), "\n")
	if strings.Count(bodies, "could not confirm cancellation") != 1 || strings.Count(bodies, "uncertain fresh-run state") != 1 {
		t.Errorf("events %q", bodies)
	}
}

func TestWorkerPollsAnAcknowledgedCancelUntilTheGraceEnds(t *testing.T) {
	run := openRun(StateRunning)
	run.Open = false
	provider := &fakeProvider{answer: states(map[string]string{ToolCancel: StateRunning})}
	fixture := newWorkerFixture(t, newFakeStore(run), provider)
	fixture.worker.serviceWorkspace(context.Background(), testWorkspace, false)
	if due, _ := fixture.worker.nextDue(); !due.Equal(mustTime(1005)) {
		t.Errorf("next check %v, want the cancel poll", due)
	}
	for fixture.clock.Now().Unix() < 1300+30 {
		fixture.clock.Advance(time.Second)
		fixture.step()
	}
	if state := fixture.store.state(testRunID); state != StateUnknown {
		t.Errorf("state %q", state)
	}
	// One cancel at 1000, then one per 5 s until the deadline plus the grace.
	if calls := len(provider.tools()); calls != 67 {
		t.Errorf("%d cancel calls", calls)
	}
}

func TestWorkerReconcilesEachStateAfterRestart(t *testing.T) {
	runs := []Run{openRun(StateQueued), openRun(StateStarting), openRun(StateRunning), openRun(StateRunning)}
	runs[0].ID, runs[1].ID, runs[2].ID, runs[3].ID = "queued", "starting", "closed", "done"
	runs[2].Open = false
	answers := map[string]string{"queued": StateUnknown, "starting": StateRunning, "closed": StateCancelled, "done": StateFinished}
	store := newFakeStore(runs...)
	store.broken["broken-workspace"] = true
	var current string
	provider := &fakeProvider{answer: func(tool string) (RunResult, error) { return RunResult{State: answers[current]}, nil }}
	fixture := newWorkerFixture(t, store, provider)
	fixture.worker.plugins = func(string) (*Plugin, error) { return &Plugin{}, nil }
	inner := fixture.worker.provider
	fixture.worker.provider = providerFunc(func(ctx context.Context, tool, id string) (RunResult, error) {
		current = id
		return inner.Operate(ctx, Plugin{}, tool, id)
	})
	if fixture.worker.Ready() {
		t.Fatal("ready before the reconciliation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		fixture.worker.Run(ctx, func(string, string) {})
	}()
	waitFor(t, fixture.worker.Ready)
	cancel()
	<-done
	want := map[string]string{"queued": StateUnknown, "starting": StateRunning, "closed": StateCancelled, "done": StateFinished}
	for id, state := range want {
		if got := store.state(id); got != state {
			t.Errorf("%s: %q, want %q", id, got, state)
		}
	}
	if !strings.Contains(fixture.logs.String(), "Fresh-run reconciliation failed") {
		t.Error("the broken store was not reported")
	}
}

// providerFunc adapts a function to the inspect and cancel side of Provider.
type providerFunc func(ctx context.Context, tool, id string) (RunResult, error)

func (function providerFunc) CheckCapabilities(context.Context, Plugin) error { return nil }

func (function providerFunc) Start(ctx context.Context, _ Plugin, arguments StartArguments) (RunResult, error) {
	return function(ctx, ToolStart, arguments.AssignmentID)
}

func (function providerFunc) Operate(ctx context.Context, _ Plugin, tool, id string) (RunResult, error) {
	return function(ctx, tool, id)
}

// waitFor polls a condition for up to five seconds.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWorkerStopsWithARunInFlight(t *testing.T) {
	before := runtime.NumGoroutine()
	run := openRun(StateQueued)
	run.Deadline = time.Now().Add(time.Hour).Unix()
	store := newFakeStore()
	started := make(chan struct{})
	provider := providerFunc(func(ctx context.Context, tool, id string) (RunResult, error) {
		if tool == ToolStart {
			close(started)
			<-ctx.Done()
		}
		return RunResult{}, ctx.Err()
	})
	worker := NewWorker(WorkerConfig{Store: store, Provider: provider, Plugins: func(string) (*Plugin, error) { return &Plugin{}, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx, func(string, string) {})
	}()
	waitFor(t, worker.Ready)
	// The launch commits after the startup reconciliation, as a request would.
	store.mutex.Lock()
	store.runs[run.ID] = &run
	store.mutex.Unlock()
	worker.Dispatch(job())
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker did not stop")
	}
	if state := store.state(testRunID); state != StateStarting {
		t.Errorf("state %q; the next daemon reconciles a starting run", state)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines: %d before, %d after", before, after)
	}
}

func TestWorkerLoopIsEventDriven(t *testing.T) {
	var mutex sync.Mutex
	calls := 0
	provider := providerFunc(func(ctx context.Context, tool, id string) (RunResult, error) {
		mutex.Lock()
		defer mutex.Unlock()
		calls++
		return RunResult{State: StateRunning}, nil
	})
	count := func() int {
		mutex.Lock()
		defer mutex.Unlock()
		return calls
	}
	run := openRun(StateRunning)
	run.Deadline = time.Now().Add(time.Hour).Unix()
	store := newFakeStore(run)
	worker := NewWorker(WorkerConfig{Store: store, Provider: provider, Plugins: func(string) (*Plugin, error) { return &Plugin{}, nil },
		Timing: Timing{InspectInterval: 50 * time.Millisecond}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx, func(string, string) {})
	}()
	defer func() {
		cancel()
		<-done
	}()
	waitFor(t, func() bool { return count() >= 3 })
	store.change(testRunID, func(run *Run) { run.State = StateFinished })
	time.Sleep(150 * time.Millisecond)
	settled := count()
	time.Sleep(200 * time.Millisecond)
	if count() != settled {
		t.Errorf("the worker kept checking with no pending run: %d then %d calls", settled, count())
	}
}

func TestWorkerLogsOnlyFixedMessages(t *testing.T) {
	store := newFakeStore(openRun(StateQueued))
	store.failErr = errors.New("insert failed for " + testJoinCode)
	store.broken["secret-workspace"] = true
	provider := &fakeProvider{capabilities: func() error { return errors.New("plugin said " + testJoinCode) }, answer: states(nil)}
	fixture := newWorkerFixture(t, store, provider)
	fixture.worker.reconcile(context.Background())
	fixture.worker.Dispatch(job())
	fixture.step()
	for _, line := range strings.Split(strings.TrimSpace(fixture.logs.String()), "\n") {
		if line != "Fresh-run dispatch failed" && line != "Fresh-run reconciliation failed" {
			t.Errorf("log line %q", line)
		}
	}
	for _, secret := range []string{testJoinCode, testRunID, "secret-workspace"} {
		if strings.Contains(fixture.logs.String(), secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
}
