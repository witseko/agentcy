package conversation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/witseko/agentcy/internal/wire"
)

// activityUpdateSeconds is how often an authenticated request saves the member's last
// activity at most.
const activityUpdateSeconds = 10

// participant is an authenticated member with the current state of its conversation,
// the row that authenticate returns in Python. For the human of an operator request it
// is built by humanActor instead; then ID is zero until the human's first write adds
// its row.
type participant struct {
	ID             int64
	Room           string
	Label          string
	Role           int64
	InviteHash     sql.NullString
	Expires        sql.NullInt64
	TokenHash      sql.NullString
	Seen           sql.NullInt64
	Retired        bool
	Model          sql.NullString
	TokenBudget    sql.NullInt64
	Brief          string
	Open           bool
	Round          sql.NullString
	RoundDeadline  sql.NullInt64
	OpenInviteHash sql.NullString
}

// sealed reports a conversation whose proposals are still held.
func (member *participant) sealed() bool {
	return member.Round.Valid && member.Round.String == roundSealed
}

// roundFields describes a structured round, leaving ordinary conversations' results
// unchanged (rounds.fields in Python). It sets the keys on result in Python's order.
func roundFields(result wire.Value, round sql.NullString, deadline sql.NullInt64) {
	if !round.Valid || round.String == "" {
		return
	}
	result.Set("round", wire.TextValue(round.String))
	if round.String == roundSealed {
		result.Set("round_deadline", nullableInt(deadline))
	}
}

const selectParticipantByToken = `SELECT participant.id, participant.room, participant.label, participant.role, participant.invite_hash, participant.expires, participant.token_hash, participant.seen, participant.retired, participant.model, participant.token_budget, conversation.brief, conversation.open, conversation.round, conversation.round_deadline, conversation.open_invite_hash 
FROM participant JOIN conversation ON conversation.id = participant.room 
WHERE participant.token_hash = ?`

// authenticate resolves membership and the current state of the conversation from a
// participant token.
func authenticate(ctx context.Context, tx *sql.Tx, token string) (*participant, error) {
	member := &participant{}
	err := tx.QueryRowContext(ctx, selectParticipantByToken, digest(token)).Scan(
		&member.ID, &member.Room, &member.Label, &member.Role, &member.InviteHash,
		&member.Expires, &member.TokenHash, &member.Seen, &member.Retired, &member.Model,
		&member.TokenBudget, &member.Brief, &member.Open, &member.Round,
		&member.RoundDeadline, &member.OpenInviteHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, reject(invalidToken)
	}
	if err != nil {
		return nil, err
	}
	return member, nil
}

// recordActivity saves the last authenticated activity at most once per interval.
func recordActivity(ctx context.Context, tx *sql.Tx, member *participant, now int64) error {
	if member.Seen.Valid && now-member.Seen.Int64 <= activityUpdateSeconds {
		return nil
	}
	_, err := tx.ExecContext(ctx, "UPDATE participant SET seen=? WHERE participant.id = ?", now, member.ID)
	return err
}

// refreshParticipant authenticates a member request, records its activity, and
// publishes a sealed round whose deadline passed before a read, post or status observes
// it (refresh_participant in Python).
func refreshParticipant(ctx context.Context, tx *sql.Tx, token string, releasesRound bool, now int64) (*participant, error) {
	member, err := authenticate(ctx, tx, token)
	if err != nil {
		return nil, err
	}
	if !member.Open || member.Retired {
		return member, nil
	}
	if err := recordActivity(ctx, tx, member, now); err != nil {
		return nil, err
	}
	if !releasesRound || !member.sealed() || now < member.RoundDeadline.Int64 {
		return member, nil
	}
	if err := releaseRound(ctx, tx, member, now); err != nil {
		return nil, err
	}
	return authenticate(ctx, tx, token)
}

// nullableInt returns an integer value or JSON null.
func nullableInt(number sql.NullInt64) wire.Value {
	if !number.Valid {
		return wire.NullValue()
	}
	return wire.IntValue(number.Int64)
}

// nullableText returns a string value or JSON null.
func nullableText(text sql.NullString) wire.Value {
	if !text.Valid {
		return wire.NullValue()
	}
	return wire.TextValue(text.String)
}
