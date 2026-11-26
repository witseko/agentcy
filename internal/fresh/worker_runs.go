package fresh

import (
	"context"
	"time"
)

// This file holds the decisions about one run, port of the methods of AssignmentWorker
// in assignment_worker.py. Each plugin call happens outside any store transaction.

// launch checks the plugin's capabilities, rechecks the run in the store, and starts it
// (AssignmentWorker.launch).
func (worker *Worker) launch(ctx context.Context, job Dispatch) error {
	rejected := worker.provider.CheckCapabilities(ctx, job.Plugin) != nil
	run, terminal, err := worker.store.ClaimQueued(ctx, job.Workspace, job.Start.AssignmentID, rejected, worker.now())
	if err != nil || run == nil {
		return err
	}
	worker.state.mutex.Lock()
	worker.state.runs[run.ID] = *run
	worker.state.mutex.Unlock()
	if terminal != "" || rejected {
		if terminal == "" {
			terminal = StateFailed
		}
		return worker.record(ctx, *run, terminal, RunResult{})
	}
	claimed := *run
	claimed.State = StateStarting
	worker.state.mutex.Lock()
	worker.state.runs[run.ID] = claimed
	worker.state.mutex.Unlock()
	return worker.startRun(ctx, job, claimed)
}

// startRun sends start once and inspects the same ID when the reply is lost
// (AssignmentWorker.start_run).
func (worker *Worker) startRun(ctx context.Context, job Dispatch, run Run) error {
	arguments := job.Start
	arguments.TimeoutSeconds = remainingSeconds(run.Deadline, worker.now())
	worker.scheduleInspection(run.ID)
	result, err := worker.provider.Start(ctx, job.Plugin, arguments)
	if err != nil {
		result, err = worker.provider.Operate(ctx, job.Plugin, ToolInspect, run.ID)
		if err != nil {
			return worker.retryOperation(ctx, run, ToolInspect)
		}
	}
	if result.State == StateUnknown {
		return worker.retryOperation(ctx, run, ToolInspect)
	}
	if result.State != StateStarting {
		return worker.record(ctx, run, result.State, result)
	}
	return nil
}

