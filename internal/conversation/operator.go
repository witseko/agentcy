package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/tokens"
	"github.com/witseko/agentcy/internal/wire"
)

// This file applies the human's operator actions through the same conversation rules as
// members (operator.py).

// readyPrompt returns the join prompt for a slot with its topic, name, and code filled in.
func readyPrompt(role, code, brief, participant string) string {
	return formatJoinPrompt(brief, joinPromptOptions{
		participant: participant, role: role, template: roleJoinTemplate(role), code: code, hasCode: true,
	})
}

// invitationValue describes one slot's invitation with a ready join prompt.
func invitationValue(label, role, code, brief string) wire.Value {
	entry := wire.NewObject()
	entry.Set("participant", wire.TextValue(label))
	entry.Set("role", wire.TextValue(role))
	entry.Set("code", wire.TextValue(code))
	entry.Set("join_prompt", wire.TextValue(readyPrompt(role, code, brief, label)))
	return entry
}

const selectActiveConversations = `SELECT conversation.id, ` + titleSourceColumn + `, conversation.open, conversation.created, (SELECT count(*) AS count_1 
FROM participant 
WHERE participant.room = conversation.id AND participant.role != ? AND participant.retired = ?) AS members, (SELECT count(*) AS count_2 
FROM participant 
WHERE participant.room = conversation.id AND participant.role != ? AND participant.retired = ? AND participant.token_hash IS NOT NULL) AS joined 
FROM conversation 
WHERE conversation.archived = ?` + listOrder

// listConversations summarizes the newest active or archived conversations.
func listConversations(ctx context.Context, tx *sql.Tx, archived bool) (wire.Value, error) {
	rows, err := tx.QueryContext(ctx, selectActiveConversations, 1, titleLength+1, roleHuman, 0, roleHuman, 0,
		boolInteger(archived), listLimit, 0)
	if err != nil {
		return wire.Value{}, err
	}
	defer rows.Close()
	conversations := []wire.Value{}
	for rows.Next() {
		var id, source string
		var open bool
		var created sql.NullInt64
		var members, joined int64
		if err := rows.Scan(&id, &source, &open, &created, &members, &joined); err != nil {
			return wire.Value{}, err
		}
		entry := wire.NewObject()
		entry.Set("conversation", wire.TextValue(id))
		entry.Set("title", wire.TextValue(sessionTitle(source)))
		entry.Set("open", wire.BoolValue(open))
		entry.Set("created", nullableInt(created))
		entry.Set("members", wire.IntValue(members))
		entry.Set("joined", wire.IntValue(joined))
		conversations = append(conversations, entry)
	}
	if err := rows.Err(); err != nil {
		return wire.Value{}, err
	}
	return singleField("conversations", wire.ListValue(conversations)), nil
}

// operatorCreate creates a conversation with an invitation for every slot, the lead
// included, and adds the human.
func operatorCreate(ctx context.Context, tx *sql.Tx, workspace string, request *wire.OperatorCreate, now int64) (wire.Value, error) {
	team, err := teamRoles(request.Lead, request.Invitees)
	if err != nil {
		return wire.Value{}, err
	}
	room, err := insertRoom(ctx, tx, newRoom{
		workspace: workspace, brief: request.Brief, openInvite: request.OpenInvite,
		sealed: request.Sealed, deadlineMinutes: request.DeadlineMinutes,
	}, now)
	if err != nil {
		return wire.Value{}, err
	}
	invitations := make([]wire.Value, 0, len(team))
	for _, slot := range team {
		slot.model, slot.tokenBudget = nil, nil
		_, code, err := insertInvitation(ctx, tx, room.id, workspace, slot, now)
		if err != nil {
			return wire.Value{}, err
		}
		invitations = append(invitations, invitationValue(slot.label, roleLabel(slot.role), code, request.Brief))
	}
	if _, err := addHuman(ctx, tx, room.id); err != nil {
		return wire.Value{}, err
	}
	members, err := roster(ctx, tx, room.id)
	if err != nil {
		return wire.Value{}, err
	}
	result := wire.NewObject()
	result.Set("conversation", wire.TextValue(room.id))
	result.Set("invitations", wire.ListValue(invitations))
	result.Set("roster", members)
	roundSettings(result, room)
	if room.openCode != "" {
		openInvite := wire.NewObject()
		openInvite.Set("code", wire.TextValue(room.openCode))
		openInvite.Set("join_prompt", wire.TextValue(formatOpenJoinPrompt(request.Brief, room.openCode, true)))
		result.Set("open_invite", openInvite)
	}
	return result, nil
}

