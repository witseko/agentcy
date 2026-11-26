package fresh

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// This file holds the fakes of the worker tests: a clock, an in-memory store with the
// rules of assignments.record, and a scripted provider.

// mustTime returns the time at a Unix second.
func mustTime(unix int64) time.Time {
	return time.Unix(unix, 0)
}

// fakeClock is a settable clock.
type fakeClock struct {
	mutex sync.Mutex
	now   time.Time
}

// Now returns the current fake time.
func (clock *fakeClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

// Advance moves the clock forward.
func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.now = clock.now.Add(duration)
}

// storedEvent is one event the fake store recorded.
type storedEvent struct {
	room string
	body string
}

// fakeStore keeps assignment rows in memory and applies the same transitions and event
// texts as the real store.
type fakeStore struct {
	mutex   sync.Mutex
	runs    map[string]*Run
	events  []storedEvent
	usage   []RunResult
	broken  map[string]bool
	failErr error
}

// newFakeStore returns a store holding runs.
func newFakeStore(runs ...Run) *fakeStore {
	store := &fakeStore{runs: map[string]*Run{}, broken: map[string]bool{}}
	for index := range runs {
		run := runs[index]
		store.runs[run.ID] = &run
	}
	return store
}

// state returns the stored state of a run.
func (store *fakeStore) state(id string) string {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return store.runs[id].State
}

// change edits a stored run, as a request transaction would.
func (store *fakeStore) change(id string, edit func(run *Run)) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	edit(store.runs[id])
}

// bodies returns the recorded event texts.
func (store *fakeStore) bodies() []string {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	bodies := []string{}
	for _, event := range store.events {
		bodies = append(bodies, event.body)
	}
	return bodies
}

// WorkspaceKeys lists the workspaces of the stored runs.
func (store *fakeStore) WorkspaceKeys() ([]string, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	seen := map[string]bool{}
	for _, run := range store.runs {
		seen[run.Workspace] = true
	}
	for workspace := range store.broken {
		seen[workspace] = true
	}
	return sortedKeys(seen), nil
}

// Pending returns copies of the pending runs of a workspace by deadline.
func (store *fakeStore) Pending(ctx context.Context, workspace string) ([]Run, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.broken[workspace] {
		return nil, errors.New("store of secret-workspace is broken")
	}
	runs := []Run{}
	for _, run := range store.runs {
		if run.Workspace == workspace && IsPendingState(run.State) {
			runs = append(runs, *run)
		}
	}
	sort.Slice(runs, func(left, right int) bool { return runs[left].Deadline < runs[right].Deadline })
	return runs, nil
}

// ClaimQueued follows the real store: recheck, stop reason, then starting.
func (store *fakeStore) ClaimQueued(ctx context.Context, workspace, assignmentID string, rejected bool, now time.Time) (*Run, string, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	stored, found := store.runs[assignmentID]
	if !found || stored.State != StateQueued {
		return nil, "", nil
	}
	claimed := *stored
	terminal := StopReason(claimed, now)
	if terminal == "" && !rejected {
		stored.State = StateStarting
	}
	return &claimed, terminal, nil
}

// fakeEventTexts are the event texts of assignments.py.
var fakeEventTexts = map[string]string{
	StateFailed:    "could not complete the fresh run",
	StateTimedOut:  "reached the fresh-run deadline",
	StateCancelled: "had the fresh run cancelled",
	StateUnknown:   "has an uncertain fresh-run state. Check the provider before launching a replacement",
}

// Record follows assignments.record: usage first, nothing for an unchanged state, an
// event for a stop.
func (store *fakeStore) Record(ctx context.Context, run Run, state string, result RunResult, now time.Time) (bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.failErr != nil {
		return false, store.failErr
	}
	if result.InputTokens != nil && result.OutputTokens != nil {
		store.usage = append(store.usage, result)
	}
	if run.State == state {
		return false, nil
	}
	store.runs[run.ID].State = state
	text, found := fakeEventTexts[state]
	if !found {
		return false, nil
	}
	store.events = append(store.events, storedEvent{room: run.Room, body: run.Label + " " + text + "."})
	return true, nil
}

// ReportUnconfirmed records the retry event.
func (store *fakeStore) ReportUnconfirmed(ctx context.Context, run Run, operation string, now time.Time) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	body := "could not confirm the fresh-run state for " + run.Label
	if operation == ToolCancel {
		body = "could not confirm cancellation of the fresh run for " + run.Label
	}
	store.events = append(store.events, storedEvent{room: run.Room, body: body})
	return nil
}

// providerCall is one call the fake provider received.
type providerCall struct {
	tool    string
	timeout int64
}

// fakeProvider answers each tool with a scripted function and records the calls.
type fakeProvider struct {
	mutex        sync.Mutex
	calls        []providerCall
	capabilities func() error
	answer       func(tool string) (RunResult, error)
}

// tools returns the names of the tools called so far.
func (provider *fakeProvider) tools() []string {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	tools := []string{}
	for _, call := range provider.calls {
		tools = append(tools, call.tool)
	}
	return tools
}

// CheckCapabilities runs the capability script.
func (provider *fakeProvider) CheckCapabilities(ctx context.Context, plugin Plugin) error {
	if provider.capabilities != nil {
		return provider.capabilities()
	}
	return nil
}

// Start records the call with its timeout.
func (provider *fakeProvider) Start(ctx context.Context, plugin Plugin, arguments StartArguments) (RunResult, error) {
	provider.mutex.Lock()
	provider.calls = append(provider.calls, providerCall{tool: ToolStart, timeout: arguments.TimeoutSeconds})
	provider.mutex.Unlock()
	return provider.answer(ToolStart)
}

// Operate records an inspect or cancel.
func (provider *fakeProvider) Operate(ctx context.Context, plugin Plugin, tool, assignmentID string) (RunResult, error) {
	provider.mutex.Lock()
	provider.calls = append(provider.calls, providerCall{tool: tool})
	provider.mutex.Unlock()
	return provider.answer(tool)
}

// states returns an answer function that replies with a fixed state per tool.
func states(byTool map[string]string) func(tool string) (RunResult, error) {
	return func(tool string) (RunResult, error) {
		state, found := byTool[tool]
		if !found {
			return RunResult{}, errors.New("provider unavailable")
		}
		return RunResult{State: state}, nil
	}
}
