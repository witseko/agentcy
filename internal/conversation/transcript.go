package conversation

import (
	"context"
	"database/sql"
	"slices"

	"github.com/witseko/agentcy/internal/wire"
)

// operatorPageSize is the number of messages in one operator transcript page.
const operatorPageSize = 50

const transcriptColumns = `SELECT message.id, participant.label, message.body, message.created, message.kind, recipient.label AS "to", message.reply_to 
FROM message JOIN participant ON message.author = participant.id LEFT OUTER JOIN participant AS recipient ON message.recipient = recipient.id 
WHERE message.room = ?`

// transcriptPage selects one page of a transcript. Forward reads use after; backward
// reads use tail (the latest page) or before (an older page).
type transcriptPage struct {
	after    int64
	before   *int64
	tail     bool
	pageSize int
}

// transcriptMessage is one published message with its set fields.
type transcriptMessage struct {
	id    int64
	value wire.Value
}

// transcript returns one page of published messages in ascending ID order and sets
// messages, more, cursor and total_messages on result, in that order (transcript in
// operator.py).
func transcript(ctx context.Context, tx *sql.Tx, room string, page transcriptPage, result wire.Value) error {
	query := transcriptColumns
	arguments := []any{room}
	backward := page.tail || page.before != nil
	switch {
	case page.before != nil:
		query += " AND message.id < ? ORDER BY message.id DESC"
		arguments = append(arguments, *page.before)
	case backward:
		query += " ORDER BY message.id DESC"
	default:
		query += " AND message.id > ? ORDER BY message.id"
		arguments = append(arguments, page.after)
	}
	query += "\n LIMIT ? OFFSET ?"
	messages, err := transcriptMessages(ctx, tx, query, append(arguments, page.pageSize+1, 0)...)
	if err != nil {
		return err
	}
	more := len(messages) > page.pageSize
	messages = messages[:min(len(messages), page.pageSize)]
	if backward {
		slices.Reverse(messages)
	}
	totalMessages, err := roomMessageCount(ctx, tx, room)
	if err != nil {
		return err
	}
	cursor := page.after
	if backward {
		cursor = 0
	}
	values := make([]wire.Value, 0, len(messages))
	for _, message := range messages {
		values = append(values, message.value)
		cursor = message.id
	}
	result.Set("messages", wire.ListValue(values))
	result.Set("more", wire.BoolValue(more))
	result.Set("cursor", wire.IntValue(cursor))
	result.Set("total_messages", wire.IntValue(totalMessages))
	return nil
}

// transcriptMessages runs a transcript query and keeps the set fields of each row.
func transcriptMessages(ctx context.Context, tx *sql.Tx, query string, arguments ...any) ([]transcriptMessage, error) {
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []transcriptMessage
	for rows.Next() {
		var id, created int64
		var label, body string
		var kind, to sql.NullString
		var replyTo sql.NullInt64
		if err := rows.Scan(&id, &label, &body, &created, &kind, &to, &replyTo); err != nil {
			return nil, err
		}
		value := wire.NewObject()
		value.Set("id", wire.IntValue(id))
		value.Set("label", wire.TextValue(label))
		value.Set("body", wire.TextValue(body))
		value.Set("created", wire.IntValue(created))
		if kind.Valid {
			value.Set("kind", wire.TextValue(kind.String))
		}
		if to.Valid {
			value.Set("to", wire.TextValue(to.String))
		}
		if replyTo.Valid {
			value.Set("reply_to", wire.IntValue(replyTo.Int64))
		}
		messages = append(messages, transcriptMessage{id: id, value: value})
	}
	return messages, rows.Err()
}
