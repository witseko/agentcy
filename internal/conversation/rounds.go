package conversation

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/witseko/agentcy/internal/wire"
)

// The sealed round states and the proposal kind (rounds.py).
const (
	roundSealed   = "sealed"
	roundCritique = "critique"
	kindProposal  = "proposal"
)

const selectHeldProposalOfAuthor = `SELECT proposal.id 
FROM proposal 
WHERE proposal.room = ? AND proposal.author = ?`

// proposalSubmitted checks only the participant's own held proposal.
func proposalSubmitted(ctx context.Context, tx *sql.Tx, member *participant) (bool, error) {
	return firstValueSet(ctx, tx, selectHeldProposalOfAuthor, member.Room, member.ID)
}

const selectProposalReceipt = `SELECT submission.proposal_id, submission.message_id, proposal.body, message.body AS released_body, message.kind 
FROM submission LEFT OUTER JOIN proposal ON submission.proposal_id = proposal.id LEFT OUTER JOIN message ON submission.message_id = message.id 
WHERE submission.author = ? AND submission."key" = ?`

// proposalReceipt resolves a submission ID before or after its proposal was published.
// It returns found false when the ID is new.
func proposalReceipt(ctx context.Context, tx *sql.Tx, author int64, submissionID, body string) (wire.Value, bool, error) {
	var proposalID, proposalBody, releasedBody, kind sql.NullString
	var messageID sql.NullInt64
	err := tx.QueryRowContext(ctx, selectProposalReceipt, author, submissionID).Scan(
		&proposalID, &messageID, &proposalBody, &releasedBody, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return wire.Value{}, false, nil
	}
	if err != nil {
		return wire.Value{}, false, err
	}
	if proposalID.Valid && proposalBody.Valid && proposalBody.String == body {
		return singleField("proposal_id", wire.TextValue(proposalID.String)), true, nil
	}
	if kind.Valid && kind.String == kindProposal && releasedBody.Valid && releasedBody.String == body {
		return singleField("message_id", nullableInt(messageID)), true, nil
	}
	return wire.Value{}, false, reject(submissionIDReused)
}

// singleField returns an object with one member.
func singleField(key string, value wire.Value) wire.Value {
	result := wire.NewObject()
	result.Set(key, value)
	return result
}

const insertProposal = `INSERT INTO proposal (id, room, author, body) VALUES (?, ?, ?, ?)`

const selectHeldProposal = `SELECT proposal.id, proposal.body 
FROM proposal 
WHERE proposal.room = ? AND proposal.author = ?`

const insertProposalReceipt = `INSERT INTO submission (author, "key", proposal_id) VALUES (?, ?, ?)`

// postProposal accepts one proposal from a worker, keeping retries safe across release
// (rounds.post in Python).
func postProposal(ctx context.Context, tx *sql.Tx, member *participant, submission submissionRequest) (wire.Value, error) {
	if member.Role != roleWorker {
		return wire.Value{}, reject("Only workers can post sealed proposals")
	}
	if submission.to != nil || submission.replyTo != nil {
		return wire.Value{}, reject("A sealed proposal cannot have an addressee or reply target")
	}
	if !member.Round.Valid {
		return wire.Value{}, reject(noSealedRound)
	}
	previous, found, err := proposalReceipt(ctx, tx, member.ID, submission.submissionID, submission.body)
	if err != nil || found {
		return previous, err
	}
	if member.Round.String == roundCritique {
		return replayReleased(ctx, tx, member, submission)
	}
	var heldID, heldBody string
	err = tx.QueryRowContext(ctx, selectHeldProposal, member.Room, member.ID).Scan(&heldID, &heldBody)
	proposalID := heldID
	switch {
	case errors.Is(err, sql.ErrNoRows):
		proposalID = tokenHex(16)
		if _, err := tx.ExecContext(ctx, insertProposal, proposalID, member.Room, member.ID, submission.body); err != nil {
			return wire.Value{}, err
		}
	case err != nil:
		return wire.Value{}, err
	case heldBody != submission.body:
		return wire.Value{}, reject("You already submitted a proposal for this round")
	}
	if _, err := tx.ExecContext(ctx, insertProposalReceipt, member.ID, submission.submissionID, proposalID); err != nil {
		return wire.Value{}, err
	}
	return singleField("proposal_id", wire.TextValue(proposalID)), nil
}