// remainingSeconds is max(1, ceil(deadline - now)) in seconds using exact nanosecond
// arithmetic (avoiding float64 precision loss on UnixNano).
func remainingSeconds(deadline int64, now time.Time) int64 {
	remaining := time.Unix(deadline, 0).Sub(now)
	if remaining <= 0 {
		return 1
	}
	seconds := int64(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	return max(1, seconds)
}

// needsInspect reports whether run is currently due for an inspect or cancel call.
func (worker *Worker) needsInspect(run Run, restart bool) bool {
	now := worker.now()
	terminal := StopReason(run, now)
	operation := ToolInspect
	if terminal != "" {
		operation = ToolCancel
	}
	worker.state.mutex.Lock()
	defer worker.state.mutex.Unlock()
	scheduled, retrying := worker.state.retries[run.ID]
	if retrying && scheduled.operation == operation && now.Before(scheduled.nextAttempt) {
		return false
	}
	if !restart && !retrying && terminal == "" && now.Before(worker.state.nextInspect[run.ID]) {
		return false
	}
	return true
}

// inspect checks an active run with the plugin, or cancels it when it must stop
// (AssignmentWorker.inspect). Outside the startup scan, a run without a stop reason is
// inspected at most once per inspect interval.
func (worker *Worker) inspect(ctx context.Context, run Run, restart bool) error {
	if !worker.needsInspect(run, restart) {
		return nil
	}
	terminal := StopReason(run, worker.now())
	operation := ToolInspect
	if terminal != "" {
		operation = ToolCancel
	}
	worker.scheduleInspection(run.ID)
	result := worker.operate(ctx, run, operation)
	if result.State == StateUnknown {
		return worker.reconcileUnconfirmed(ctx, run, operation)
	}
	if terminal != "" && IsPendingState(result.State) {
		return worker.pollStoppingRun(ctx, run, result)
	}
	state := result.State
	if terminal == StateTimedOut && state == StateCancelled {
		state = terminal
	}
	return worker.record(ctx, run, state, result)
}

// operate calls the run's plugin with operation. A plugin that is no longer configured
// or a failed call gives the unknown state.
func (worker *Worker) operate(ctx context.Context, run Run, operation string) RunResult {
	plugin, err := worker.plugins(run.Plugin)
	if err != nil {
		return RunResult{State: StateUnknown}
	}
	result, err := worker.provider.Operate(ctx, *plugin, operation, run.ID)
	if err != nil {
		return RunResult{State: StateUnknown}
	}
	return result
}

// scheduleInspection sets the next routine inspection of a run.
func (worker *Worker) scheduleInspection(id string) {
	worker.state.mutex.Lock()
	worker.state.nextInspect[id] = worker.now().Add(worker.timing.InspectInterval)
	worker.state.mutex.Unlock()
}

// pollStoppingRun rechecks an acknowledged cancel through the run deadline and a short
// grace period (AssignmentWorker.poll_stopping_run).
func (worker *Worker) pollStoppingRun(ctx context.Context, run Run, result RunResult) error {
	now := worker.now()
	graceDeadline := time.Unix(run.Deadline, 0).Add(worker.timing.CancelDeadlineGrace)
	if !now.Before(graceDeadline) {
		return worker.record(ctx, run, StateUnknown, RunResult{})
	}
	if result.State != run.State {
		if err := worker.record(ctx, run, result.State, RunResult{ExitCode: result.ExitCode}); err != nil {
			return err
		}
	}
	worker.state.mutex.Lock()
	worker.state.retries[run.ID] = retry{operation: ToolCancel, nextAttempt: now.Add(worker.timing.CancelPoll)}
	worker.state.mutex.Unlock()
	return nil
}

// reconcileUnconfirmed retries a check of provider work that may have started
// (AssignmentWorker.reconcile_unconfirmed).
func (worker *Worker) reconcileUnconfirmed(ctx context.Context, run Run, operation string) error {
	if run.State == StateQueued {
		// A restarted worker has no join code for the launch it never delivered.
		return worker.record(ctx, run, StateUnknown, RunResult{})
	}
	return worker.retryOperation(ctx, run, operation)
}

// notifyRoom invokes the configured notification callback under lock.
func (worker *Worker) notifyRoom(workspace, room string) {
	worker.state.mutex.Lock()
	notify := worker.state.notify
	worker.state.mutex.Unlock()
	if notify != nil {
		notify(workspace, room)
	}
}

// retryOperation backs off a failed check and caps consecutive cancel failures
// (AssignmentWorker.retry_operation). The first failure of an operation adds an event.
func (worker *Worker) retryOperation(ctx context.Context, run Run, operation string) error {
	worker.state.mutex.Lock()
	failures := 1
	if previous, found := worker.state.retries[run.ID]; found && previous.operation == operation {
		failures = min(previous.failures+1, cancelMaxFailures)
	}
	worker.state.mutex.Unlock()
	if operation == ToolCancel && failures >= cancelMaxFailures {
		return worker.record(ctx, run, StateUnknown, RunResult{})
	}
	if failures == 1 {
		if err := worker.store.ReportUnconfirmed(ctx, run, operation, worker.now()); err != nil {
			return err
		}
		worker.notifyRoom(run.Workspace, run.Room)
	}
	delay := min(time.Duration(1<<(failures-1))*time.Second, worker.timing.RetryMax)
	worker.state.mutex.Lock()
	worker.state.retries[run.ID] = retry{operation: operation, failures: failures, nextAttempt: worker.now().Add(delay)}
	worker.state.mutex.Unlock()
	return nil
}

// record commits a state change with the plugin's token usage and wakes the
// conversation when an event was added (AssignmentWorker.record).
func (worker *Worker) record(ctx context.Context, run Run, state string, result RunResult) error {
	added, err := worker.store.Record(ctx, run, state, result, worker.now())
	if err != nil {
		return err
	}
	worker.state.mutex.Lock()
	delete(worker.state.retries, run.ID)
	if IsPendingState(state) {
		updated := run
		updated.State = state
		worker.state.runs[run.ID] = updated
	} else {
		delete(worker.state.runs, run.ID)
		delete(worker.state.nextInspect, run.ID)
	}
	worker.state.mutex.Unlock()
	if added {
		worker.notifyRoom(run.Workspace, run.Room)
	}
	return nil
}
