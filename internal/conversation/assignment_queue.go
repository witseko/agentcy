package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/fresh"
	"github.com/witseko/agentcy/internal/progress"
	"github.com/witseko/agentcy/internal/wire"
)

// This file queues a fresh run: the checks and the assignment row that a launch writes
// before the worker gets the join code (queue and its helpers in assignments.py, launch
// in operator.py).

// launchEndedError is the answer to an operator launch without relaunch for a slot whose
// fresh run is over.
const launchEndedError = "The fresh run for this slot has ended. Pass relaunch to start a new run"

// queueRequest is one launch after its plugin has been resolved.
type queueRequest struct {
	workspace string
	name      string
	plugin    string
	timeout   int64
	model     string
	// mcpTask asks for an MCP task ID for a new assignment.
	mcpTask bool
	// relaunch replaces an ended fresh run (or a joined/retired worker slot) with a new run.
	relaunch bool
}

// queuedLaunch is the result of queueing. code is "" for a repeated launch of an
// existing assignment, which dispatches nothing.
type queuedLaunch struct {
	result wire.Value
	code   string
	room   string
}

// previousAssignment is an existing assignment of a slot, as find_assignment reads it.
type previousAssignment struct {
	id      string
	state   string
	plugin  string
	label   string
	retired bool
}

const selectRoomAssignments = `SELECT assignment.id, assignment.state, assignment.plugin, participant.label, participant.retired
FROM assignment JOIN participant ON participant.id = assignment.slot
WHERE assignment.room = ?`

// findAssignment resolves the slot's assignment case-insensitively, with the same rule
// as invitations (find_assignment).
func findAssignment(ctx context.Context, tx *sql.Tx, room, name string) (*previousAssignment, error) {
	rows, err := tx.QueryContext(ctx, selectRoomAssignments, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var found previousAssignment
		if err := rows.Scan(&found.id, &found.state, &found.plugin, &found.label, &found.retired); err != nil {
			return nil, err
		}
		if sameName(found.label, name) {
			return &found, nil
		}
	}
	return nil, rows.Err()
}

