package conversation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/witseko/agentcy/internal/progress"
	"github.com/witseko/agentcy/internal/wire"
)

// This file holds the assignment rows of fresh runs and the reads that do not need a
// running plugin (assignments.py). Queueing is in assignment_queue.go.

const selectActiveAssignment = `SELECT assignment.id 
FROM assignment 
WHERE assignment.slot = ? AND assignment.state IN (?, ?, ?)`

// activeAssignment protects the credential of a launch that has not consumed its
// invitation yet.
func activeAssignment(ctx context.Context, tx *sql.Tx, slot int64) (bool, error) {
	return firstValueSet(ctx, tx, selectActiveAssignment, slot, progress.PendingStates[0], progress.PendingStates[1], progress.PendingStates[2])
}

const selectPendingAssignment = `SELECT assignment.id 
FROM assignment 
WHERE assignment.state IN (?, ?, ?)
 LIMIT ? OFFSET ?`

// hasPendingAssignment keeps the daemon alive while a provider run needs service.
func hasPendingAssignment(ctx context.Context, tx *sql.Tx) (bool, error) {
	return firstValueSet(ctx, tx, selectPendingAssignment, progress.PendingStates[0], progress.PendingStates[1], progress.PendingStates[2], 1, 0)
}

const selectOpenConversation = `SELECT conversation.id 
FROM conversation 
WHERE conversation.open = ?
 LIMIT ? OFFSET ?`

// hasOpenConversation reports whether a store holds an open conversation.
func hasOpenConversation(ctx context.Context, tx *sql.Tx) (bool, error) {
	return firstValueSet(ctx, tx, selectOpenConversation, 1, 1, 0)
}

// launchAuthority requires an active lead with an open conversation before a member may
// read launch settings or launch.
func launchAuthority(ctx context.Context, tx *sql.Tx, token string) (*participant, error) {
	member, err := authenticate(ctx, tx, token)
	if err != nil {
		return nil, err
	}
	if member.Retired {
		return nil, reject(retiredError)
	}
	if !isLeader(member.Role) {
		return nil, reject("Only a lead or co-lead can launch fresh workers")
	}
	if !member.Open {
		return nil, reject(conversationClosed)
	}
	return member, nil
}

const selectMcpTask = `SELECT assignment.id, assignment.state, assignment.created, assignment.updated, participant.label 
FROM assignment JOIN participant ON participant.id = assignment.slot 
WHERE assignment.mcp_task_hash = ?`

// mcpTask is the fresh run behind an MCP task.
type mcpTask struct {
	id      string
	state   string
	created sql.NullInt64
	updated sql.NullInt64
	label   string
}

// findMcpTask finds the assignment whose stored digest matches an MCP task ID. Each
// workspace has its own store, so an ID from another workspace is unknown here.
func findMcpTask(ctx context.Context, tx *sql.Tx, taskID string) (*mcpTask, error) {
	found := &mcpTask{}
	err := tx.QueryRowContext(ctx, selectMcpTask, digest(taskID)).Scan(&found.id, &found.state, &found.created, &found.updated, &found.label)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, reject(unknownMcpTask)
	}
	if err != nil {
		return nil, err
	}
	return found, nil
}

// mcpTaskStatus returns the launch state and timestamps of the run behind an MCP task.
func mcpTaskStatus(ctx context.Context, tx *sql.Tx, taskID string) (wire.Value, error) {
	found, err := findMcpTask(ctx, tx, taskID)
	if err != nil {
		return wire.Value{}, err
	}
	result := wire.NewObject()
	result.Set("launch_state", wire.TextValue(found.state))
	result.Set("participant", wire.TextValue(found.label))
	result.Set("created", nullableInt(found.created))
	result.Set("updated", nullableInt(found.updated))
	return result, nil
}

// cancelMcpTask asks the fresh-run worker to cancel a pending run. A finished run is
// left as it is.
func cancelMcpTask(ctx context.Context, tx *sql.Tx, taskID string) (wire.Value, error) {
	found, err := findMcpTask(ctx, tx, taskID)
	if err != nil {
		return wire.Value{}, err
	}
	if progress.IsPendingState(found.state) {
		if _, err := tx.ExecContext(ctx, "UPDATE assignment SET cancel_requested=? WHERE assignment.id = ?", 1, found.id); err != nil {
			return wire.Value{}, err
		}
	}
	return wire.NewObject(), nil
}
