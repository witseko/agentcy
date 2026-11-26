package tokens

import (
	"context"
	"database/sql"
	"errors"
)

// SlotSnapshot is one slot's effective token totals with its bound model, ready to be
// copied into the user-level ledger. Model is nil when the slot has none.
type SlotSnapshot struct {
	Room         string
	Slot         int64
	Model        *string
	InputTokens  int64
	OutputTokens int64
	Source       string
	Updated      int64
}

// SlotUsage is one slot's model, budget override and token totals in a conversation, as
// BuildMemberUsage and SumConversationUsage read them (one value of Python's room_usage).
// EstimatedTokens is the bridge estimate and ActualTokens the exact reported or plugin
// total, nil until exact usage arrives.
type SlotUsage struct {
	Slot               int64
	Model              *string
	TokenBudget        *int64
	InputTokens        int64
	OutputTokens       int64
	ConversationTokens int64
	EstimatedTokens    int64
	ActualTokens       *int64
	Source             string
	HasUsage           bool
}

// ExactUsage is cumulative usage reported by a participant or a fresh-run plugin. A
// non-nil Model also binds the slot to that model.
type ExactUsage struct {
	InputTokens  int64
	OutputTokens int64
	Source       string
	Model        *string
}

// usageCounts are the stored counters of one participant_usage row.
type usageCounts struct {
	estimatedInput  int64
	estimatedOutput int64
	reportedInput   sql.NullInt64
	reportedOutput  sql.NullInt64
	source          string
}

// effectiveTotals prefers exact counts over estimated ones and returns the input and
// output tokens with their source.
func effectiveTotals(counts usageCounts) (int64, int64, string) {
	if counts.reportedInput.Valid && counts.reportedOutput.Valid {
		return counts.reportedInput.Int64, counts.reportedOutput.Int64, counts.source
	}
	return counts.estimatedInput, counts.estimatedOutput, SourceEstimated
}

const selectSlotSnapshot = `
SELECT participant_usage.room, participant_usage.slot, participant_usage.estimated_input,
	participant_usage.estimated_output, participant_usage.reported_input,
	participant_usage.reported_output, participant_usage.source, participant_usage.updated,
	participant.model
FROM participant_usage JOIN participant ON participant.id = participant_usage.slot
WHERE participant_usage.slot = ?`

// ReadSlotSnapshot returns one slot's effective usage and bound model, or nil when the
// slot has no usage row.
func ReadSlotSnapshot(ctx context.Context, tx *sql.Tx, slot int64) (*SlotSnapshot, error) {
	var snapshot SlotSnapshot
	var counts usageCounts
	var model sql.NullString
	err := tx.QueryRowContext(ctx, selectSlotSnapshot, slot).Scan(
		&snapshot.Room, &snapshot.Slot, &counts.estimatedInput, &counts.estimatedOutput,
		&counts.reportedInput, &counts.reportedOutput, &counts.source, &snapshot.Updated, &model)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snapshot.InputTokens, snapshot.OutputTokens, snapshot.Source = effectiveTotals(counts)
	if model.Valid {
		snapshot.Model = &model.String
	}
	return &snapshot, nil
}

const upsertEstimated = `
INSERT INTO participant_usage (slot, room, estimated_input, estimated_output, source, updated)
VALUES (?, ?, ?, ?, 'estimated', ?)
ON CONFLICT (slot) DO UPDATE SET
	estimated_input = participant_usage.estimated_input + ?,
	estimated_output = participant_usage.estimated_output + ?,
	updated = ?`

// AddEstimated adds bridge-estimated input and output tokens to a slot and returns its
// snapshot. It does nothing and returns nil when both counts are zero or less.
func AddEstimated(ctx context.Context, tx *sql.Tx, room string, slot, inputTokens, outputTokens, now int64) (*SlotSnapshot, error) {
	if inputTokens <= 0 && outputTokens <= 0 {
		return nil, nil
	}
	_, err := tx.ExecContext(ctx, upsertEstimated, slot, room, inputTokens, outputTokens, now, inputTokens, outputTokens, now)
	if err != nil {
		return nil, err
	}
	return ReadSlotSnapshot(ctx, tx, slot)
}

