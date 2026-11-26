package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/wire"
)

const expireUnjoinedInvites = `UPDATE participant SET expires=? WHERE participant.room = ? AND participant.token_hash IS NULL AND participant.expires IS NOT NULL`

// expireUnjoinedInvitesOf expires the unused single-use join codes of a room while
// keeping their hashes for assignment joins.
func expireUnjoinedInvitesOf(ctx context.Context, tx *sql.Tx, room string) error {
	_, err := tx.ExecContext(ctx, expireUnjoinedInvites, 0, room)
	return err
}

// closeConversation records the final message of a lead, co-lead or the human and
// closes the conversation once (close in repository.py). summary may be nil.
func closeConversation(ctx context.Context, tx *sql.Tx, member *participant, summary *string, now int64) (wire.Value, error) {
	if member.Retired {
		return wire.Value{}, reject(retiredError)
	}
	if !canManage(member.Role) {
		return wire.Value{}, reject("Only a lead or co-lead can close the conversation")
	}
	closed := singleField("closed", wire.BoolValue(true))
	if !member.Open {
		return closed, nil
	}
	if member.sealed() {
		if err := discardProposals(ctx, tx, member.Room); err != nil {
			return wire.Value{}, err
		}
	}
	if summary != nil {
		if err := insertNote(ctx, tx, member.Room, member.ID, *summary, now); err != nil {
			return wire.Value{}, err
		}
	}
	if err := expireUnjoinedInvitesOf(ctx, tx, member.Room); err != nil {
		return wire.Value{}, err
	}
	_, err := tx.ExecContext(ctx, "UPDATE conversation SET open=?, open_invite_hash=? WHERE conversation.id = ?", 0, nil, member.Room)
	return closed, err
}

// reopenConversation reopens a closed conversation as a lead, co-lead or the human and
// optionally posts a note.
func reopenConversation(ctx context.Context, tx *sql.Tx, member *participant, note *string, now int64) (wire.Value, error) {
	if member.Retired {
		return wire.Value{}, reject(retiredError)
	}
	if !canManage(member.Role) {
		return wire.Value{}, reject("Only a lead or co-lead can reopen the conversation")
	}
	reopened := singleField("reopened", wire.BoolValue(true))
	if member.Open {
		return reopened, nil
	}
	if note != nil {
		if err := insertNote(ctx, tx, member.Room, member.ID, *note, now); err != nil {
			return wire.Value{}, err
		}
	}
	// Keep previously closed unused codes expired even for rooms closed before this
	// safeguard.
	if err := expireUnjoinedInvitesOf(ctx, tx, member.Room); err != nil {
		return wire.Value{}, err
	}
	_, err := tx.ExecContext(ctx, "UPDATE conversation SET open=?, archived=? WHERE conversation.id = ?", 1, 0, member.Room)
	return reopened, err
}