const selectReleasedProposal = `SELECT message.id, message.body 
FROM message 
WHERE message.room = ? AND message.author = ? AND message.kind = ?`

const insertMessageReceipt = `INSERT INTO submission (author, "key", message_id) VALUES (?, ?, ?)`

// replayReleased recognizes a released proposal by its content even when a retry uses a
// new submission ID.
func replayReleased(ctx context.Context, tx *sql.Tx, member *participant, submission submissionRequest) (wire.Value, error) {
	var messageID int64
	var body string
	err := tx.QueryRowContext(ctx, selectReleasedProposal, member.Room, member.ID, kindProposal).Scan(&messageID, &body)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && body != submission.body) {
		result := wire.NewObject()
		result.Set("round", wire.TextValue(roundCritique))
		result.Set("accepted", wire.BoolValue(false))
		return result, nil
	}
	if err != nil {
		return wire.Value{}, err
	}
	if _, err := tx.ExecContext(ctx, insertMessageReceipt, member.ID, submission.submissionID, messageID); err != nil {
		return wire.Value{}, err
	}
	return singleField("message_id", wire.IntValue(messageID)), nil
}

const selectHeldKey = `SELECT submission.proposal_id 
FROM submission 
WHERE submission.author = ? AND submission."key" = ?`

// keyIsHeld keeps one submission ID namespace for held proposals and ordinary messages.
func keyIsHeld(ctx context.Context, tx *sql.Tx, author int64, key string) (bool, error) {
	return firstValueSet(ctx, tx, selectHeldKey, author, key)
}

const selectAnyHeldProposal = `SELECT proposal.id 
FROM proposal 
WHERE proposal.room = ?
 LIMIT ? OFFSET ?`

// hasHeldProposals checks whether at least one unpublished proposal is held in a room.
func hasHeldProposals(ctx context.Context, tx *sql.Tx, room string) (bool, error) {
	return firstValueSet(ctx, tx, selectAnyHeldProposal, room, 1, 0)
}

const selectWorkerWithoutProposal = `SELECT participant.id 
FROM participant 
WHERE participant.room = ? AND participant.role = ? AND participant.retired = ? AND participant.token_hash IS NOT NULL AND NOT (EXISTS (SELECT proposal.id 
FROM proposal 
WHERE proposal.room = ? AND proposal.author = participant.id))
 LIMIT ? OFFSET ?`

// roundComplete checks whether every active joined worker of a room has a proposal.
func roundComplete(ctx context.Context, tx *sql.Tx, room string) (bool, error) {
	pending, err := firstValueSet(ctx, tx, selectWorkerWithoutProposal, room, roleWorker, 0, room, 1, 0)
	return !pending, err
}

const selectRoomProposals = `SELECT proposal.id, proposal.room, proposal.author, proposal.body 
FROM proposal 
WHERE proposal.room = ? ORDER BY proposal.author`

// heldProposal is one unpublished proposal.
type heldProposal struct {
	id     string
	author int64
	body   string
}

