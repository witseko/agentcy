package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/wire"
)

// maxParticipants caps the non-human slots of a conversation (MAX_PARTICIPANTS).
const maxParticipants = 17

// maxLabelLength is the longest member name in code points.
const maxLabelLength = 64

// teamSlot is one slot of a new conversation with its role and optional model binding.
type teamSlot struct {
	label       string
	role        int64
	model       *string
	tokenBudget *int64
}

// teamRoles builds one roster, the initiator first, and selects shared leadership when
// a co-lead was invited (team_roles in repository.py).
func teamRoles(initiator string, invitees []wire.Slot) ([]teamSlot, error) {
	if err := checkName(initiator); err != nil {
		return nil, err
	}
	team := []teamSlot{{label: initiator, role: roleLead}}
	seen := map[string]bool{foldCase(initiator): true}
	for _, slot := range invitees {
		invitation := slotInvitation(slot)
		if err := checkName(invitation.Name); err != nil {
			return nil, err
		}
		folded := foldCase(invitation.Name)
		if seen[folded] {
			return nil, reject("Use a different name for each participant")
		}
		seen[folded] = true
		member := teamSlot{label: invitation.Name, role: roleWorker}
		if slot.Invitation != nil {
			member.model = invitation.Model
			member.tokenBudget = invitation.TokenBudget
		}
		if invitation.Role == "co-lead" {
			team[0].role = roleCoLead
			member.role = roleCoLead
		}
		team = append(team, member)
	}
	return team, nil
}

// slotInvitation returns the invitation of a slot; a plain name is a worker invitation.
func slotInvitation(slot wire.Slot) wire.Invitation {
	if slot.Invitation != nil {
		return *slot.Invitation
	}
	return wire.Invitation{Name: slot.Name, Role: "worker"}
}

// newRoom is the settings of a conversation to insert.
type newRoom struct {
	workspace       string
	brief           string
	openInvite      bool
	sealed          bool
	deadlineMinutes *int64
}

// insertedRoom is a stored conversation with its round fields and open-invite code.
type insertedRoom struct {
	id            string
	round         sql.NullString
	roundDeadline sql.NullInt64
	openCode      string
}

// defaultDeadlineMinutes is the sealed round deadline when the creator sets none.
const defaultDeadlineMinutes = 10

const insertConversation = `INSERT INTO conversation (id, brief, open, round, round_deadline, created, open_invite_hash) VALUES (?, ?, ?, ?, ?, ?, ?)`

// insertRoom stores an open conversation (insert_room in repository.py).
func insertRoom(ctx context.Context, tx *sql.Tx, settings newRoom, now int64) (insertedRoom, error) {
	room := insertedRoom{id: tokenHex(12)}
	if settings.sealed {
		minutes := int64(defaultDeadlineMinutes)
		if settings.deadlineMinutes != nil {
			minutes = *settings.deadlineMinutes
		}
		room.round = sql.NullString{String: roundSealed, Valid: true}
		room.roundDeadline = sql.NullInt64{Int64: now + minutes*60, Valid: true}
	}
	var openHash sql.NullString
	if settings.openInvite {
		code, hash := newOpenInviteCode(settings.workspace)
		room.openCode = code
		openHash = sql.NullString{String: hash, Valid: true}
	}
	_, err := tx.ExecContext(ctx, insertConversation, room.id, settings.brief, 1, room.round,
		room.roundDeadline, now, openHash)
	return room, err
}

const insertInvitationSlot = `INSERT INTO participant (room, label, role, invite_hash, expires, model, token_budget) VALUES (?, ?, ?, ?, ?, ?, ?)`

// insertInvitation reserves one slot and returns its new join code.
func insertInvitation(ctx context.Context, tx *sql.Tx, room, workspace string, slot teamSlot, now int64) (string, error) {
	code := newJoinCode(workspace, now)
	_, err := tx.ExecContext(ctx, insertInvitationSlot, room, slot.label, slot.role, code.hash,
		code.expires, slot.model, slot.tokenBudget)
	return code.code, err
}

const insertJoinedMember = `INSERT INTO participant (room, label, role, token_hash, model, token_budget) VALUES (?, ?, ?, ?, ?, ?)`

// insertMember adds a member who holds a token from the start and returns its ID.
func insertMember(ctx context.Context, tx *sql.Tx, room string, slot teamSlot, token string) (int64, error) {
	result, err := tx.ExecContext(ctx, insertJoinedMember, room, slot.label, slot.role, digest(token),
		slot.model, slot.tokenBudget)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// roundSettings copies the round fields of a new conversation into a result, as the
// Python create results spread round_state.
func roundSettings(result wire.Value, room insertedRoom) {
	if room.round.Valid {
		result.Set("round", wire.TextValue(room.round.String))
		result.Set("round_deadline", wire.IntValue(room.roundDeadline.Int64))
	}
}

// create returns the creator credential and a different invitation for each slot.
func create(ctx context.Context, tx *sql.Tx, request *wire.Create, now int64) (wire.Value, error) {
	team, err := teamRoles(request.Initiator, request.Invitees)
	if err != nil {
		return wire.Value{}, err
	}
	room, err := insertRoom(ctx, tx, newRoom{
		workspace: request.Workspace, brief: request.Brief, openInvite: request.OpenInvite,
		sealed: request.Sealed, deadlineMinutes: request.DeadlineMinutes,
	}, now)
	if err != nil {
		return wire.Value{}, err
	}
	token := newParticipantToken()
	initiator := team[0]
	initiator.model = request.InitiatorModel
	initiator.tokenBudget = request.InitiatorTokenBudget
	if _, err := insertMember(ctx, tx, room.id, initiator, token); err != nil {
		return wire.Value{}, err
	}
	invitations := wire.NewObject()
	for _, slot := range team[1:] {
		code, err := insertInvitation(ctx, tx, room.id, request.Workspace, slot, now)
		if err != nil {
			return wire.Value{}, err
		}
		invitations.Set(slot.label, wire.TextValue(code))
	}
	members, err := roster(ctx, tx, room.id)
	if err != nil {
		return wire.Value{}, err
	}
	result := wire.NewObject()
	result.Set("conversation", wire.TextValue(room.id))
	result.Set("token", wire.TextValue(token))
	result.Set("invitations", invitations)
	result.Set("join_prompt", wire.TextValue(formatJoinPrompt(request.Brief, joinPromptOptions{})))
	result.Set("role", wire.TextValue(roleLabel(initiator.role)))
	result.Set("roster", members)
	roundSettings(result, room)
	if room.openCode != "" {
		openInvite := wire.NewObject()
		openInvite.Set("code", wire.TextValue(room.openCode))
		openInvite.Set("join_prompt", wire.TextValue(formatOpenJoinPrompt(request.Brief, "", false)))
		result.Set("open_invite", openInvite)
	}
	return result, nil
}
