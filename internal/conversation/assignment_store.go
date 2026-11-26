package conversation

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/witseko/agentcy/internal/fresh"
	"github.com/witseko/agentcy/internal/store"
	"github.com/witseko/agentcy/internal/tokens"
)

// AssignmentStore gives the fresh-run worker the assignment rows of every workspace
// store (the store side of assignment_worker.py and the functions it calls in
// assignments.py). It is safe for concurrent use.
type AssignmentStore struct {
	dataDir string
	book    *usageBook
}

// The store must satisfy the worker's interface.
var _ fresh.Store = (*AssignmentStore)(nil)

// NewAssignmentStore returns the assignment store of the checked data directory.
// logger receives the warnings that Python logs.
func NewAssignmentStore(dataDir string, logger *log.Logger) *AssignmentStore {
	return &AssignmentStore{
		dataDir: dataDir,
		book:    &usageBook{dataDir: dataDir, settings: tokens.NewSettingsFile(dataDir), logger: logger},
	}
}

// eventTexts end the event of a run that stopped in one of these states (EVENTS in
// assignments.py). The worker's other state changes add no event.
var eventTexts = map[string]string{
	fresh.StateFailed:    "could not complete the fresh run",
	fresh.StateTimedOut:  "reached the fresh-run deadline",
	fresh.StateCancelled: "had the fresh run cancelled",
	fresh.StateUnknown:   "has an uncertain fresh-run state. Check the provider before launching a replacement",
}

// WorkspaceKeys lists the workspace stores.
func (assignments *AssignmentStore) WorkspaceKeys() ([]string, error) {
	return store.WorkspaceKeys(assignments.dataDir)
}

// write runs work in a write transaction of a workspace store and flushes any buffered
// ledger syncs after commit.
func (assignments *AssignmentStore) write(ctx context.Context, workspace string, work func(ctx context.Context, tx *sql.Tx) error) error {
	workspaceStore, err := store.Open(ctx, assignments.dataDir, workspace)
	if err != nil {
		return err
	}
	ctx, syncs := withLedgerSync(ctx)
	if err := workspaceStore.Write(ctx, func(tx *sql.Tx) error {
		return work(ctx, tx)
	}); err != nil {
		return err
	}
	return syncs.flush(ctx, assignments.book)
}

// read runs work in a read transaction of a workspace store.
func (assignments *AssignmentStore) read(ctx context.Context, workspace string, work func(tx *sql.Tx) error) error {
	workspaceStore, err := store.Open(ctx, assignments.dataDir, workspace)
	if err != nil {
		return err
	}
	return workspaceStore.Read(ctx, work)
}

// Pending reads the pending runs of one store, ordered by deadline.
func (assignments *AssignmentStore) Pending(ctx context.Context, workspace string) ([]fresh.Run, error) {
	var runs []fresh.Run
	err := assignments.read(ctx, workspace, func(tx *sql.Tx) error {
		var err error
		runs, err = pendingRuns(ctx, tx, workspace)
		return err
	})
	return runs, err
}

// ClaimQueued rechecks a dispatched run after the capability call and marks it starting
// when nothing stops it.
func (assignments *AssignmentStore) ClaimQueued(ctx context.Context, workspace, assignmentID string, rejected bool, now time.Time) (*fresh.Run, string, error) {
	var claimed *fresh.Run
	terminal := ""
	err := assignments.write(ctx, workspace, func(ctx context.Context, tx *sql.Tx) error {
		runs, err := pendingRuns(ctx, tx, workspace)
		if err != nil {
			return err
		}
		for index := range runs {
			if runs[index].ID == assignmentID {
				claimed = &runs[index]
			}
		}
		if claimed == nil || claimed.State != fresh.StateQueued {
			claimed = nil
			return nil
		}
		terminal = fresh.StopReason(*claimed, now)
		if terminal != "" || rejected {
			return nil
		}
		_, err = recordRunState(ctx, tx, nil, *claimed, fresh.StateStarting, fresh.RunResult{}, now.Unix())
		return err
	})
	return claimed, terminal, err
}

