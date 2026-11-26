package conversation

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/witseko/agentcy/internal/tokens"
	"github.com/witseko/agentcy/internal/wire"
)

// contentDedupSeconds is how long an identical post from the same author is treated as a
// retry, even with a new submission ID.
const contentDedupSeconds = 60

// submissionRequest is one message with the client-chosen ID that protects its retries,
// from a member post or an operator post.
type submissionRequest struct {
	submissionID string
	body         string
	kind         string
	to           *string
	replyTo      *int64
	dueMinutes   *int64
}

// postContent is a message resolved against its conversation, as it is stored. A note
// is stored without a kind.
type postContent struct {
	body       string
	kind       sql.NullString
	recipient  sql.NullInt64
	replyTo    sql.NullInt64
	dueMinutes sql.NullInt64
}

const selectRecipients = `SELECT participant.id, participant.label, participant.retired 
FROM participant 
WHERE participant.room = ?`

const selectReplyTarget = `SELECT message.id 
FROM message 
WHERE message.room = ? AND message.id = ?`

// resolveContent resolves the recipient (case-insensitively) and the reply target within
// the conversation (post_content in repository.py).
func resolveContent(ctx context.Context, tx *sql.Tx, room string, submission submissionRequest) (postContent, error) {
	content := postContent{body: submission.body}
	if submission.kind != "note" {
		content.kind = sql.NullString{String: submission.kind, Valid: true}
	}
	if submission.dueMinutes != nil {
		content.dueMinutes = sql.NullInt64{Int64: *submission.dueMinutes, Valid: true}
	}
	if submission.to != nil {
		recipient, err := resolveRecipient(ctx, tx, room, *submission.to)
		if err != nil {
			return content, err
		}
		content.recipient = sql.NullInt64{Int64: recipient, Valid: true}
	}
	if submission.replyTo != nil {
		found, err := firstValueSet(ctx, tx, selectReplyTarget, room, *submission.replyTo)
		if err != nil {
			return content, err
		}
		if !found {
			return content, reject("Unknown reply target in this conversation")
		}
		content.replyTo = sql.NullInt64{Int64: *submission.replyTo, Valid: true}
	}
	return content, nil
}

