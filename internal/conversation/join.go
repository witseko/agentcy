package conversation

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/witseko/agentcy/internal/tokens"
	"github.com/witseko/agentcy/internal/wire"
)

const selectInitialMessages = `SELECT message.id, participant.label, message.body, message.kind, recipient.label AS "to", message.reply_to, message.due_minutes, participant.role AS author_role 
FROM message JOIN participant ON message.author = participant.id LEFT OUTER JOIN participant AS recipient ON message.recipient = recipient.id 
WHERE message.room = ? ORDER BY message.id
 LIMIT ? OFFSET ?`

// joinedMember is the new member that initialMessages records the first read for.
type joinedMember struct {
	room      string
	id        int64
	workspace string
	brief     string
}

// initialMessages loads the first page of existing messages on join and records the
// estimated input tokens of the brief and that page once. It sets messages, cursor and
// more on result, in that order.
func initialMessages(ctx context.Context, tx *sql.Tx, book *usageBook, member joinedMember, result wire.Value, now int64) error {
	rows, err := queryMessageRows(ctx, tx, selectInitialMessages, false, member.room, pageSize+1, 0)
	if err != nil {
		return err
	}
	messages, bodies := messagePage(rows)
	readTokens := tokens.EstimateRead(bodies, member.brief)
	if readTokens > 0 {
		snapshot, err := tokens.AddEstimated(ctx, tx, member.room, member.id, readTokens, 0, now)
		if err != nil {
			return err
		}
		if err := book.sync(ctx, member.workspace, snapshot); err != nil {
			return err
		}
	}
	cursor := int64(0)
	if len(messages) > 0 {
		cursor = rows[len(messages)-1].ID
	}
	result.Set("messages", wire.ListValue(messages))
	result.Set("cursor", wire.IntValue(cursor))
	result.Set("more", wire.BoolValue(len(rows) > pageSize))
	return nil
}