// operatorShow returns the brief, member status and one transcript page without adding
// the human.
func operatorShow(ctx context.Context, tx *sql.Tx, book *usageBook, request *wire.OperatorShow, now int64) (wire.Value, error) {
	human, err := humanActor(ctx, tx, request.Conversation)
	if err != nil {
		return wire.Value{}, err
	}
	current, err := status(ctx, tx, book, &human.participant, now)
	if err != nil {
		return wire.Value{}, err
	}
	current.value.Delete("participant")
	titleSource := human.Brief
	if human.title.Valid && human.title.String != "" {
		titleSource = human.title.String
	}
	result := wire.NewObject()
	result.Set("conversation", wire.TextValue(request.Conversation))
	result.Set("title", wire.TextValue(sessionTitle(titleSource)))
	result.Set("brief", wire.TextValue(human.Brief))
	result.Set("created", nullableInt(human.created))
	result.Set("archived", wire.BoolValue(human.archived))
	result.Set("status", current.value)
	page := transcriptPage{after: request.After, before: request.Before, tail: request.Tail, pageSize: operatorPageSize}
	err = transcript(ctx, tx, request.Conversation, page, result)
	return result, err
}

// operatorInvite adds, reissues, or reinvites a slot as the human and returns its ready
// join prompt.
func operatorInvite(ctx context.Context, tx *sql.Tx, workspace string, request *wire.OperatorInvite, now int64) (wire.Value, error) {
	human, err := writingActor(ctx, tx, request.Conversation)
	if err != nil {
		return wire.Value{}, err
	}
	invited, err := invite(ctx, tx, &human.participant, inviteRequest{
		workspace: workspace, name: request.Name, role: request.Role, reinvite: request.Reinvite,
	}, now)
	if err != nil {
		return wire.Value{}, err
	}
	return invitationValue(invited.label, invited.role, invited.code, human.Brief), nil
}

// operatorOpenInvite enables, rotates, or disables the open invite as the human.
func operatorOpenInvite(ctx context.Context, tx *sql.Tx, workspace string, request *wire.OperatorOpenInvite, now int64) (wire.Value, error) {
	human, err := writingActor(ctx, tx, request.Conversation)
	if err != nil {
		return wire.Value{}, err
	}
	opened, err := setOpenInvite(ctx, tx, &human.participant, workspace, request.Enabled, now)
	if err != nil || !opened.enabled {
		return opened.value(), err
	}
	ready := wire.NewObject()
	ready.Set("open_invite", wire.BoolValue(true))
	ready.Set("code", wire.TextValue(opened.code))
	ready.Set("join_prompt", wire.TextValue(formatOpenJoinPrompt(human.Brief, opened.code, true)))
	return ready, nil
}

const selectBudgetSlots = `SELECT participant.id, participant.label, participant.role, participant.model, participant.token_budget 
FROM participant 
WHERE participant.room = ?`

// budgetSlot is a slot with its model binding and budget override.
type budgetSlot struct {
	id          int64
	label       string
	role        int64
	model       sql.NullString
	tokenBudget sql.NullInt64
}

// findBudgetSlot returns the slot of a room whose label matches name, or nil.
func findBudgetSlot(ctx context.Context, tx *sql.Tx, room, name string) (*budgetSlot, error) {
	rows, err := tx.QueryContext(ctx, selectBudgetSlots, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		slot := &budgetSlot{}
		if err := rows.Scan(&slot.id, &slot.label, &slot.role, &slot.model, &slot.tokenBudget); err != nil {
			return nil, err
		}
		if sameName(slot.label, name) {
			return slot, nil
		}
	}
	return nil, rows.Err()
}

