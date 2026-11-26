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

// launchEnded is the answer to an operator launch for a slot whose fresh run is over.
const launchEndedError = "The fresh run for this slot has ended. Relaunch is not supported yet"

// queueRequest is one launch after its plugin has been resolved.
type queueRequest struct {
	workspace string
	name      string
	plugin    string
	timeout   int64
	model     string
	// mcpTask asks for an MCP task ID for a new assignment.
	mcpTask bool
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

// queueAssignment persists an approved assignment before its code goes to the worker
// (queue in assignments.py). With mcpTask, a new assignment also gets an MCP task ID,
// which the result carries once under "mcp_task"; only its digest is stored.
func queueAssignment(ctx context.Context, tx *sql.Tx, actor *participant, request queueRequest, now int64) (queuedLaunch, error) {
	previous, err := findAssignment(ctx, tx, actor.Room, request.name)
	if err != nil {
		return queuedLaunch{}, err
	}
	if previous != nil {
		if previous.retired {
			return queuedLaunch{}, rejectf("Participant is retired: %s", previous.label)
		}
		if previous.plugin != request.plugin {
			return queuedLaunch{}, reject("This participant already has a fresh-run assignment")
		}
		return queuedLaunch{result: assignmentResult(previous.id, previous.state), room: actor.Room}, nil
	}
	if err := protectLeadSlot(ctx, tx, actor.Room, request.name); err != nil {
		return queuedLaunch{}, err
	}
	code, slot, err := reserveWorker(ctx, tx, actor, request, now)
	if err != nil {
		return queuedLaunch{}, err
	}
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
	if _, err := tx.ExecContext(ctx, insertAssignment, assignmentID, slot, actor.Room, request.plugin, digest(code),
		fresh.StateQueued, now+request.timeout, round, taskHash, now, now); err != nil {
		return queuedLaunch{}, err
	}
	result := assignmentResult(assignmentID, fresh.StateQueued)
	if taskID != "" {
		task := singleField("task_id", wire.TextValue(taskID))
		task.Set("launch_state", wire.TextValue(fresh.StateQueued))
		task.Set("participant", wire.TextValue(request.name))
		task.Set("created", wire.IntValue(now))
		task.Set("updated", wire.IntValue(now))
		result.Set("mcp_task", task)
	}
	return queuedLaunch{result: result, code: code, room: actor.Room}, nil
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

// reserveWorker invites the worker slot with the same rules as a manual invitation and
// returns the join code and the slot (reserve_worker).
func reserveWorker(ctx context.Context, tx *sql.Tx, actor *participant, request queueRequest, now int64) (string, int64, error) {
	role := "worker"
	model := request.model
	invited, err := invite(ctx, tx, actor, inviteRequest{workspace: request.workspace, name: request.name, role: &role, model: &model}, now)
	if err != nil {
		return "", 0, err
	}
	return invited.code, invited.slotID, nil
}

// operatorQueue queues a fresh run for the human (launch in operator.py). Relaunching a
// slot whose run has ended is not supported yet.
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
	if previous != nil && !progress.IsPendingState(previous.state) {
		return queuedLaunch{}, reject(launchEndedError)
	}
	return queueAssignment(ctx, tx, &human.participant, queue, now)
}