const upsertExact = `
INSERT INTO participant_usage
	(slot, room, estimated_input, estimated_output, reported_input, reported_output, source, updated)
VALUES (?, ?, 0, 0, ?, ?, ?, ?)
ON CONFLICT (slot) DO UPDATE SET
	reported_input = ?, reported_output = ?, source = ?, updated = ?`

// RecordExact stores exact cumulative usage for a slot, replacing the previous report,
// and returns its snapshot.
func RecordExact(ctx context.Context, tx *sql.Tx, room string, slot int64, usage ExactUsage, now int64) (*SlotSnapshot, error) {
	if usage.Model != nil {
		if err := setParticipantModel(ctx, tx, slot, *usage.Model); err != nil {
			return nil, err
		}
	}
	_, err := tx.ExecContext(ctx, upsertExact,
		slot, room, usage.InputTokens, usage.OutputTokens, usage.Source, now,
		usage.InputTokens, usage.OutputTokens, usage.Source, now)
	if err != nil {
		return nil, err
	}
	return ReadSlotSnapshot(ctx, tx, slot)
}

// SetMemberBudget updates a participant's model and per-conversation starting budget,
// each only when given, and returns the slot's snapshot.
func SetMemberBudget(ctx context.Context, tx *sql.Tx, slot int64, model *string, tokenBudget *int64) (*SlotSnapshot, error) {
	if model != nil {
		if err := setParticipantModel(ctx, tx, slot, *model); err != nil {
			return nil, err
		}
	}
	if tokenBudget != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE participant SET token_budget = ? WHERE id = ?", *tokenBudget, slot); err != nil {
			return nil, err
		}
	}
	return ReadSlotSnapshot(ctx, tx, slot)
}

// setParticipantModel binds a participant to a model.
func setParticipantModel(ctx context.Context, tx *sql.Tx, slot int64, model string) error {
	_, err := tx.ExecContext(ctx, "UPDATE participant SET model = ? WHERE id = ?", model, slot)
	return err
}

const selectRoomUsage = `
SELECT participant.id, participant.model, participant.token_budget, assignment.plugin,
	participant_usage.estimated_input, participant_usage.estimated_output,
	participant_usage.reported_input, participant_usage.reported_output,
	participant_usage.source
FROM participant
LEFT OUTER JOIN participant_usage ON participant_usage.slot = participant.id
LEFT OUTER JOIN assignment ON assignment.slot = participant.id
WHERE participant.room = ?`

// RoomUsage returns the usage of every slot of a conversation, in the order SQLite gives
// (Python's room_usage has no ORDER BY either; callers look slots up by number).
func RoomUsage(ctx context.Context, tx *sql.Tx, room string) ([]SlotUsage, error) {
	rows, err := tx.QueryContext(ctx, selectRoomUsage, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	slots := []SlotUsage{}
	for rows.Next() {
		slot, err := scanSlotUsage(rows)
		if err != nil {
			return nil, err
		}
		slots = append(slots, slot)
	}
	return slots, rows.Err()
}

// scanSlotUsage reads one row of selectRoomUsage.
func scanSlotUsage(rows *sql.Rows) (SlotUsage, error) {
	var slot SlotUsage
	var model, plugin, source sql.NullString
	var budget, estimatedInput, estimatedOutput sql.NullInt64
	var counts usageCounts
	err := rows.Scan(&slot.Slot, &model, &budget, &plugin, &estimatedInput, &estimatedOutput,
		&counts.reportedInput, &counts.reportedOutput, &source)
	if err != nil {
		return slot, err
	}
	// Python: row["model"] or row["plugin"].
	if model.Valid && model.String != "" {
		slot.Model = &model.String
	} else if plugin.Valid {
		slot.Model = &plugin.String
	}
	if budget.Valid {
		slot.TokenBudget = &budget.Int64
	}
	slot.Source = SourceEstimated
	slot.HasUsage = source.Valid
	if !slot.HasUsage {
		return slot, nil
	}
	counts.estimatedInput, counts.estimatedOutput, counts.source = estimatedInput.Int64, estimatedOutput.Int64, source.String
	slot.InputTokens, slot.OutputTokens, slot.Source = effectiveTotals(counts)
	slot.ConversationTokens = slot.InputTokens + slot.OutputTokens
	slot.EstimatedTokens = counts.estimatedInput + counts.estimatedOutput
	if slot.Source != SourceEstimated {
		actual := slot.ConversationTokens
		slot.ActualTokens = &actual
	}
	return slot, nil
}