// setMemberBudget binds a model name and/or a per-conversation starting token budget to
// one participant.
func setMemberBudget(ctx context.Context, tx *sql.Tx, book *usageBook, workspace string, request *wire.OperatorSetMemberBudget) (wire.Value, error) {
	human, err := humanActor(ctx, tx, request.Conversation)
	if err != nil {
		return wire.Value{}, err
	}
	if !human.Open {
		return wire.Value{}, reject(conversationClosed)
	}
	target, err := findBudgetSlot(ctx, tx, request.Conversation, request.Name)
	if err != nil {
		return wire.Value{}, err
	}
	if target == nil {
		return wire.Value{}, reject(unknownParticipant)
	}
	if target.role == roleHuman {
		return wire.Value{}, rejectf("Cannot set a token budget for %s", humanLabel)
	}
	snapshot, err := tokens.SetMemberBudget(ctx, tx, target.id, request.Model, request.TokenBudget)
	if err != nil {
		return wire.Value{}, err
	}
	if err := book.sync(ctx, workspace, snapshot); err != nil {
		return wire.Value{}, err
	}
	result := singleField("participant", wire.TextValue(target.label))
	if request.Model != nil {
		result.Set("model", wire.TextValue(*request.Model))
	} else if target.model.Valid {
		result.Set("model", wire.TextValue(target.model.String))
	}
	if request.TokenBudget != nil {
		result.Set("token_budget", wire.IntValue(*request.TokenBudget))
	} else if target.tokenBudget.Valid {
		result.Set("token_budget", wire.IntValue(target.tokenBudget.Int64))
	}
	return result, nil
}

// archiveConversation archives a closed conversation or returns it to the active list.
// Members keep access.
func archiveConversation(ctx context.Context, tx *sql.Tx, request *wire.OperatorArchive, now int64) (wire.Value, error) {
	human, err := humanActor(ctx, tx, request.Conversation)
	if err != nil {
		return wire.Value{}, err
	}
	if request.Archived && human.Open {
		return wire.Value{}, reject("Close the conversation before archiving it")
	}
	if request.DeprecateTasks {
		writer, err := writingActor(ctx, tx, request.Conversation)
		if err != nil {
			return wire.Value{}, err
		}
		if err := deprecateOpenTasks(ctx, tx, &writer.participant, now); err != nil {
			return wire.Value{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE conversation SET archived=? WHERE conversation.id = ?", boolInteger(request.Archived), request.Conversation); err != nil {
		return wire.Value{}, err
	}
	return singleField("archived", wire.BoolValue(request.Archived)), nil
}

// renameConversation stores a custom title, or clears it so the title follows the brief
// again.
func renameConversation(ctx context.Context, tx *sql.Tx, request *wire.OperatorRename) (wire.Value, error) {
	human, err := humanActor(ctx, tx, request.Conversation)
	if err != nil {
		return wire.Value{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE conversation SET title=? WHERE conversation.id = ?", request.Title, request.Conversation); err != nil {
		return wire.Value{}, err
	}
	source := human.Brief
	if request.Title != nil && *request.Title != "" {
		source = *request.Title
	}
	return singleField("title", wire.TextValue(sessionTitle(source))), nil
}

// operatorTasks returns all tasks of one conversation with their state and timeline.
func operatorTasks(ctx context.Context, tx *sql.Tx, request *wire.OperatorTasks, now int64) (wire.Value, error) {
	if _, err := humanActor(ctx, tx, request.Conversation); err != nil {
		return wire.Value{}, err
	}
	details, err := listTaskDetails(ctx, tx, request.Conversation, now)
	if err != nil {
		return wire.Value{}, err
	}
	return singleField("tasks", details), nil
}

// operatorClose closes the conversation as the human, deprecating its unfinished tasks
// first when asked.
func operatorClose(ctx context.Context, tx *sql.Tx, request *wire.OperatorClose, now int64) (wire.Value, error) {
	human, err := writingActor(ctx, tx, request.Conversation)
	if err != nil {
		return wire.Value{}, err
	}
	if request.DeprecateTasks {
		if err := deprecateOpenTasks(ctx, tx, &human.participant, now); err != nil {
			return wire.Value{}, err
		}
	}
	return closeConversation(ctx, tx, &human.participant, request.Summary, now)
}