// roomProposals reads the held proposals of a room in author order.
func roomProposals(ctx context.Context, tx *sql.Tx, room string) ([]heldProposal, error) {
	rows, err := tx.QueryContext(ctx, selectRoomProposals, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var proposals []heldProposal
	for rows.Next() {
		var proposal heldProposal
		var proposalRoom string
		if err := rows.Scan(&proposal.id, &proposalRoom, &proposal.author, &proposal.body); err != nil {
			return nil, err
		}
		proposals = append(proposals, proposal)
	}
	return proposals, rows.Err()
}

const insertKindMessage = `INSERT INTO message (room, author, body, created, kind) VALUES (?, ?, ?, ?, ?)`

const repointReceipt = `UPDATE submission SET message_id=?, proposal_id=? WHERE submission.proposal_id = ?`

const deleteRoomProposals = `DELETE FROM proposal WHERE proposal.room = ?`

const updateRound = `UPDATE conversation SET round=?, round_deadline=? WHERE conversation.id = ?`

// publishProposals publishes the held proposals with new transcript IDs, repoints their
// receipts, moves the room to critique and returns the release event text
// (rounds.release in Python).
func publishProposals(ctx context.Context, tx *sql.Tx, room string, now int64) (string, error) {
	held, err := roomProposals(ctx, tx, room)
	if err != nil {
		return "", err
	}
	authors := make([]int64, 0, len(held))
	for _, proposal := range held {
		inserted, err := tx.ExecContext(ctx, insertKindMessage, room, proposal.author, proposal.body, now, kindProposal)
		if err != nil {
			return "", err
		}
		number, err := inserted.LastInsertId()
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, repointReceipt, number, nil, proposal.id); err != nil {
			return "", err
		}
		authors = append(authors, proposal.author)
	}
	if _, err := tx.ExecContext(ctx, deleteRoomProposals, room); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, updateRound, roundCritique, nil, room); err != nil {
		return "", err
	}
	missing, err := missingWorkers(ctx, tx, room, authors)
	if err != nil {
		return "", err
	}
	if len(missing) > 0 {
		return "Proposals released for critique. Missing proposals from: " + strings.Join(missing, ", ") + ".", nil
	}
	return "Proposals released for critique. All workers submitted.", nil
}

const selectActiveWorkerLabels = `SELECT participant.label 
FROM participant 
WHERE participant.room = ? AND participant.role = ? AND participant.retired = ?`

// missingWorkers names the active worker slots that did not contribute, unused
// invitations included.
func missingWorkers(ctx context.Context, tx *sql.Tx, room string, authors []int64) ([]string, error) {
	query := selectActiveWorkerLabels
	arguments := []any{room, roleWorker, 0}
	if len(authors) > 0 {
		query += " AND (participant.id NOT IN (" + placeholders(len(authors)) + "))"
		for _, author := range authors {
			arguments = append(arguments, author)
		}
	}
	return collectStrings(ctx, tx, query+" ORDER BY participant.id", arguments...)
}

// placeholders returns count comma-separated question marks for an IN list.
func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

const deleteAuthorReceipts = `DELETE FROM submission WHERE submission.proposal_id IN (SELECT proposal.id 
FROM proposal 
WHERE proposal.room = ? AND proposal.author = ?)`

const deleteAuthorProposal = `DELETE FROM proposal WHERE proposal.room = ? AND proposal.author = ?`

// discardAuthorProposal removes an unpublished proposal of a retired worker.
func discardAuthorProposal(ctx context.Context, tx *sql.Tx, room string, author int64) error {
	if _, err := tx.ExecContext(ctx, deleteAuthorReceipts, room, author); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, deleteAuthorProposal, room, author)
	return err
}

const deleteRoomReceipts = `DELETE FROM submission WHERE submission.proposal_id IN (SELECT proposal.id 
FROM proposal 
WHERE proposal.room = ?)`

// discardProposals removes the unpublished proposals and the round when a conversation
// closes early.
func discardProposals(ctx context.Context, tx *sql.Tx, room string) error {
	if _, err := tx.ExecContext(ctx, deleteRoomReceipts, room); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, deleteRoomProposals, room); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, updateRound, nil, nil, room)
	return err
}

// releaseRound publishes a room's proposals and records one release event authored by
// actor.
func releaseRound(ctx context.Context, tx *sql.Tx, actor *participant, now int64) error {
	event, err := publishProposals(ctx, tx, actor.Room, now)
	if err != nil {
		return err
	}
	return recordEvent(ctx, tx, actor.Room, actor.ID, event, now)
}

// release lets a lead publish the proposals early and retry release safely.
func release(ctx context.Context, tx *sql.Tx, member *participant, now int64) (wire.Value, error) {
	if member.Retired {
		return wire.Value{}, reject(retiredError)
	}
	if !isLeader(member.Role) {
		return wire.Value{}, reject("Only a lead or co-lead can release proposals")
	}
	if !member.Open {
		return wire.Value{}, reject(conversationClosed)
	}
	if !member.Round.Valid {
		return wire.Value{}, reject(noSealedRound)
	}
	if member.sealed() {
		if err := releaseRound(ctx, tx, member, now); err != nil {
			return wire.Value{}, err
		}
	}
	return singleField("round", wire.TextValue(roundCritique)), nil
}
