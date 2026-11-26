package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/core"
)

// readerFilter is the wake rule of one waiting read (port plan fix L3). It answers from
// an added message alone the question that reading the page again would answer: does
// the message make the page ready? It mirrors the SQL of a read: the message must be
// unread for the reader (unreadFilter) and wake it (wakeColumn). A close, a retirement
// or a released round are structural writes, which wake every reader without asking.
type readerFilter struct {
	member int64
	leader bool
	after  int64
}

// newReaderFilter returns the filter of a member reading after a cursor.
func newReaderFilter(member *participant, after int64) readerFilter {
	return readerFilter{member: member.ID, leader: isLeader(member.Role), after: after}
}

// Wakes reports whether message is unread for the reader and wakes it.
func (filter readerFilter) Wakes(message core.Message) bool {
	return filter.unread(message) && filter.wakes(message)
}

// unread mirrors unreadFilter: from cursor zero everything, after it only other
// members' messages plus events and released proposals.
func (filter readerFilter) unread(message core.Message) bool {
	if message.ID <= filter.after {
		return false
	}
	return filter.after == 0 || message.Author != filter.member ||
		message.Kind == eventKind || message.Kind == kindProposal
}

// wakes mirrors wakeColumn: a lead or co-lead wakes for every message, anyone else for a
// message addressed to them or an unaddressed message that is neither a blocker nor an
// event.
func (filter readerFilter) wakes(message core.Message) bool {
	if filter.leader {
		return true
	}
	if message.HasRecipient {
		return message.Recipient == filter.member
	}
	return message.Kind != kindBlocker && message.Kind != eventKind
}

const selectAddedMessages = `SELECT message.id, message.author, message.recipient, message.kind 
FROM message 
WHERE message.room = ? AND message.id > ? ORDER BY message.id`

// addedMessages returns the messages of a room after message ID after, which inside a
// write transaction are the messages that the transaction added.
func addedMessages(ctx context.Context, tx *sql.Tx, room string, after int64) ([]core.Message, error) {
	rows, err := tx.QueryContext(ctx, selectAddedMessages, room, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []core.Message
	for rows.Next() {
		var message core.Message
		var recipient sql.NullInt64
		var kind sql.NullString
		if err := rows.Scan(&message.ID, &message.Author, &recipient, &kind); err != nil {
			return nil, err
		}
		message.Recipient, message.HasRecipient = recipient.Int64, recipient.Valid
		message.Kind = kind.String
		messages = append(messages, message)
	}
	return messages, rows.Err()
}
