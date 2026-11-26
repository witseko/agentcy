package fresh

import (
	"context"
	"time"
)

// Store is the worker's access to the assignment rows of the workspace stores.
// internal/conversation implements it, so the conversation rules (events, token usage,
// invitation expiry) stay in one place. Every method runs in its own transaction.
type Store interface {
	// WorkspaceKeys lists the workspace stores in the data directory.
	WorkspaceKeys() ([]string, error)
	// Pending reads the pending runs of one store with the circumstances that stop them,
	// ordered by deadline (assignments.pending).
	Pending(ctx context.Context, workspace string) ([]Run, error)
	// ClaimQueued finds the queued run with the ID and computes its stop reason with
	// StopReason. When there is none and rejected is false it marks the run starting.
	// It returns the run as read before that change, or nil when the run is no longer
	// queued (the transaction in AssignmentWorker.launch).
	ClaimQueued(ctx context.Context, workspace, assignmentID string, rejected bool, now time.Time) (*Run, string, error)
	// Record saves a state change with its optional plugin token usage and its event,
	// and reports whether an event was added (assignments.record).
	Record(ctx context.Context, run Run, state string, result RunResult, now time.Time) (bool, error)
	// ReportUnconfirmed adds the event of a temporary provider failure for an inspect or
	// cancel operation (assignments.report_unconfirmed).
	ReportUnconfirmed(ctx context.Context, run Run, operation string, now time.Time) error
}

// Run is one pending assignment row with the state of its conversation and slot. Empty
// strings and zero numbers stand for SQL NULL, as both are false in Python.
type Run struct {
	Workspace       string
	ID              string
	Room            string
	Slot            int64
	Label           string
	Plugin          string
	InviteHash      string
	State           string
	Deadline        int64
	Round           string
	CancelRequested bool
	Open            bool
	Retired         bool
	CurrentRound    string
	RoundDeadline   int64
}

// StopReason returns "cancelled" when the run must stop because its conversation closed,
// its slot retired, a cancel was requested, its round ended or the round deadline
// passed; "timed_out" when its own deadline passed; and "" otherwise
// (AssignmentWorker.stop_reason).
func StopReason(run Run, now time.Time) string {
	unix := now.Unix()
	if !run.Open || run.Retired || run.CancelRequested || (run.Round != "" && run.Round != run.CurrentRound) {
		return StateCancelled
	}
	if run.RoundDeadline != 0 && run.RoundDeadline <= unix {
		return StateCancelled
	}
	if run.Deadline <= unix {
		return StateTimedOut
	}
	return ""
}

// Dispatch is a committed launch with its short-lived join code, handed from the
// request that queued it to the worker. Start holds the join code, so a Dispatch is
// never logged.
type Dispatch struct {
	Workspace string
	Plugin    Plugin
	Start     StartArguments
}
