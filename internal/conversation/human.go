package conversation

import (
	"context"
	"database/sql"
	"errors"
)

// humanRoom is a conversation loaded with its human, in the shape that authenticate
// returns, plus the fields that operator views show.
type humanRoom struct {
	participant
	created  sql.NullInt64
	archived bool
	title    sql.NullString
}

const selectHumanRoom = `SELECT conversation.id AS room, conversation.brief, conversation.open, conversation.round, conversation.round_deadline, conversation.created, conversation.archived, conversation.open_invite_hash, conversation.title, participant.id 
FROM conversation LEFT OUTER JOIN participant ON participant.room = conversation.id AND participant.role = ? 
WHERE conversation.id = ?`

// humanActor loads a conversation with its human. The human's ID is zero until its
// first write to a conversation that a chat created (human_actor in operator.py).
func humanActor(ctx context.Context, tx *sql.Tx, conversation string) (*humanRoom, error) {
	human := &humanRoom{}
	var id sql.NullInt64
	err := tx.QueryRowContext(ctx, selectHumanRoom, roleHuman, conversation).Scan(
		&human.Room, &human.Brief, &human.Open, &human.Round, &human.RoundDeadline,
		&human.created, &human.archived, &human.OpenInviteHash, &human.title, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, reject(unknownConversation)
	}
	if err != nil {
		return nil, err
	}
	human.ID = id.Int64
	human.Label = humanLabel
	human.Role = roleHuman
	return human, nil
}

const selectRoomLabels = `SELECT participant.label 
FROM participant 
WHERE participant.room = ?`

// addHuman adds the human participant, which has no token and no invitation, and
// returns its ID.
func addHuman(ctx context.Context, tx *sql.Tx, room string) (int64, error) {
	labels, err := collectStrings(ctx, tx, selectRoomLabels, room)
	if err != nil {
		return 0, err
	}
	for _, label := range labels {
		if sameName(label, humanLabel) {
			return 0, rejectf("A member of this conversation already uses the name %s", humanLabel)
		}
	}
	inserted, err := tx.ExecContext(ctx, "INSERT INTO participant (room, label, role) VALUES (?, ?, ?)", room, humanLabel, roleHuman)
	if err != nil {
		return 0, err
	}
	return inserted.LastInsertId()
}

// writingActor returns the human, adding it on its first write to a conversation that a
// chat created.
func writingActor(ctx context.Context, tx *sql.Tx, conversation string) (*humanRoom, error) {
	human, err := humanActor(ctx, tx, conversation)
	if err != nil {
		return nil, err
	}
	if human.ID == 0 {
		if human.ID, err = addHuman(ctx, tx, conversation); err != nil {
			return nil, err
		}
	}
	return human, nil
}
