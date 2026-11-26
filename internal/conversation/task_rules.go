package conversation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// validateDueMinutes requires a timeline on every new task and rejects one on any other
// message kind.
func validateDueMinutes(kind string, dueMinutes *int64) error {
	if kind == kindTask && dueMinutes == nil {
		return reject("A task requires a timeline. Set due_minutes to the number of minutes you expect " +
			"the task to take")
	}
	if kind != kindTask && dueMinutes != nil {
		return reject("Only a kind='task' message accepts due_minutes")
	}
	return nil
}

// validateTransition checks role and ownership before a new task, handoff, resume, or
// handoff response is stored.
func validateTransition(ctx context.Context, tx *sql.Tx, member *participant, content postContent) error {
	kind := content.kind.String
	if kind == kindTask || kind == kindHandoff {
		if err := validateTaskRecipient(ctx, tx, content.recipient); err != nil {
			return err
		}
	}
	switch kind {
	case kindTask:
		if !isLeader(member.Role) {
			return reject("Only a lead or co-lead can assign a task")
		}
		if !content.recipient.Valid {
			return reject("A task requires an addressee")
		}
	case kindHandoff:
		return validateHandoff(ctx, tx, member, content)
	case kindResume:
		return validateResume(ctx, tx, member, content.replyTo)
	case kindAccept, kindDecline:
		return validateResponse(ctx, tx, member, content.replyTo)
	}
	return nil
}

const selectRecipientRole = `SELECT participant.role, participant.retired 
FROM participant 
WHERE participant.id = ?`

// validateTaskRecipient rejects assigning a task or handoff to the human or a retired
// member.
func validateTaskRecipient(ctx context.Context, tx *sql.Tx, recipient sql.NullInt64) error {
	if !recipient.Valid {
		return nil
	}
	var role int64
	var retired bool
	err := tx.QueryRowContext(ctx, selectRecipientRole, recipient.Int64).Scan(&role, &retired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if role == roleHuman {
		return rejectf("The %s cannot own a task. Address it to a member", roleLabel(roleHuman))
	}
	if retired {
		return reject("Cannot assign a task or handoff to a retired participant")
	}
	return nil
}

// openTask returns the unfinished task with the given ID, or nil.
func openTask(ctx context.Context, tx *sql.Tx, room string, taskID int64) (*task, error) {
	board, err := readTasks(ctx, tx, room, taskID)
	if err != nil {
		return nil, err
	}
	state := board.tasks[taskID]
	if state == nil || state.done {
		return nil, nil
	}
	return state, nil
}

// ownedBy reports whether a task's owner is the given member.
func (state *task) ownedBy(member int64) bool {
	return state.owner.Valid && state.owner.Int64 == member
}

// validateResume allows only the current owner to clear a blocker on the original task.
func validateResume(ctx context.Context, tx *sql.Tx, member *participant, taskID sql.NullInt64) error {
	const notOpen = "A resume must reply to the original open task"
	if !taskID.Valid {
		return reject(notOpen)
	}
	state, err := openTask(ctx, tx, member.Room, taskID.Int64)
	if err != nil {
		return err
	}
	if state == nil {
		return reject(notOpen)
	}
	if !state.ownedBy(member.ID) {
		return reject("Only the task owner can resume work")
	}
	if !state.blocked {
		return reject("This task is not blocked")
	}
	return nil
}

// changesBlockedState recognizes a new block or resume transition even when its text
// matches an earlier post, so deduplication does not swallow it.
func changesBlockedState(ctx context.Context, tx *sql.Tx, member *participant, content postContent) (bool, error) {
	kind := content.kind.String
	if (kind != kindBlocker && kind != kindResume) || !content.replyTo.Valid {
		return false, nil
	}
	state, err := openTask(ctx, tx, member.Room, content.replyTo.Int64)
	if err != nil || state == nil {
		return false, err
	}
	return state.ownedBy(member.ID) && state.blocked != (kind == kindBlocker), nil
}

const selectOwnerActivity = `SELECT participant.retired, assignment.state AS launch 
FROM participant LEFT OUTER JOIN assignment ON assignment.slot = participant.id AND assignment.invite_hash = participant.invite_hash 
WHERE participant.id = ?`

// ownerIsInactive recognizes a task owner who is retired or whose fresh run has ended.
func ownerIsInactive(ctx context.Context, tx *sql.Tx, owner sql.NullInt64) (bool, error) {
	var retired bool
	var launch sql.NullString
	err := tx.QueryRowContext(ctx, selectOwnerActivity, owner).Scan(&retired, &launch)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return retired || launchEnded(launch), nil
}

// validateHandoff allows the current owner, or a lead recovering an inactive owner's
// task, to offer a handoff.
func validateHandoff(ctx context.Context, tx *sql.Tx, member *participant, content postContent) error {
	if !content.recipient.Valid || content.recipient.Int64 == member.ID {
		return reject("A handoff requires another member as addressee")
	}
	const notOpen = "A handoff must reply to an open task"
	if !content.replyTo.Valid {
		return reject(notOpen)
	}
	state, err := openTask(ctx, tx, member.Room, content.replyTo.Int64)
	if err != nil {
		return err
	}
	if state == nil {
		return reject(notOpen)
	}
	canRecover := false
	if isLeader(member.Role) {
		if canRecover, err = ownerIsInactive(ctx, tx, state.owner); err != nil {
			return err
		}
	}
	if !state.ownedBy(member.ID) && !canRecover {
		return reject("Only the task owner can offer a handoff")
	}
	if state.handoff != 0 {
		return reject("This task already has a pending handoff")
	}
	return nil
}

const selectHandoffTask = `SELECT message.reply_to 
FROM message 
WHERE message.room = ? AND message.id = ? AND message.kind = ? AND message.recipient = ?`

// validateResponse allows only the receiver to answer an outstanding handoff.
func validateResponse(ctx context.Context, tx *sql.Tx, member *participant, handoffID sql.NullInt64) error {
	taskID, err := firstInteger(ctx, tx, selectHandoffTask, member.Room, handoffID, kindHandoff, member.ID)
	if err != nil {
		return err
	}
	if !taskID.Valid {
		return reject("A handoff response must reply to a handoff addressed to you")
	}
	board, err := readTasks(ctx, tx, member.Room, taskID.Int64)
	if err != nil {
		return err
	}
	state := board.tasks[taskID.Int64]
	if state == nil || state.done || !handoffID.Valid || state.handoff != handoffID.Int64 {
		return reject("This handoff is no longer pending")
	}
	return nil
}

// unclosedDoneWarning warns when a kind='done' post did not close any of the author's
// open tasks. It returns "" without a warning.
func unclosedDoneWarning(ownedBefore, ownedAfter []taskSummary) string {
	if len(ownedAfter) == 0 || len(ownedAfter) < len(ownedBefore) {
		return ""
	}
	ids := make([]string, 0, len(ownedAfter))
	for _, summary := range ownedAfter {
		ids = append(ids, fmt.Sprintf("#%d", summary.id))
	}
	return "Open task(s) " + strings.Join(ids, ", ") + " remain open. Reply with reply_to=<task_id> and kind='done' to close."
}