// queryMessageRows runs a message query. withFlags tells whether it also selects the
// wake and task_blocker columns of a member read.
func queryMessageRows(ctx context.Context, tx *sql.Tx, query string, withFlags bool, arguments ...any) ([]messageRow, error) {
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []messageRow
	for rows.Next() {
		var row messageRow
		targets := []any{&row.ID, &row.Label, &row.Body, &row.Kind, &row.To, &row.ReplyTo, &row.DueMinutes, &row.AuthorRole}
		var wake sql.NullBool
		if withFlags {
			targets = append(targets, &wake, &row.TaskBlocker)
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		row.Wake = wake.Valid && wake.Bool
		result = append(result, row)
	}
	return result, rows.Err()
}

const selectOpenRoomByInvite = `SELECT conversation.id, conversation.brief, conversation.round, conversation.round_deadline 
FROM conversation 
WHERE conversation.open_invite_hash = ? AND conversation.open = ?`

const selectMemberLabels = `SELECT participant.label 
FROM participant 
WHERE participant.room = ? AND participant.role != ?`

// joinOpen joins an open conversation through its reusable open-invite code. It returns
// found false when no open conversation has that code.
func joinOpen(ctx context.Context, tx *sql.Tx, book *usageBook, request *wire.Join, codeDigest string, now int64) (wire.Value, bool, error) {
	var room, brief string
	var round sql.NullString
	var roundDeadline sql.NullInt64
	err := tx.QueryRowContext(ctx, selectOpenRoomByInvite, codeDigest, 1).Scan(&room, &brief, &round, &roundDeadline)
	if errors.Is(err, sql.ErrNoRows) {
		return wire.Value{}, false, nil
	}
	if err != nil {
		return wire.Value{}, false, err
	}
	labels, err := collectStrings(ctx, tx, selectMemberLabels, room, roleHuman)
	if err != nil {
		return wire.Value{}, false, err
	}
	if len(labels) >= maxParticipants {
		return wire.Value{}, false, rejectf(participantCapTemplate, maxParticipants)
	}
	label, err := resolveOpenName(labels, request.Name)
	if err != nil {
		return wire.Value{}, false, err
	}
	token := newParticipantToken()
	slot := teamSlot{label: label, role: roleWorker, model: request.Model, tokenBudget: request.TokenBudget}
	memberID, err := insertMember(ctx, tx, room, slot, token)
	if err != nil {
		return wire.Value{}, false, err
	}
	if err := recordEvent(ctx, tx, room, memberID, label+" joined via the open invite.", now); err != nil {
		return wire.Value{}, false, err
	}
	result, err := joinResult(ctx, tx, joinedSlot{room: room, label: label, role: roleWorker, brief: brief, token: token})
	if err != nil {
		return wire.Value{}, false, err
	}
	member := joinedMember{room: room, id: memberID, workspace: request.Workspace, brief: brief}
	if err := initialMessages(ctx, tx, book, member, result, now); err != nil {
		return wire.Value{}, false, err
	}
	roundFields(result, round, roundDeadline)
	return result, true, nil
}

// joinedSlot is what a join result starts with.
type joinedSlot struct {
	room  string
	label string
	role  int64
	brief string
	token string
}

// joinResult starts a join result with the conversation, the member, the roster, the
// brief and the token, in Python's order.
func joinResult(ctx context.Context, tx *sql.Tx, slot joinedSlot) (wire.Value, error) {
	members, err := roster(ctx, tx, slot.room)
	if err != nil {
		return wire.Value{}, err
	}
	result := wire.NewObject()
	result.Set("conversation", wire.TextValue(slot.room))
	result.Set("participant", wire.TextValue(slot.label))
	result.Set("role", wire.TextValue(roleLabel(slot.role)))
	result.Set("roster", members)
	result.Set("brief", wire.TextValue(slot.brief))
	result.Set("token", wire.TextValue(slot.token))
	return result, nil
}

// resolveOpenName picks a unique worker label, honoring an optional requested name with a
// numeric suffix when it is taken, or "Worker N" without one.
func resolveOpenName(existing []string, requested *string) (string, error) {
	taken := map[string]bool{}
	for _, label := range existing {
		taken[foldCase(label)] = true
	}
	if requested == nil {
		for number := 1; ; number++ {
			candidate := "Worker " + strconv.Itoa(number)
			if !taken[foldCase(candidate)] {
				return candidate, nil
			}
		}
	}
	if err := checkName(*requested); err != nil {
		return "", err
	}
	if !taken[foldCase(*requested)] {
		return *requested, nil
	}
	for suffix := 2; ; suffix++ {
		tag := " " + strconv.Itoa(suffix)
		base := pythonRightStrip(firstCodePoints(*requested, maxLabelLength-len(tag)))
		candidate := base + tag
		if !taken[foldCase(candidate)] {
			return candidate, nil
		}
	}
}

const selectInvitedSlot = `SELECT participant.id, participant.room, participant.label, participant.role, participant.invite_hash, participant.expires, participant.token_hash, participant.seen, participant.retired, participant.model, participant.token_budget, conversation.brief, conversation.round, conversation.round_deadline 
FROM participant JOIN conversation ON conversation.id = participant.room 
WHERE participant.invite_hash = ? AND conversation.open = ?`

// invitedSlot is the slot behind a single-use join code.
type invitedSlot struct {
	id            int64
	room          string
	label         string
	role          int64
	expires       sql.NullInt64
	tokenHash     sql.NullString
	retired       bool
	brief         string
	round         sql.NullString
	roundDeadline sql.NullInt64
}

// findInvitedSlot loads the slot of a code digest in an open conversation.
func findInvitedSlot(ctx context.Context, tx *sql.Tx, codeDigest string) (*invitedSlot, error) {
	slot := &invitedSlot{}
	var inviteHash, model sql.NullString
	var seen, budget sql.NullInt64
	err := tx.QueryRowContext(ctx, selectInvitedSlot, codeDigest, 1).Scan(
		&slot.id, &slot.room, &slot.label, &slot.role, &inviteHash, &slot.expires,
		&slot.tokenHash, &seen, &slot.retired, &model, &budget, &slot.brief, &slot.round,
		&slot.roundDeadline)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return slot, nil
}

// invalidInvitationError is the refusal of an unknown or expired code. An untagged code
// gets the --workspace help, because a code from another workspace looks unknown here.
func invalidInvitationError(tagged bool) error {
	if !tagged {
		return reject(invalidInvitation + ". " + joinWorkspaceHelp)
	}
	return reject(invalidInvitation)
}

// join consumes a single-use slot invite or joins through a room's shared open invite.
func join(ctx context.Context, tx *sql.Tx, book *usageBook, request *wire.Join, now int64) (wire.Value, error) {
	tagged, matches := codeTagMatches(request.Code, request.Workspace)
	if tagged && !matches {
		return wire.Value{}, reject("Join code does not match this workspace. " + joinWorkspaceHelp)
	}
	codeDigest := digest(request.Code)
	slot, err := findInvitedSlot(ctx, tx, codeDigest)
	if err != nil {
		return wire.Value{}, err
	}
	if slot == nil {
		opened, found, err := joinOpen(ctx, tx, book, request, codeDigest, now)
		if err != nil || found {
			return opened, err
		}
		return wire.Value{}, invalidInvitationError(tagged)
	}
	// Python compares expires <= time.time() and would raise on a NULL expiry; a slot
	// without one is refused as expired here.
	if slot.retired || !slot.expires.Valid || slot.expires.Int64 <= now {
		return wire.Value{}, invalidInvitationError(tagged)
	}
	if slot.tokenHash.Valid && slot.tokenHash.String != "" {
		return wire.Value{}, rejectf("Invitation slot already taken: %s", slot.label)
	}
	token := newParticipantToken()
	if err := claimSlot(ctx, tx, slot.id, token, request); err != nil {
		return wire.Value{}, err
	}
	result, err := joinResult(ctx, tx, joinedSlot{room: slot.room, label: slot.label, role: slot.role, brief: slot.brief, token: token})
	if err != nil {
		return wire.Value{}, err
	}
	member := joinedMember{room: slot.room, id: slot.id, workspace: request.Workspace, brief: slot.brief}
	if err := initialMessages(ctx, tx, book, member, result, now); err != nil {
		return wire.Value{}, err
	}
	roundFields(result, slot.round, slot.roundDeadline)
	return result, nil
}

// claimSlot gives a slot its token and the joining chat's optional model and budget.
func claimSlot(ctx context.Context, tx *sql.Tx, slot int64, token string, request *wire.Join) error {
	query := "UPDATE participant SET token_hash=?"
	arguments := []any{digest(token)}
	if request.Model != nil {
		query += ", model=?"
		arguments = append(arguments, *request.Model)
	}
	if request.TokenBudget != nil {
		query += ", token_budget=?"
		arguments = append(arguments, *request.TokenBudget)
	}
	query += " WHERE participant.id = ?"
	_, err := tx.ExecContext(ctx, query, append(arguments, slot)...)
	return err
}
