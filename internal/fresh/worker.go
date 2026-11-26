package fresh

import (
	"context"
	"io"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// The worker's default intervals, from assignment_worker.py except where noted.
const (
	// DefaultInspectInterval spaces inspections of a run that has no stop reason. Python
	// inspects every second; the port waits 5 s, the MCP task poll interval
	// (POLL_INTERVAL_MS in tasks.py, plan fix L8). Stops are never delayed by it.
	DefaultInspectInterval = 5 * time.Second
	// DefaultRetryMax caps the backoff between failed checks (RETRY_MAX_SECONDS).
	DefaultRetryMax = 5 * time.Second
	// DefaultCancelPoll spaces the checks of a run whose cancel the plugin acknowledged
	// while it still runs (CANCEL_POLL_SECONDS).
	DefaultCancelPoll = 5 * time.Second
	// DefaultCancelDeadlineGrace is how long after the run deadline such a run may keep
	// stopping before its state becomes unknown (CANCEL_DEADLINE_GRACE_SECONDS).
	DefaultCancelDeadlineGrace = 30 * time.Second
)

// cancelMaxFailures is the number of consecutive failed cancel checks after which the
// state becomes unknown (CANCEL_MAX_FAILURES).
const cancelMaxFailures = 5

// maxConcurrentPluginCalls bounds concurrent blocking plugin subprocesses dispatched by
// the worker so one hung plugin cannot starve other workspaces.
const maxConcurrentPluginCalls = 8

// Timing holds the worker's intervals. Zero fields take the defaults.
type Timing struct {
	InspectInterval     time.Duration
	RetryMax            time.Duration
	CancelPoll          time.Duration
	CancelDeadlineGrace time.Duration
}

// withDefaults fills in the zero fields.
func (timing Timing) withDefaults() Timing {
	if timing.InspectInterval <= 0 {
		timing.InspectInterval = DefaultInspectInterval
	}
	if timing.RetryMax <= 0 {
		timing.RetryMax = DefaultRetryMax
	}
	if timing.CancelPoll <= 0 {
		timing.CancelPoll = DefaultCancelPoll
	}
	if timing.CancelDeadlineGrace <= 0 {
		timing.CancelDeadlineGrace = DefaultCancelDeadlineGrace
	}
	return timing
}

// WorkerConfig is what NewWorker needs. Store, Provider and Plugins are required.
type WorkerConfig struct {
	Store    Store
	Provider Provider
	// Plugins resolves a configured plugin by name (configured_plugin).
	Plugins func(name string) (*Plugin, error)
	Timing  Timing
	// Now returns the current time; time.Now when nil.
	Now func() time.Time
	// Logger receives fixed messages only; a discarding logger when nil.
	Logger *log.Logger
}

// Worker dispatches, inspects, cancels and reconciles fresh runs outside the request
// transactions. Port of AssignmentWorker in assignment_worker.py, made event-driven
// (plan fix L8): it reads the stores once at start, then works only when a launch is
// dispatched, a write may have stopped a run (Wake), or the nearest inspection, retry or
// deadline comes due. It never polls on a fixed tick and never lists the data
// directory again.
//
// Concurrency: Dispatch, Wake and Ready are safe from any goroutine; they touch only the
// inbox (guarded by its own mutex) and the ready flag. Everything in state is owned by
// the one goroutine that runs Run, or by a test that drives the steps itself.
type Worker struct {
	store    Store
	provider Provider
	plugins  func(name string) (*Plugin, error)
	timing   Timing
	now      func() time.Time
	logger   *log.Logger

	ready atomic.Bool
	inbox inbox
	state workerState
	sem   chan struct{}
}

// workerState is the worker's memory of pending runs. mutex guards all maps and notify.
type workerState struct {
	mutex sync.Mutex
	// notify wakes the waiting reads of a conversation.
	notify func(workspace, room string)
	// runs are the pending runs from the last read of each workspace, by assignment ID.
	runs map[string]Run
	// retries are the scheduled checks after a failure or an acknowledged cancel.
	retries map[string]retry
	// nextInspect is when a run without a stop reason is inspected next.
	nextInspect map[string]time.Time
	// inFlight tracks assignment IDs that currently have a blocking plugin call running.
	inFlight map[string]bool
}

// retry schedules another check of one run while counting consecutive failures (Retry
// in assignment_worker.py).
type retry struct {
	operation   string
	failures    int
	nextAttempt time.Time
}

// inbox hands launches and wakes from request goroutines to the worker.
type inbox struct {
	mutex sync.Mutex
	jobs  []Dispatch
	woken map[string]bool
	// signal holds at most one pending wake-up of the worker goroutine.
	signal chan struct{}
}

// NewWorker returns a worker that is not ready until Run has reconciled the stores.
func NewWorker(config WorkerConfig) *Worker {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	logger := config.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Worker{
		store:    config.Store,
		provider: config.Provider,
		plugins:  config.Plugins,
		timing:   config.Timing.withDefaults(),
		now:      now,
		logger:   logger,
		inbox:    inbox{woken: map[string]bool{}, signal: make(chan struct{}, 1)},
		sem:      make(chan struct{}, maxConcurrentPluginCalls),
		state: workerState{
			notify:      func(string, string) {},
			runs:        map[string]Run{},
			retries:     map[string]retry{},
			nextInspect: map[string]time.Time{},
			inFlight:    map[string]bool{},
		},
	}
}

// Ready reports whether the startup reconciliation has finished. Until then launches
// are refused, as in Python.
func (worker *Worker) Ready() bool {
	return worker.ready.Load()
}

// Dispatch queues a committed launch for the worker.
func (worker *Worker) Dispatch(job Dispatch) {
	worker.inbox.mutex.Lock()
	worker.inbox.jobs = append(worker.inbox.jobs, job)
	worker.inbox.mutex.Unlock()
	worker.signal()
}

// Wake tells the worker that a write to the workspace may have stopped a run: a close,
// retire, cancel request or round change. The worker rereads that workspace's pending
// runs only when it tracks some.
func (worker *Worker) Wake(workspace string) {
	worker.inbox.mutex.Lock()
	worker.inbox.woken[workspace] = true
	worker.inbox.mutex.Unlock()
	worker.signal()
}

// signal wakes the worker goroutine without blocking.
func (worker *Worker) signal() {
	select {
	case worker.inbox.signal <- struct{}{}:
	default:
	}
}

// take removes and returns the queued launches and woken workspaces.
func (box *inbox) take() ([]Dispatch, map[string]bool) {
	box.mutex.Lock()
	defer box.mutex.Unlock()
	jobs, woken := box.jobs, box.woken
	box.jobs, box.woken = nil, map[string]bool{}
	return jobs, woken
}

// Run reconciles every store once, becomes ready, then serves launches, wakes and due
// checks until ctx ends. notify wakes the waiting reads of a conversation after the
// worker added an event to it. A plugin call in flight when ctx ends is stopped and its
// process killed; the next daemon reconciles the run.
func (worker *Worker) Run(ctx context.Context, notify func(workspace, room string)) {
	worker.state.mutex.Lock()
	worker.state.notify = notify
	worker.state.mutex.Unlock()
	worker.reconcile(ctx)
	worker.ready.Store(true)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		worker.resetTimer(timer)
		select {
		case <-ctx.Done():
			return
		case <-worker.inbox.signal:
		case <-timer.C:
		}
		worker.stepAsync(ctx, &wg)
	}
}

