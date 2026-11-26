package conversation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/witseko/agentcy/internal/wire"
)

// This file lets any chat of the workspace read closed conversations without a
// participant token (history.py).

// listLimit is the most conversations a list returns.
const listLimit = 100

// titleSourceColumn selects the custom title, or the start of the brief for a chat that
// has none (title_source in repository.py; substr from 1 for titleLength+1 code points).
const titleSourceColumn = `coalesce(conversation.title, substr(conversation.brief, ?, ?)) AS title_source`

const selectClosedConversations = `SELECT conversation.id, ` + titleSourceColumn + `, conversation.created, conversation.archived, (SELECT count(*) AS count_1 
FROM message 
WHERE message.room = conversation.id) AS messages 
FROM conversation 
WHERE conversation.open = ?`

const listOrder = ` ORDER BY conversation.created IS NULL, conversation.created DESC, conversation.rowid DESC
 LIMIT ? OFFSET ?`

// listClosed summarizes the newest closed conversations, optionally only archived or
// only active ones. Conversations without a created time come last.
func listClosed(ctx context.Context, tx *sql.Tx, archived *bool) (wire.Value, error) {
	query := selectClosedConversations
	arguments := []any{1, titleLength + 1, 0}
	if archived != nil {
		query += " AND conversation.archived = ?"
		arguments = append(arguments, boolInteger(*archived))
	}
	rows, err := tx.QueryContext(ctx, query+listOrder, append(arguments, listLimit, 0)...)
	if err != nil {
		return wire.Value{}, err
	}
	defer rows.Close()
	conversations := []wire.Value{}
	for rows.Next() {
		var id, source string
		var created sql.NullInt64
		var isArchived bool
		var messages int64
		if err := rows.Scan(&id, &source, &created, &isArchived, &messages); err != nil {
			return wire.Value{}, err
		}
		entry := wire.NewObject()
		entry.Set("conversation", wire.TextValue(id))
		entry.Set("title", wire.TextValue(sessionTitle(source)))
		entry.Set("created", nullableInt(created))
		entry.Set("archived", wire.BoolValue(isArchived))
		entry.Set("messages", wire.IntValue(messages))
		conversations = append(conversations, entry)
	}
	if err := rows.Err(); err != nil {
		return wire.Value{}, err
	}
	return singleField("conversations", wire.ListValue(conversations)), nil
}

// boolInteger stores a boolean as SQLite's 0 or 1.
func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

// readClosed returns one page of a closed conversation, with the brief and roster from
// cursor zero.
func readClosed(ctx context.Context, tx *sql.Tx, conversation string, after int64) (wire.Value, error) {
	var brief string
	var created sql.NullInt64
	var archived bool
	err := tx.QueryRowContext(ctx, "SELECT conversation.brief, conversation.created, conversation.archived \nFROM conversation \nWHERE conversation.id = ? AND conversation.open = ?", conversation, 0).Scan(&brief, &created, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return wire.Value{}, reject(unknownClosed)
	}
	if err != nil {
		return wire.Value{}, err
	}
	result := wire.NewObject()
	result.Set("conversation", wire.TextValue(conversation))
	result.Set("closed", wire.BoolValue(true))
	result.Set("archived", wire.BoolValue(archived))
	result.Set("created", nullableInt(created))
	if after == 0 {
		members, err := roster(ctx, tx, conversation)
		if err != nil {
			return wire.Value{}, err
		}
		result.Set("brief", wire.TextValue(brief))
		result.Set("roster", members)
	}
	err = transcript(ctx, tx, conversation, transcriptPage{after: after, pageSize: pageSize}, result)
	return result, err
}