const insertAssignment = `INSERT INTO assignment (id, slot, room, plugin, invite_hash, state, deadline, round, mcp_task_hash, created, updated) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const replaceAssignment = `UPDATE assignment SET id=?, plugin=?, invite_hash=?, state=?, deadline=?, round=?, exit_code=NULL, mcp_task_hash=?, cancel_requested=0, created=?, updated=? WHERE assignment.slot = ?`

// reservedWorker holds the worker slot and fresh join code prepared for an assignment.
type reservedWorker struct {
	slotID          int64
	label           string
	code            string
	replaceExisting bool
}

// queueAssignment persists an approved assignment before its code goes to the worker
// (queue in assignments.py). With mcpTask, a new assignment also gets an MCP task ID,
// which the result carries once under "mcp_task"; only its digest is stored.
func queueAssignment(ctx context.Context, tx *sql.Tx, actor *participant, request queueRequest, now int64) (queuedLaunch, error) {
	previous, err := findAssignment(ctx, tx, actor.Room, request.name)
	if err != nil {
		return queuedLaunch{}, err
	}
	if previous != nil && !request.relaunch {
		if previous.retired {
			return queuedLaunch{}, rejectf("Participant is retired: %s", previous.label)
		}
		if previous.plugin != request.plugin {
			return queuedLaunch{}, reject("This participant already has a fresh-run assignment")
		}
		return queuedLaunch{result: assignmentResult(previous.id, previous.state), room: actor.Room}, nil
	}
	if previous != nil && progress.IsPendingState(previous.state) {
		return queuedLaunch{}, rejectf("Fresh run is still active: %s", previous.label)
	}
	if err := protectLeadSlot(ctx, tx, actor.Room, request.name); err != nil {
		return queuedLaunch{}, err
	}
	reserved, err := reserveWorker(ctx, tx, actor, request, previous != nil, now)
	if err != nil {
		return queuedLaunch{}, err
	}
	return storeQueuedAssignment(ctx, tx, actor, request, reserved, now)
}

// storeQueuedAssignment inserts a new assignment row or replaces an ended one in place.
func storeQueuedAssignment(ctx context.Context, tx *sql.Tx, actor *participant, request queueRequest, reserved reservedWorker, now int64) (queuedLaunch, error) {
	assignmentID := tokenHex(16)
	var taskID string
	var taskHash any
	if request.mcpTask {
		taskID = tokenURLSafe(32)
		taskHash = digest(taskID)
	}
	var round any
	if actor.Round.Valid {
		round = actor.Round.String
	}
	deadline := now + request.timeout
	var err error
	if reserved.replaceExisting {
		_, err = tx.ExecContext(ctx, replaceAssignment, assignmentID, request.plugin, digest(reserved.code),
			fresh.StateQueued, deadline, round, taskHash, now, now, reserved.slotID)
	} else {
		_, err = tx.ExecContext(ctx, insertAssignment, assignmentID, reserved.slotID, actor.Room, request.plugin, digest(reserved.code),
			fresh.StateQueued, deadline, round, taskHash, now, now)
	}
	if err != nil {
		return queuedLaunch{}, err
	}
	result := assignmentResult(assignmentID, fresh.StateQueued)
	if taskID != "" {
		task := singleField("task_id", wire.TextValue(taskID))
		task.Set("launch_state", wire.TextValue(fresh.StateQueued))
		task.Set("participant", wire.TextValue(reserved.label))
		task.Set("created", wire.IntValue(now))
		task.Set("updated", wire.IntValue(now))
		result.Set("mcp_task", task)
	}
	return queuedLaunch{result: result, code: reserved.code, room: actor.Room}, nil
}

// assignmentResult is the answer to a launch: the assignment ID and its state.
func assignmentResult(id, state string) wire.Value {
	result := singleField("assignment_id", wire.TextValue(id))
	result.Set("state", wire.TextValue(state))
	return result
}

const selectSlotRoles = `SELECT participant.label, participant.role
FROM participant
WHERE participant.room = ?`

// protectLeadSlot leaves an unused lead or co-lead invitation to its intended user
// (protect_lead_slot).
func protectLeadSlot(ctx context.Context, tx *sql.Tx, room, name string) error {
	rows, err := tx.QueryContext(ctx, selectSlotRoles, room)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		var role int64
		if err := rows.Scan(&label, &role); err != nil {
			return err
		}
		if sameName(label, name) && isLeader(role) {
			return reject("Fresh workers cannot replace a lead or co-lead slot")
		}
	}
	return rows.Err()
}

// reserveWorker invites or reinvites the worker slot and returns the join code, slot ID
// and canonical label (reserve_worker).
func reserveWorker(ctx context.Context, tx *sql.Tx, actor *participant, request queueRequest, replaceExisting bool, now int64) (reservedWorker, error) {
	reinvite := false
	if request.relaunch {
		var err error
		reinvite, err = relaunchNeedsReinvite(ctx, tx, actor.Room, request.name)
		if err != nil {
			return reservedWorker{}, err
		}
	}
	role := "worker"
	model := request.model
	invited, err := invite(ctx, tx, actor, inviteRequest{
		workspace: request.workspace, name: request.name, role: &role, model: &model, reinvite: reinvite,
	}, now)
	if err != nil {
		return reservedWorker{}, err
	}
	return reservedWorker{
		slotID: invited.slotID, label: invited.label, code: invited.code, replaceExisting: replaceExisting,
	}, nil
}

// relaunchNeedsReinvite checks that the relaunched slot exists and reports whether it
// must be reinvited (joined or retired) rather than reissued.
func relaunchNeedsReinvite(ctx context.Context, tx *sql.Tx, room, name string) (bool, error) {
	slots, err := inviteSlots(ctx, tx, room)
	if err != nil {
		return false, err
	}
	for _, slot := range slots {
		if sameName(slot.label, name) {
			return slot.joined || slot.retired, nil
		}
	}
	return false, reject(unknownParticipant)
}

// operatorQueue queues a fresh run for the human (launch in operator.py). Without
// relaunch, a slot whose run has ended is refused.
func operatorQueue(ctx context.Context, tx *sql.Tx, request *wire.OperatorLaunch, queue queueRequest, now int64) (queuedLaunch, error) {
	human, err := writingActor(ctx, tx, request.Conversation)
	if err != nil {
		return queuedLaunch{}, err
	}
	if !human.Open {
		return queuedLaunch{}, reject(conversationClosed)
	}
	previous, err := findAssignment(ctx, tx, request.Conversation, request.Name)
	if err != nil {
		return queuedLaunch{}, err
	}
	if previous != nil && !queue.relaunch && !progress.IsPendingState(previous.state) {
		return queuedLaunch{}, reject(launchEndedError)
	}
	return queueAssignment(ctx, tx, &human.participant, queue, now)
}