// resetTimer sets the timer to the nearest due check, or stops it when no run is
// pending.
func (worker *Worker) resetTimer(timer *time.Timer) {
	timer.Stop()
	due, found := worker.nextDue()
	if !found {
		return
	}
	timer.Reset(max(due.Sub(worker.now()), 0))
}

// spawnPluginTask runs task in a goroutine bounded by worker.sem while marking runID
// in-flight so no duplicate call is launched for the same run. It signals the worker
// loop when done.
func (worker *Worker) spawnPluginTask(ctx context.Context, wg *sync.WaitGroup, runID string, task func()) {
	worker.state.mutex.Lock()
	if worker.state.inFlight[runID] {
		worker.state.mutex.Unlock()
		return
	}
	worker.state.inFlight[runID] = true
	worker.state.mutex.Unlock()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			worker.state.mutex.Lock()
			delete(worker.state.inFlight, runID)
			worker.state.mutex.Unlock()
			worker.signal()
		}()
		select {
		case worker.sem <- struct{}{}:
			defer func() { <-worker.sem }()
		case <-ctx.Done():
			return
		}
		task()
	}()
}

// step serves the queued launches and due checks synchronously (used by unit tests).
func (worker *Worker) step(ctx context.Context) {
	jobs, workspaces := worker.inbox.take()
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		if err := worker.launch(ctx, job); err != nil {
			worker.logger.Print("Fresh-run dispatch failed")
		}
		workspaces[job.Workspace] = true
	}
	worker.addDueWorkspaces(workspaces)
	for _, workspace := range sortedKeys(workspaces) {
		if ctx.Err() != nil {
			return
		}
		if worker.tracks(workspace) {
			worker.serviceWorkspace(ctx, workspace, false)
		}
	}
}

// stepAsync dispatches queued launches and due inspections concurrently via the bounded
// worker pool so one slow or hung plugin does not block other workspaces.
func (worker *Worker) stepAsync(ctx context.Context, wg *sync.WaitGroup) {
	jobs, workspaces := worker.inbox.take()
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		job := job
		worker.spawnPluginTask(ctx, wg, job.Start.AssignmentID, func() {
			if err := worker.launch(ctx, job); err != nil {
				worker.logger.Print("Fresh-run dispatch failed")
			}
		})
	}
	worker.addDueWorkspaces(workspaces)
	for _, workspace := range sortedKeys(workspaces) {
		if ctx.Err() != nil {
			return
		}
		if worker.tracks(workspace) {
			worker.serviceWorkspaceAsync(ctx, wg, workspace, false)
		}
	}
}

