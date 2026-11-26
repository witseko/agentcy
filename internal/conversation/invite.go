package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/wire"
)

// inviteRequest is an invitation by a lead, a co-lead or the human. role is nil to keep
// a reissued slot's role.
type inviteRequest struct {
	workspace   string
	name        string
	role        *string
	model       *string
	tokenBudget *int64
	reinvite    bool
}

// existingSlot is a non-human slot of a room as invite sees it.
type existingSlot struct {
	id      int64
	label   string
	role    int64
	retired bool
	joined  bool
}

const selectInviteSlots = `SELECT participant.id, participant.label, participant.role, participant.retired, participant.token_hash IS NOT NULL AS joined 
FROM participant 
WHERE participant.room = ? AND participant.role != ?`

// inviteSlots reads the non-human slots of a room.
func inviteSlots(ctx context.Context, tx *sql.Tx, room string) ([]existingSlot, error) {
	rows, err := tx.QueryContext(ctx, selectInviteSlots, room, roleHuman)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slots []existingSlot
	for rows.Next() {
		var slot existingSlot
		if err := rows.Scan(&slot.id, &slot.label, &slot.role, &slot.retired, &slot.joined); err != nil {
			return nil, err
		}
		slots = append(slots, slot)
	}
	return slots, rows.Err()
}

// checkManager applies the checks that every lead action starts with: not retired, a
// lead, co-lead or the human, and an open conversation.
func checkManager(member *participant, roleError string) error {
	if member.Retired {
		return reject(retiredError)
	}
	if !canManage(member.Role) {
		return reject(roleError)
	}
	if !member.Open {
		return reject(conversationClosed)
	}
	return nil
}

// invite reserves a slot or replaces an unused code within the actor's conversation.
// With reinvite, a joined or retired slot gets a new code: the slot becomes active again
// and its previous chat loses its token (invite in repository.py).
func invite(ctx context.Context, tx *sql.Tx, actor *participant, request inviteRequest, now int64) (wire.Value, error) {
	if err := checkManager(actor, "Only a lead or co-lead can invite participants"); err != nil {
		return wire.Value{}, err
	}
	if err := checkName(request.name); err != nil {
		return wire.Value{}, err
	}
	slots, err := inviteSlots(ctx, tx, actor.Room)
	if err != nil {
		return wire.Value{}, err
	}
	var slot *existingSlot
	for index := range slots {
		if sameName(slots[index].label, request.name) {
			slot = &slots[index]
			break
		}
	}
	if slot == nil && request.reinvite {
		return wire.Value{}, reject(unknownParticipant)
	}
	if slot == nil && len(slots) >= maxParticipants {
		return wire.Value{}, rejectf(participantCapTemplate, maxParticipants)
	}
	if slot != nil {
		if err := checkReissue(ctx, tx, actor, *slot, request.reinvite); err != nil {
			return wire.Value{}, err
		}
	}
	label := request.name
	role := int64(roleWorker)
	if request.role != nil && *request.role == "co-lead" {
		role = roleCoLead
	}
	if slot != nil {
		label = slot.label
		if request.role == nil {
			role = slot.role
		}
	}
	code, event, err := storeInvitation(ctx, tx, actor, slot, teamSlot{label: label, role: role, model: request.model, tokenBudget: request.tokenBudget}, request, now)
	if err != nil {
		return wire.Value{}, err
	}
	if role == roleCoLead {
		if _, err := tx.ExecContext(ctx, "UPDATE participant SET role=? WHERE participant.room = ? AND participant.role = ?", roleCoLead, actor.Room, roleLead); err != nil {
			return wire.Value{}, err
		}
	}
	if err := recordEvent(ctx, tx, actor.Room, actor.ID, event, now); err != nil {
		return wire.Value{}, err
	}
	result := wire.NewObject()
	result.Set("participant", wire.TextValue(label))
	result.Set("role", wire.TextValue(roleLabel(role)))
	result.Set("code", wire.TextValue(code))
	result.Set("join_prompt", wire.TextValue(formatJoinPrompt(actor.Brief, joinPromptOptions{
		participant: label, role: roleLabel(role), template: roleJoinTemplate(roleLabel(role)),
	})))
	return result, nil
}