// resolveRecipient finds a member by name. The error lists the active members in the
// order SQLite returns them (byte order of labels, as in Python).
func resolveRecipient(ctx context.Context, tx *sql.Tx, room, name string) (int64, error) {
	rows, err := tx.QueryContext(ctx, selectRecipients, room)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	folded := foldCase(name)
	var active []string
	for rows.Next() {
		var id int64
		var label string
		var retired bool
		if err := rows.Scan(&id, &label, &retired); err != nil {
			return 0, err
		}
		if foldCase(label) == folded {
			return id, nil
		}
		if !retired {
			active = append(active, label)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return 0, rejectf("Unknown recipient '%s' in this conversation. Active members: %s", name, strings.Join(active, ", "))
}

const selectReceiptMessage = `SELECT message.id, message.body, message.kind, message.recipient, message.reply_to, message.due_minutes 
FROM message JOIN submission ON message.id = submission.message_id 
WHERE submission.author = ? AND submission."key" = ?`

// previousSubmission returns the message stored under a submission ID, or nil.
func previousSubmission(ctx context.Context, tx *sql.Tx, author int64, key string) (*postContent, int64, error) {
	var previous postContent
	var id int64
	err := tx.QueryRowContext(ctx, selectReceiptMessage, author, key).Scan(
		&id, &previous.body, &previous.kind, &previous.recipient, &previous.replyTo, &previous.dueMinutes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	return &previous, id, nil
}

// recentDuplicate finds the newest identical message of the author within the
// deduplication window. A NULL field matches only NULL, as SQLAlchemy's filter_by does.
func recentDuplicate(ctx context.Context, tx *sql.Tx, author int64, content postContent, now int64) (sql.NullInt64, error) {
	query := "SELECT message.id \nFROM message \nWHERE message.author = ? AND message.created >= ? AND message.body = ?"
	arguments := []any{author, now - contentDedupSeconds, content.body}
	query, arguments = matchNullable(query, arguments, "message.kind", content.kind)
	query, arguments = matchNullable(query, arguments, "message.recipient", content.recipient)
	query, arguments = matchNullable(query, arguments, "message.reply_to", content.replyTo)
	query, arguments = matchNullable(query, arguments, "message.due_minutes", content.dueMinutes)
	query += " ORDER BY message.id DESC\n LIMIT ? OFFSET ?"
	return firstInteger(ctx, tx, query, append(arguments, 1, 0)...)
}

// matchNullable adds "column = ?" for a value, or "column IS NULL" for NULL.
func matchNullable(query string, arguments []any, column string, value any) (string, []any) {
	switch typed := value.(type) {
	case sql.NullString:
		if !typed.Valid {
			return query + " AND " + column + " IS NULL", arguments
		}
		return query + " AND " + column + " = ?", append(arguments, typed.String)
	case sql.NullInt64:
		if !typed.Valid {
			return query + " AND " + column + " IS NULL", arguments
		}
		return query + " AND " + column + " = ?", append(arguments, typed.Int64)
	}
	return query, arguments
}

const insertFullMessage = `INSERT INTO message (room, author, body, created, kind, recipient, reply_to, due_minutes) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

// post stores a message, deduplicating retries with the same text, kind, addressee,
// reply target and due time (post in repository.py). workspace is "" for the human,
// whose usage is not recorded.
func post(ctx context.Context, tx *sql.Tx, book *usageBook, member *participant, submission submissionRequest, workspace string, now int64) (wire.Value, error) {
	content, err := resolveContent(ctx, tx, member.Room, submission)
	if err != nil {
		return wire.Value{}, err
	}
	previous, previousID, err := previousSubmission(ctx, tx, member.ID, submission.submissionID)
	if err != nil {
		return wire.Value{}, err
	}
	if previous != nil {
		if *previous != content {
			return wire.Value{}, reject(submissionIDReused)
		}
		return singleField("message_id", wire.IntValue(previousID)), nil
	}
	number, err := recentDuplicate(ctx, tx, member.ID, content, now)
	if err != nil {
		return wire.Value{}, err
	}
	var ownedBefore []taskSummary
	isDone := content.kind.Valid && content.kind.String == kindDone
	if isDone {
		byOwner, err := openTasks(ctx, tx, member.Room, now)
		if err != nil {
			return wire.Value{}, err
		}
		ownedBefore = byOwner[member.ID]
	}
	insert := !number.Valid
	if !insert {
		if insert, err = changesBlockedState(ctx, tx, member, content); err != nil {
			return wire.Value{}, err
		}
	}
	if insert {
		if number, err = insertMessage(ctx, tx, book, member, content, workspace, now); err != nil {
			return wire.Value{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, insertMessageReceipt, member.ID, submission.submissionID, number.Int64); err != nil {
		return wire.Value{}, err
	}
	result := singleField("message_id", wire.IntValue(number.Int64))
	if !isDone || len(ownedBefore) == 0 {
		return result, nil
	}
	return attachDoneWarning(ctx, tx, member, ownedBefore, result, now)
}

// insertMessage validates a task transition, stores the message and records the
// author's estimated output tokens.
func insertMessage(ctx context.Context, tx *sql.Tx, book *usageBook, member *participant, content postContent, workspace string, now int64) (sql.NullInt64, error) {
	if err := validateTransition(ctx, tx, member, content); err != nil {
		return sql.NullInt64{}, err
	}
	inserted, err := tx.ExecContext(ctx, insertFullMessage, member.Room, member.ID, content.body, now,
		content.kind, content.recipient, content.replyTo, content.dueMinutes)
	if err != nil {
		return sql.NullInt64{}, err
	}
	number, err := inserted.LastInsertId()
	if err != nil {
		return sql.NullInt64{}, err
	}
	if err := recordPostUsage(ctx, tx, book, member, content.body, workspace, now); err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: number, Valid: true}, nil
}

// recordPostUsage records the bridge-estimated output tokens of a new post by a member
// other than the human.
func recordPostUsage(ctx context.Context, tx *sql.Tx, book *usageBook, member *participant, body, workspace string, now int64) error {
	if member.Role == roleHuman {
		return nil
	}
	snapshot, err := tokens.AddEstimated(ctx, tx, member.Room, member.ID, 0, tokens.EstimatePost(body), now)
	if err != nil || workspace == "" {
		return err
	}
	return book.sync(ctx, workspace, snapshot)
}

// attachDoneWarning adds a warning when a done post left the author's open tasks
// untouched.
func attachDoneWarning(ctx context.Context, tx *sql.Tx, member *participant, ownedBefore []taskSummary, result wire.Value, now int64) (wire.Value, error) {
	byOwner, err := openTasks(ctx, tx, member.Room, now)
	if err != nil {
		return wire.Value{}, err
	}
	if warning := unclosedDoneWarning(ownedBefore, byOwner[member.ID]); warning != "" {
		result.Set("warning", wire.TextValue(warning))
	}
	return result, nil
}

const selectReceiptMessageID = `SELECT submission.message_id 
FROM submission 
WHERE submission.author = ? AND submission."key" = ?`

// postInRound routes an ordinary message or a proposal through the right submission
// store (post_in_round in repository.py). workspace is "" for the human.
func postInRound(ctx context.Context, tx *sql.Tx, book *usageBook, member *participant, submission submissionRequest, workspace string, now int64) (wire.Value, error) {
	if member.Retired {
		return wire.Value{}, reject(retiredError)
	}
	if !member.Open {
		return wire.Value{}, reject(conversationClosed)
	}
	if err := validateDueMinutes(submission.kind, submission.dueMinutes); err != nil {
		return wire.Value{}, err
	}
	if submission.kind != kindProposal {
		if member.sealed() {
			held, err := keyIsHeld(ctx, tx, member.ID, submission.submissionID)
			if err != nil {
				return wire.Value{}, err
			}
			if held {
				return wire.Value{}, reject(submissionIDReused)
			}
		}
		return post(ctx, tx, book, member, submission, workspace, now)
	}
	result, err := postProposal(ctx, tx, member, submission)
	if err != nil || !result.Has("proposal_id") {
		return result, err
	}
	complete, err := roundComplete(ctx, tx, member.Room)
	if err != nil || !complete {
		return result, err
	}
	if err := releaseRound(ctx, tx, member, now); err != nil {
		return wire.Value{}, err
	}
	number, err := firstInteger(ctx, tx, selectReceiptMessageID, member.ID, submission.submissionID)
	if err != nil {
		return wire.Value{}, err
	}
	return singleField("message_id", nullableInt(number)), nil
}