// addDueWorkspaces adds every workspace with a remembered run that is due and not
// currently in flight.
func (worker *Worker) addDueWorkspaces(workspaces map[string]bool) {
	now := worker.now()
	worker.state.mutex.Lock()
	defer worker.state.mutex.Unlock()
	for _, run := range worker.state.runs {
		if worker.state.inFlight[run.ID] {
			continue
		}
		if due, found := worker.runDueLocked(run, now); found && !due.After(now) {
			workspaces[run.Workspace] = true
		}
	}
}

// reconcile is the startup scan: it checks every pending run of every store with the
// plugin, queued runs included, as AssignmentWorker.scan(restart=True) does, using
// bounded concurrency across workspaces.
func (worker *Worker) reconcile(ctx context.Context) {
	workspaces, err := worker.store.WorkspaceKeys()
	if err != nil {
		worker.logger.Print("Fresh-run reconciliation failed")
		return
	}
	var wg sync.WaitGroup
	for _, workspace := range workspaces {
		if ctx.Err() != nil {
			break
		}
		worker.serviceWorkspaceAsync(ctx, &wg, workspace, true)
	}
	wg.Wait()
}

// serviceWorkspace rereads one store's pending runs, remembers them, and checks each
// run that is due synchronously.
func (worker *Worker) serviceWorkspace(ctx context.Context, workspace string, restart bool) {
	runs, err := worker.store.Pending(ctx, workspace)
	if err != nil {
		worker.logger.Print("Fresh-run reconciliation failed")
		return
	}
	worker.remember(workspace, runs)
	for _, run := range runs {
		if !restart && run.State == StateQueued {
			// A queued run of this process belongs to its Dispatch.
			continue
		}
		if err := worker.inspect(ctx, run, restart); err != nil {
			worker.logger.Print("Fresh-run reconciliation failed")
			return
		}
	}
}

// serviceWorkspaceAsync dispatches one bounded concurrent worker task per workspace so
// slow plugins in one workspace cannot starve others, while preserving deterministic
// deadline order for runs within the same workspace.
func (worker *Worker) serviceWorkspaceAsync(ctx context.Context, wg *sync.WaitGroup, workspace string, restart bool) {
	worker.spawnPluginTask(ctx, wg, "workspace:"+workspace, func() {
		worker.serviceWorkspace(ctx, workspace, restart)
	})
}

// remember replaces the remembered runs of a workspace with the ones just read and
// prunes retry/inspection state for runs that disappeared.
func (worker *Worker) remember(workspace string, runs []Run) {
	present := make(map[string]bool, len(runs))
	for _, run := range runs {
		present[run.ID] = true
	}
	worker.state.mutex.Lock()
	defer worker.state.mutex.Unlock()
	for id, run := range worker.state.runs {
		if run.Workspace == workspace && !present[id] {
			delete(worker.state.runs, id)
			delete(worker.state.retries, id)
			delete(worker.state.nextInspect, id)
		}
	}
	for _, run := range runs {
		worker.state.runs[run.ID] = run
	}
}

// tracks reports whether the worker remembers a pending run of the workspace.
func (worker *Worker) tracks(workspace string) bool {
	worker.state.mutex.Lock()
	defer worker.state.mutex.Unlock()
	for _, run := range worker.state.runs {
		if run.Workspace == workspace {
			return true
		}
	}
	return false
}

// nextDue returns the earliest time any remembered run (not currently in flight) needs a
// check.
func (worker *Worker) nextDue() (time.Time, bool) {
	var earliest time.Time
	found := false
	now := worker.now()
	worker.state.mutex.Lock()
	defer worker.state.mutex.Unlock()
	for _, run := range worker.state.runs {
		if worker.state.inFlight[run.ID] {
			continue
		}
		due, ok := worker.runDueLocked(run, now)
		if ok && (!found || due.Before(earliest)) {
			earliest, found = due, true
		}
	}
	return earliest, found
}

// runDueLocked returns when a run needs its next check. Caller must hold state.mutex.
func (worker *Worker) runDueLocked(run Run, now time.Time) (time.Time, bool) {
	if run.State == StateQueued {
		return time.Time{}, false
	}
	if StopReason(run, now) != "" {
		if scheduled, found := worker.state.retries[run.ID]; found && scheduled.operation == ToolCancel {
			return scheduled.nextAttempt, true
		}
		return now, true
	}
	due := worker.state.nextInspect[run.ID]
	if scheduled, found := worker.state.retries[run.ID]; found {
		due = scheduled.nextAttempt
	}
	for _, deadline := range []int64{run.Deadline, run.RoundDeadline} {
		if deadline != 0 {
			at := time.Unix(deadline, 0)
			if at.Before(due) {
				due = at
			}
		}
	}
	return due, true
}

// sortedKeys returns the keys of a set in order, so stores are visited predictably.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
