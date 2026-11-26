package conversation

import (
	"context"
	"database/sql"
	"strings"

	"github.com/witseko/agentcy/internal/wire"
)

// roomSlot is one slot of a room with its role and retirement.
type roomSlot struct {
	id      int64
	label   string
	role    int64
	retired bool
}

const selectRoomSlots = `SELECT participant.id, participant.label, participant.role, participant.retired 
FROM participant 
WHERE participant.room = ?`

// roomSlots reads the slots of a room with query, which selects roomSlot columns.
func roomSlots(ctx context.Context, tx *sql.Tx, query, room string) ([]roomSlot, error) {
	rows, err := tx.QueryContext(ctx, query, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slots []roomSlot
	for rows.Next() {
		var slot roomSlot
		if err := rows.Scan(&slot.id, &slot.label, &slot.role, &slot.retired); err != nil {
			return nil, err
		}
		slots = append(slots, slot)
	}
	return slots, rows.Err()
}

// findSlot returns the first slot whose label matches name case-insensitively, or nil.
func findSlot(slots []roomSlot, name string) *roomSlot {
	for index := range slots {
		if sameName(slots[index].label, name) {
			return &slots[index]
		}
	}
	return nil
}

// retiredResult is the answer of a retirement.
func retiredResult(label string) wire.Value {
	result := wire.NewObject()
	result.Set("participant", wire.TextValue(label))
	result.Set("retired", wire.BoolValue(true))
	return result
}

// retire retires a participant, expiring its unused invite and stopping further writes.
// The lead cannot be retired until another participant becomes lead. The retired
// participant's unfinished tasks move to an available worker, or to the lead.
func retire(ctx context.Context, tx *sql.Tx, actor *participant, name string, now int64) (wire.Value, error) {
	if err := checkManager(actor, "Only a lead, co-lead or the human can retire participants"); err != nil {
		return wire.Value{}, err
	}
	slots, err := roomSlots(ctx, tx, selectRoomSlots, actor.Room)
	if err != nil {
		return wire.Value{}, err
	}
	target := findSlot(slots, name)
	if target == nil {
		return wire.Value{}, reject(unknownParticipant)
	}
	if target.role == roleHuman {
		return wire.Value{}, rejectf("Cannot retire %s", humanLabel)
	}
	if target.retired {
		return retiredResult(target.label), nil
	}
	if target.role == roleLead {
		return wire.Value{}, reject("Cannot retire the lead. Assign a new lead first")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE participant SET expires=?, retired=? WHERE participant.id = ?", 0, 1, target.id); err != nil {
		return wire.Value{}, err
	}
	if target.role == roleCoLead {
		if err := promoteSoleCoLead(ctx, tx, actor.Room); err != nil {
			return wire.Value{}, err
		}
	}
	if err := recordEvent(ctx, tx, actor.Room, actor.ID, "Retired "+target.label+".", now); err != nil {
		return wire.Value{}, err
	}
	if err := redelegateTasks(ctx, tx, actor, target.id, target.label, now); err != nil {
		return wire.Value{}, err
	}
	if err := releaseIfSealedComplete(ctx, tx, actor, target.id, now); err != nil {
		return wire.Value{}, err
	}
	return retiredResult(target.label), nil
}

// releaseIfSealedComplete discards a retired author's proposal and releases the round
// when the remaining workers are complete.
func releaseIfSealedComplete(ctx context.Context, tx *sql.Tx, actor *participant, target int64, now int64) error {
	if !actor.sealed() {
		return nil
	}
	if err := discardAuthorProposal(ctx, tx, actor.Room, target); err != nil {
		return err
	}
	held, err := hasHeldProposals(ctx, tx, actor.Room)
	if err != nil || !held {
		return err
	}
	complete, err := roundComplete(ctx, tx, actor.Room)
	if err != nil || !complete {
		return err
	}
	return releaseRound(ctx, tx, actor, now)
}

const selectActiveCoLeads = `SELECT participant.id 
FROM participant 
WHERE participant.room = ? AND participant.role = ? AND participant.retired = ?`

// promoteSoleCoLead makes the last active co-lead the lead, so the conversation keeps
// one leader.
func promoteSoleCoLead(ctx context.Context, tx *sql.Tx, room string) error {
	rows, err := tx.QueryContext(ctx, selectActiveCoLeads, room, roleCoLead, 0)
	if err != nil {
		return err
	}
	var coLeads []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		coLeads = append(coLeads, id)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	if len(coLeads) != 1 {
		return nil
	}
	return setSlotRole(ctx, tx, coLeads[0], roleLead)
}

// setSlotRole changes the role of one slot.
func setSlotRole(ctx context.Context, tx *sql.Tx, slot, role int64) error {
	_, err := tx.ExecContext(ctx, "UPDATE participant SET role=? WHERE participant.id = ?", role, slot)
	return err
}

// otherActiveLeaders returns the active leads and co-leads other than target.
func otherActiveLeaders(slots []roomSlot, target *roomSlot) []roomSlot {
	var leaders []roomSlot
	for _, slot := range slots {
		if slot.id != target.id && !slot.retired && isLeader(slot.role) {
			leaders = append(leaders, slot)
		}
	}
	return leaders
}

// applyLeadRole promotes target to sole lead and demotes the other active leads and
// co-leads to worker. It returns the event text, or "" when nothing changed.
func applyLeadRole(ctx context.Context, tx *sql.Tx, target *roomSlot, slots []roomSlot) (string, error) {
	previous := otherActiveLeaders(slots, target)
	if target.role == roleLead && len(previous) == 0 {
		return "", nil
	}
	if len(previous) > 0 {
		arguments := []any{roleWorker}
		for _, slot := range previous {
			arguments = append(arguments, slot.id)
		}
		query := "UPDATE participant SET role=? WHERE participant.id IN (" + placeholders(len(previous)) + ")"
		if _, err := tx.ExecContext(ctx, query, arguments...); err != nil {
			return "", err
		}
	}
	if err := setSlotRole(ctx, tx, target.id, roleLead); err != nil {
		return "", err
	}
	if len(previous) == 0 {
		return "Set " + target.label + " as lead.", nil
	}
	demoted := make([]string, 0, len(previous))
	for _, slot := range previous {
		demoted = append(demoted, slot.label)
	}
	return "Changed lead to " + target.label + " (demoted " + strings.Join(demoted, ", ") + " to worker).", nil
}

// applyCoLeadRole promotes target to co-lead and shares leadership with any active sole
// lead.
func applyCoLeadRole(ctx context.Context, tx *sql.Tx, room string, target *roomSlot) (string, error) {
	if target.role == roleCoLead {
		return "", nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE participant SET role=? WHERE participant.room = ? AND participant.role = ? AND participant.retired = ?", roleCoLead, room, roleLead, 0); err != nil {
		return "", err
	}
	if err := setSlotRole(ctx, tx, target.id, roleCoLead); err != nil {
		return "", err
	}
	return "Set " + target.label + " as co-lead.", nil
}

// applyWorkerRole demotes target to worker while keeping at least one active lead or
// co-lead.
func applyWorkerRole(ctx context.Context, tx *sql.Tx, target *roomSlot, slots []roomSlot) (string, error) {
	if target.role == roleWorker {
		return "", nil
	}
	remaining := otherActiveLeaders(slots, target)
	if len(remaining) == 0 {
		return "", reject("Cannot demote the only lead. Promote another participant first")
	}
	if err := setSlotRole(ctx, tx, target.id, roleWorker); err != nil {
		return "", err
	}
	if len(remaining) == 1 && remaining[0].role == roleCoLead {
		if err := setSlotRole(ctx, tx, remaining[0].id, roleLead); err != nil {
			return "", err
		}
	}
	return "Changed " + target.label + " to worker.", nil
}

const selectRoomSlotsByID = selectRoomSlots + " ORDER BY participant.id"

// setRole changes an active participant's role within an open conversation.
func setRole(ctx context.Context, tx *sql.Tx, actor *participant, name, requestedRole string, now int64) (wire.Value, error) {
	if err := checkManager(actor, "Only a lead, co-lead or the human can change participant roles"); err != nil {
		return wire.Value{}, err
	}
	slots, err := roomSlots(ctx, tx, selectRoomSlotsByID, actor.Room)
	if err != nil {
		return wire.Value{}, err
	}
	target := findSlot(slots, name)
	if target == nil {
		return wire.Value{}, reject(unknownParticipant)
	}
	if target.role == roleHuman {
		return wire.Value{}, rejectf("Cannot change the role of %s", humanLabel)
	}
	if target.retired {
		return wire.Value{}, rejectf("Participant is retired: %s", target.label)
	}
	newRole := assignableRoles[requestedRole]
	var event string
	switch newRole {
	case roleLead:
		event, err = applyLeadRole(ctx, tx, target, slots)
	case roleCoLead:
		event, err = applyCoLeadRole(ctx, tx, actor.Room, target)
	default:
		event, err = applyWorkerRole(ctx, tx, target, slots)
	}
	if err != nil {
		return wire.Value{}, err
	}
	if event != "" {
		if err := recordEvent(ctx, tx, actor.Room, actor.ID, event, now); err != nil {
			return wire.Value{}, err
		}
	}
	members, err := roster(ctx, tx, actor.Room)
	if err != nil {
		return wire.Value{}, err
	}
	result := wire.NewObject()
	result.Set("participant", wire.TextValue(target.label))
	result.Set("role", wire.TextValue(roleLabel(newRole)))
	result.Set("roster", members)
	return result, nil
}
