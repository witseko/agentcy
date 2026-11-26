package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/tokens"
	"github.com/witseko/agentcy/internal/wire"
)

// The select list of a member read. The wake column is "?" for a lead or co-lead, who
// wakes for every message, and the wake predicate for everyone else.
const (
	readColumnsStart = `SELECT message.id, participant.label, message.body, message.kind, recipient.label AS "to", message.reply_to, message.due_minutes, participant.role AS author_role, `
	leaderWake       = `?`
	memberWake       = `message.recipient = ? OR message.recipient IS NULL AND message.kind IS NOT ? AND message.kind IS NOT ?`
	readColumnsEnd   = ` AS wake, blocker_task.id IS NOT NULL AS task_blocker 
FROM message JOIN participant ON message.author = participant.id LEFT OUTER JOIN participant AS recipient ON message.recipient = recipient.id LEFT OUTER JOIN message AS blocker_task ON message.kind = ? AND blocker_task.id = message.reply_to AND blocker_task.room = message.room AND blocker_task.kind = ? 
WHERE `
	unreadCondition = `message.room = ? AND message.id > ?`
	othersCondition = ` AND (message.author != ? OR message.kind IN (?, ?))`
	readOrder       = ` ORDER BY message.id
 LIMIT ? OFFSET ?`
	memberWakeFilter   = ` AND (message.recipient = ? OR message.recipient IS NULL AND message.kind IS NOT ? AND message.kind IS NOT ?)`
	selectLaterWakeKey = `SELECT message.id 
FROM message 
WHERE `
)

// unreadFilter is the WHERE condition of a read with its arguments: from cursor zero
// the whole transcript, after it only other members' messages plus events and released
// proposals.
func unreadFilter(member *participant, after int64) (string, []any) {
	condition := unreadCondition
	arguments := []any{member.Room, after}
	if after != 0 {
		condition += othersCondition
		arguments = append(arguments, member.ID, eventKind, kindProposal)
	}
	return condition, arguments
}

// wakeColumn returns the wake select expression and its arguments.
func wakeColumn(member *participant) (string, []any) {
	if isLeader(member.Role) {
		return leaderWake, []any{true}
	}
	return memberWake, []any{member.ID, kindBlocker, eventKind}
}

// readRows loads one page plus one row of unread messages.
func readRows(ctx context.Context, tx *sql.Tx, member *participant, after int64) ([]messageRow, error) {
	wake, wakeArguments := wakeColumn(member)
	condition, conditionArguments := unreadFilter(member, after)
	query := readColumnsStart + wake + readColumnsEnd + condition + readOrder
	arguments := append([]any{}, wakeArguments...)
	arguments = append(arguments, kindBlocker, kindTask)
	arguments = append(arguments, conditionArguments...)
	arguments = append(arguments, pageSize+1, 0)
	return queryMessageRows(ctx, tx, query, true, arguments...)
}

// laterWake looks past a full page for a message that wakes a member who is not a lead
// or co-lead. A leader wakes for every message, so a full page always wakes it.
func laterWake(ctx context.Context, tx *sql.Tx, member *participant, after, lastID int64) (bool, error) {
	condition, arguments := unreadFilter(member, after)
	query := selectLaterWakeKey + condition + memberWakeFilter + " AND message.id > ?\n LIMIT ? OFFSET ?"
	arguments = append(arguments, member.ID, kindBlocker, eventKind, lastID, 1, 0)
	return firstValueSet(ctx, tx, query, arguments...)
}

// readPage returns the full view from cursor zero, and only other members' new messages
// after it, without recording usage (read in repository.py with record_usage=False). It
// also returns the page's message bodies for the usage estimate.
func readPage(ctx context.Context, tx *sql.Tx, member *participant, after int64) (wire.Value, []string, error) {
	rows, err := readRows(ctx, tx, member, after)
	if err != nil {
		return wire.Value{}, nil, err
	}
	messages, bodies := messagePage(rows)
	more := len(rows) > pageSize
	hasWake := false
	for _, row := range rows {
		hasWake = hasWake || row.Wake
	}
	if more && !hasWake {
		if hasWake, err = laterWake(ctx, tx, member, after, rows[len(rows)-1].ID); err != nil {
			return wire.Value{}, nil, err
		}
	}
	cursor := after
	if len(messages) > 0 {
		cursor = rows[len(messages)-1].ID
	}
	result := wire.NewObject()
	result.Set("messages", wire.ListValue(messages))
	result.Set("more", wire.BoolValue(more))
	result.Set("cursor", wire.IntValue(cursor))
	result.Set("wake", wire.BoolValue(hasWake))
	roundFields(result, member.Round, member.RoundDeadline)
	if blockers := taskBlockers(rows[:len(messages)]); len(blockers) > 0 {
		result.Set("_task_blockers", wire.ListValue(blockers))
	}
	if err := catchUpFields(ctx, tx, member, after, result); err != nil {
		return wire.Value{}, nil, err
	}
	if !member.Open {
		result.Set("closed", wire.BoolValue(true))
	}
	if member.Retired {
		result.Set("retired", wire.BoolValue(true))
	}
	return result, bodies, nil
}

// taskBlockers lists the IDs of blockers that reply to a task. The shim reads this
// internal key to explain blocked tasks.
func taskBlockers(rows []messageRow) []wire.Value {
	var blockers []wire.Value
	for _, row := range rows {
		if row.TaskBlocker {
			blockers = append(blockers, wire.IntValue(row.ID))
		}
	}
	return blockers
}

// catchUpFields adds the brief, the role and the roster to a read from cursor zero.
func catchUpFields(ctx context.Context, tx *sql.Tx, member *participant, after int64, result wire.Value) error {
	if after != 0 {
		return nil
	}
	members, err := roster(ctx, tx, member.Room)
	if err != nil {
		return err
	}
	result.Set("brief", wire.TextValue(member.Brief))
	result.Set("role", wire.TextValue(roleLabel(member.Role)))
	result.Set("roster", members)
	return nil
}

// recordReadUsage records the estimated input tokens of a page delivered to a member
// other than the human. brief is "" unless the page carries it.
func recordReadUsage(ctx context.Context, tx *sql.Tx, book *usageBook, member *participant, workspace string, bodies []string, brief string, now int64) error {
	readTokens := tokens.EstimateRead(bodies, brief)
	if readTokens <= 0 || member.Role == roleHuman {
		return nil
	}
	snapshot, err := tokens.AddEstimated(ctx, tx, member.Room, member.ID, readTokens, 0, now)
	if err != nil {
		return err
	}
	return book.sync(ctx, workspace, snapshot)
}

// read is a member read that records the usage of the page it returns.
func read(ctx context.Context, tx *sql.Tx, book *usageBook, member *participant, request *wire.Read, now int64) (wire.Value, error) {
	result, bodies, err := readPage(ctx, tx, member, request.After)
	if err != nil {
		return wire.Value{}, err
	}
	brief := ""
	if request.After == 0 {
		brief = member.Brief
	}
	if err := recordReadUsage(ctx, tx, book, member, request.Workspace, bodies, brief, now); err != nil {
		return wire.Value{}, err
	}
	return result, nil
}