// storeInvitation inserts a new slot, or gives an existing one a new code, and returns
// the code and the event text.
func storeInvitation(ctx context.Context, tx *sql.Tx, actor *participant, slot *existingSlot, wanted teamSlot, request inviteRequest, now int64) (string, string, error) {
	if slot == nil {
		code, err := insertInvitation(ctx, tx, actor.Room, request.workspace, wanted, now)
		return code, "Invited " + wanted.label + " as " + roleLabel(wanted.role) + ".", err
	}
	code := newJoinCode(request.workspace, now)
	query := "UPDATE participant SET role=?, invite_hash=?, expires=?"
	arguments := []any{wanted.role, code.hash, code.expires}
	if wanted.model != nil {
		query += ", model=?"
		arguments = append(arguments, *wanted.model)
	}
	if wanted.tokenBudget != nil {
		query += ", token_budget=?"
		arguments = append(arguments, *wanted.tokenBudget)
	}
	if request.reinvite {
		query += ", token_hash=?, seen=?, retired=?"
		arguments = append(arguments, nil, nil, 0)
	}
	query += " WHERE participant.id = ?"
	if _, err := tx.ExecContext(ctx, query, append(arguments, slot.id)...); err != nil {
		return "", "", err
	}
	event := "Reissued the invitation for " + wanted.label + " as " + roleLabel(wanted.role) + "."
	if request.reinvite {
		event = "Reinvited " + wanted.label + " as " + roleLabel(wanted.role) + "."
	}
	return code.code, event, nil
}

// checkReissue allows a new code for an existing slot only when it cannot cut off a chat
// by accident.
func checkReissue(ctx context.Context, tx *sql.Tx, actor *participant, slot existingSlot, reinvite bool) error {
	if reinvite && slot.id == actor.ID {
		return reject("You cannot reinvite yourself")
	}
	if slot.retired && !reinvite {
		return rejectf("Participant is retired: %s. Pass reinvite to bring them back", slot.label)
	}
	if slot.joined && !reinvite {
		return rejectf("Invitation slot already taken: %s. Pass reinvite to replace its chat", slot.label)
	}
	reserved, err := activeAssignment(ctx, tx, slot.id)
	if err != nil {
		return err
	}
	if reserved {
		return rejectf("Invitation reserved for an active fresh run: %s", slot.label)
	}
	return nil
}

// setOpenInvite enables, rotates, or disables a room's reusable invite code.
func setOpenInvite(ctx context.Context, tx *sql.Tx, actor *participant, workspace string, enabled bool, now int64) (wire.Value, error) {
	if err := checkManager(actor, "Only a lead, co-lead or the human can manage open invites"); err != nil {
		return wire.Value{}, err
	}
	hadCode := actor.OpenInviteHash.Valid
	if !enabled {
		if hadCode {
			if _, err := tx.ExecContext(ctx, "UPDATE conversation SET open_invite_hash=? WHERE conversation.id = ?", nil, actor.Room); err != nil {
				return wire.Value{}, err
			}
			if err := recordEvent(ctx, tx, actor.Room, actor.ID, "Disabled open invite.", now); err != nil {
				return wire.Value{}, err
			}
		}
		return singleField("open_invite", wire.BoolValue(false)), nil
	}
	code, hash := newOpenInviteCode(workspace)
	if _, err := tx.ExecContext(ctx, "UPDATE conversation SET open_invite_hash=? WHERE conversation.id = ?", hash, actor.Room); err != nil {
		return wire.Value{}, err
	}
	event := "Enabled open invite."
	if hadCode {
		event = "Rotated the open invite code."
	}
	if err := recordEvent(ctx, tx, actor.Room, actor.ID, event, now); err != nil {
		return wire.Value{}, err
	}
	result := wire.NewObject()
	result.Set("open_invite", wire.BoolValue(true))
	result.Set("code", wire.TextValue(code))
	result.Set("join_prompt", wire.TextValue(formatOpenJoinPrompt(actor.Brief, "", false)))
	return result, nil
}
