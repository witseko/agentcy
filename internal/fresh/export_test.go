package fresh

import (
	"context"
	"maps"
	"slices"
)

// These hooks let the external tests in fixture_runs_test.go drive the worker one step
// at a time, as the Python tests call AssignmentWorker.scan and launch directly.

// ReconcileForTest runs the startup scan, sets notify, and marks the worker ready.
func (worker *Worker) ReconcileForTest(ctx context.Context, notify func(workspace, room string)) {
	worker.state.notify = notify
	worker.reconcile(ctx)
	worker.ready.Store(true)
}

// StepForTest runs one iteration of the worker loop.
func (worker *Worker) StepForTest(ctx context.Context) {
	worker.step(ctx)
}

// ScanForTest checks every remembered workspace, like AssignmentWorker.scan().
func (worker *Worker) ScanForTest(ctx context.Context) {
	workspaces := map[string]bool{}
	for _, run := range worker.state.runs {
		workspaces[run.Workspace] = true
	}
	for _, workspace := range slices.Sorted(maps.Keys(workspaces)) {
		worker.serviceWorkspace(ctx, workspace, false)
	}
}
