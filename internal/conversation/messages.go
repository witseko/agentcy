package conversation

import (
	"database/sql"

	"github.com/witseko/agentcy/internal/wire"
)

// pageSize is the number of messages in one member read page (PAGE_SIZE).
const pageSize = 16

// messageRow is one message as a member sees it. Wake and TaskBlocker are only read by
// the read query.
type messageRow struct {
	ID          int64
	Label       string
	Body        string
	Kind        sql.NullString
	To          sql.NullString
	ReplyTo     sql.NullInt64
	DueMinutes  sql.NullInt64
	AuthorRole  int64
	Wake        bool
	TaskBlocker bool
}

// fields builds one message for an agent: the set fields only, plus role "human" for the
// user's messages (message_fields in repository.py).
func (row messageRow) fields() wire.Value {
	message := wire.NewObject()
	message.Set("id", wire.IntValue(row.ID))
	message.Set("label", wire.TextValue(row.Label))
	message.Set("body", wire.TextValue(row.Body))
	if row.Kind.Valid {
		message.Set("kind", wire.TextValue(row.Kind.String))
	}
	if row.To.Valid {
		message.Set("to", wire.TextValue(row.To.String))
	}
	if row.ReplyTo.Valid {
		message.Set("reply_to", wire.IntValue(row.ReplyTo.Int64))
	}
	if row.DueMinutes.Valid {
		message.Set("due_minutes", wire.IntValue(row.DueMinutes.Int64))
	}
	if row.AuthorRole == roleHuman {
		message.Set("role", wire.TextValue(roleLabel(roleHuman)))
	}
	return message
}

// messagePage turns at most pageSize rows into message objects and their bodies.
func messagePage(rows []messageRow) ([]wire.Value, []string) {
	shown := rows[:min(len(rows), pageSize)]
	messages := make([]wire.Value, 0, len(shown))
	bodies := make([]string, 0, len(shown))
	for _, row := range shown {
		messages = append(messages, row.fields())
		bodies = append(bodies, row.Body)
	}
	return messages, bodies
}