// Record saves a state change with optional plugin usage and its event.
func (assignments *AssignmentStore) Record(ctx context.Context, run fresh.Run, state string, result fresh.RunResult, now time.Time) (bool, error) {
	added := false
	err := assignments.write(ctx, run.Workspace, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		added, err = recordRunState(ctx, tx, assignments.book, run, state, result, now.Unix())
		return err
	})
	return added, err
}

// ReportUnconfirmed adds the event of a temporary provider failure.
func (assignments *AssignmentStore) ReportUnconfirmed(ctx context.Context, run fresh.Run, operation string, now time.Time) error {
	body := "The bridge could not confirm the fresh-run state for " + run.Label + ". It will retry inspection."
	if operation == fresh.ToolCancel {
		body = "The bridge could not confirm cancellation of the fresh run for " + run.Label + ". It will retry."
	}
	return assignments.write(ctx, run.Workspace, func(ctx context.Context, tx *sql.Tx) error {
		return recordEvent(ctx, tx, run.Room, run.Slot, body, now.Unix())
	})
}

const selectPendingRuns = `SELECT assignment.id, assignment.slot, assignment.room, assignment.plugin, assignment.invite_hash, assignment.state, assignment.deadline, assignment.round, assignment.cancel_requested, conversation.open, conversation.round AS current_round, conversation.round_deadline, participant.label, participant.retired
FROM assignment JOIN conversation ON conversation.id = assignment.room JOIN participant ON participant.id = assignment.slot
WHERE assignment.state IN (?, ?, ?) ORDER BY assignment.deadline`

// pendingRuns reads the pending runs with the circumstances that stop them
// (assignments.pending).
func pendingRuns(ctx context.Context, tx *sql.Tx, workspace string) ([]fresh.Run, error) {
	rows, err := tx.QueryContext(ctx, selectPendingRuns, pendingStates[0], pendingStates[1], pendingStates[2])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []fresh.Run{}
	for rows.Next() {
		run := fresh.Run{Workspace: workspace}
		var round, currentRound sql.NullString
		var roundDeadline sql.NullInt64
		if err := rows.Scan(&run.ID, &run.Slot, &run.Room, &run.Plugin, &run.InviteHash, &run.State, &run.Deadline,
			&round, &run.CancelRequested, &run.Open, &currentRound, &roundDeadline, &run.Label, &run.Retired); err != nil {
			return nil, err
		}
		run.Round, run.CurrentRound, run.RoundDeadline = round.String, currentRound.String, roundDeadline.Int64
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

const updateAssignmentState = `UPDATE assignment SET state=?, exit_code=?, updated=? WHERE assignment.id = ?`

const expireRunInvitation = `UPDATE participant SET expires=? WHERE participant.id = ? AND participant.token_hash IS NULL AND participant.invite_hash = ?`

// recordRunState saves a state transition, the plugin's usage when it reported both
// counts, and the event of a stop (assignments.record). It reports whether an event was
// added. book may be nil when result carries no usage.
func recordRunState(ctx context.Context, tx *sql.Tx, book *usageBook, run fresh.Run, state string, result fresh.RunResult, now int64) (bool, error) {
	if result.InputTokens != nil && result.OutputTokens != nil && book != nil {
		snapshot, err := tokens.RecordExact(ctx, tx, run.Room, run.Slot, tokens.ExactUsage{
			InputTokens: *result.InputTokens, OutputTokens: *result.OutputTokens, Source: tokens.SourcePlugin,
		}, now)
		if err != nil {
			return false, err
		}
		if err := book.sync(ctx, run.Workspace, snapshot); err != nil {
			return false, err
		}
	}
	if run.State == state {
		return false, nil
	}
	var exitCode any
	if result.ExitCode != nil {
		exitCode = *result.ExitCode
	}
	if _, err := tx.ExecContext(ctx, updateAssignmentState, state, exitCode, now, run.ID); err != nil {
		return false, err
	}
	if !fresh.IsPendingState(state) {
		if _, err := tx.ExecContext(ctx, expireRunInvitation, 0, run.Slot, run.InviteHash); err != nil {
			return false, err
		}
	}
	text, found := eventTexts[state]
	if !found {
		return false, nil
	}
	return true, recordEvent(ctx, tx, run.Room, run.Slot, run.Label+" "+text+".", now)
}
