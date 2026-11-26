package conversation

import (
	"context"
	"database/sql"
	"time"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/store"
	"github.com/witseko/agentcy/internal/wire"
)

// Member applies one validated member request (Core.apply in core.py). After a
// committed write it wakes the authenticated member's room, or the joined room. Any
// write that wakes a room also wakes the fresh-run worker for that workspace, because a
// close, retire or round release stops runs there.
func (handler *Handler) Member(ctx context.Context, request wire.Request, now time.Time) (core.Outcome, error) {
	outcome, err := handler.member(ctx, request, now.Unix())
	if err == nil {
		for _, room := range outcome.Notify {
			handler.wakeFreshRuns(room.Workspace)
		}
		for _, added := range outcome.Added {
			handler.wakeFreshRuns(added.Room.Workspace)
		}
	}
	return outcome, err
}

// member dispatches one member request.
func (handler *Handler) member(ctx context.Context, request wire.Request, unix int64) (core.Outcome, error) {
	switch typed := request.(type) {
	case *wire.Create:
		result, err := handler.writeResult(ctx, typed.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
			return create(ctx, tx, typed, unix)
		})
		return core.Outcome{Result: result}, err
	case *wire.Join:
		result, err := handler.writeResult(ctx, typed.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
			return join(ctx, tx, handler.book, typed, unix)
		})
		if err != nil {
			return core.Outcome{}, err
		}
		room, _ := result.Get("conversation")
		return core.Outcome{Result: result, Notify: []core.Room{{Workspace: typed.Workspace, Conversation: room.Text}}}, nil
	case *wire.ListClosed:
		return handler.listClosed(ctx, typed)
	case *wire.ReadClosed:
		return handler.readClosed(ctx, typed)
	case *wire.Launch:
		return handler.launch(ctx, typed, unix)
	case *wire.McpTaskStatus:
		return handler.mcpTask(ctx, typed.Workspace, typed.TaskID, true, mcpTaskStatus)
	case *wire.McpTaskCancel:
		outcome, err := handler.mcpTask(ctx, typed.Workspace, typed.TaskID, false, cancelMcpTask)
		if err == nil {
			handler.wakeFreshRuns(typed.Workspace)
		}
		return outcome, err
	case *wire.Plugins:
		return handler.memberPlugins(ctx, typed)
	case *wire.ReportUsage:
		return handler.memberReportUsage(ctx, typed, unix)
	}
	return handler.participantRequest(ctx, request, unix)
}

// memberReportUsage writes the member's reported usage, flushes the token ledger after
// commit, and then reads status so same-model peers reflect the updated ledger spend.
func (handler *Handler) memberReportUsage(ctx context.Context, request *wire.ReportUsage, now int64) (core.Outcome, error) {
	var room string
	_, err := handler.writeResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		member, _, err := refreshParticipantReleasing(ctx, tx, request.Token, false, now)
		if err != nil {
			return wire.Value{}, err
		}
		room = member.Room
		return wire.Value{}, recordReportedUsage(ctx, tx, handler.book, member, request, now)
	})
	if err != nil {
		return core.Outcome{}, err
	}
	result, err := handler.readResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		member, err := authenticate(ctx, tx, request.Token)
		if err != nil {
			return wire.Value{}, err
		}
		return reportUsageSummary(ctx, tx, handler.book, member, now)
	})
	if err != nil {
		return core.Outcome{}, err
	}
	return core.Outcome{Result: result, Notify: []core.Room{{Workspace: request.Workspace, Conversation: room}}}, nil
}

// participantMeta holds the per-request routing metadata extracted once from a
// token-authenticated member request.
type participantMeta struct {
	token            string
	workspace        string
	releasesRound    bool
	addsOnlyMessages bool
	noWakeOnRead     bool
	noWake           bool
}

// describeParticipantRequest extracts credentials and wake behavior in one switch.
func describeParticipantRequest(request wire.Request) participantMeta {
	switch typed := request.(type) {
	case *wire.Status:
		return participantMeta{token: typed.Token, workspace: typed.Workspace, releasesRound: true, noWake: true}
	case *wire.Read:
		return participantMeta{token: typed.Token, workspace: typed.Workspace, releasesRound: true, noWakeOnRead: true}
	case *wire.Post:
		return participantMeta{token: typed.Token, workspace: typed.Workspace, releasesRound: true, addsOnlyMessages: typed.Kind != kindProposal}
	case *wire.Invite:
		return participantMeta{token: typed.Token, workspace: typed.Workspace}
	case *wire.Retire:
		return participantMeta{token: typed.Token, workspace: typed.Workspace}
	case *wire.SetRole:
		return participantMeta{token: typed.Token, workspace: typed.Workspace}
	case *wire.OpenInvite:
		return participantMeta{token: typed.Token, workspace: typed.Workspace}
	case *wire.ReportUsage:
		return participantMeta{token: typed.Token, workspace: typed.Workspace}
	case *wire.Close:
		return participantMeta{token: typed.Token, workspace: typed.Workspace}
	case *wire.Reopen:
		return participantMeta{token: typed.Token, workspace: typed.Workspace}
	case *wire.Release:
		return participantMeta{token: typed.Token, workspace: typed.Workspace}
	}
	return participantMeta{}
}

// participantRequest applies a request of an authenticated member and wakes its room,
// except for a status or a read that did not release a sealed round. A post that is not
// a proposal wakes only the waiting reads that the added messages concern, unless the
// request released a sealed round (port plan fix L3).
func (handler *Handler) participantRequest(ctx context.Context, request wire.Request, now int64) (core.Outcome, error) {
	meta := describeParticipantRequest(request)
	var room string
	var added []core.Message
	var roundReleased bool
	targeted := meta.addsOnlyMessages
	result, err := handler.writeResult(ctx, meta.workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		member, released, err := refreshParticipantReleasing(ctx, tx, meta.token, meta.releasesRound, now)
		if err != nil {
			return wire.Value{}, err
		}
		room = member.Room
		roundReleased = released
		targeted = targeted && !released
		if !targeted {
			return handler.applyParticipant(ctx, tx, member, request, now)
		}
		lastMessage, err := lastRoomMessage(ctx, tx, room)
		if err != nil {
			return wire.Value{}, err
		}
		result, err := handler.applyParticipant(ctx, tx, member, request, now)
		if err != nil {
			return wire.Value{}, err
		}
		added, err = addedMessages(ctx, tx, room, lastMessage)
		return result, err
	})
	if err != nil {
		return core.Outcome{}, err
	}
	outcome := core.Outcome{Result: result}
	changed := core.Room{Workspace: meta.workspace, Conversation: room}
	switch {
	case meta.noWake:
	case meta.noWakeOnRead && !roundReleased:
	case targeted:
		if len(added) > 0 {
			outcome.Added = []core.AddedMessages{{Room: changed, Messages: added}}
		}
	default:
		outcome.Notify = []core.Room{changed}
	}
	return outcome, nil
}

// applyParticipant dispatches one request of an authenticated member (apply in
// repository.py).
func (handler *Handler) applyParticipant(ctx context.Context, tx *sql.Tx, member *participant, request wire.Request, now int64) (wire.Value, error) {
	switch typed := request.(type) {
	case *wire.Status:
		current, err := status(ctx, tx, handler.book, member, now)
		return current.value, err
	case *wire.Invite:
		invited, err := invite(ctx, tx, member, inviteRequest{
			workspace: typed.Workspace, name: typed.Name, role: typed.Role, model: typed.Model,
			tokenBudget: typed.TokenBudget, reinvite: typed.Reinvite,
		}, now)
		return invited.value(), err
	case *wire.Retire:
		return retire(ctx, tx, member, typed.Name, now)
	case *wire.SetRole:
		return setRole(ctx, tx, member, typed.Name, typed.Role, now)
	case *wire.OpenInvite:
		opened, err := setOpenInvite(ctx, tx, member, typed.Workspace, typed.Enabled, now)
		return opened.value(), err
	case *wire.Post:
		return postInRound(ctx, tx, handler.book, member, submissionRequest{
			submissionID: typed.SubmissionID, body: typed.Body, kind: typed.Kind, to: typed.To,
			replyTo: typed.ReplyTo, dueMinutes: typed.DueMinutes,
		}, typed.Workspace, now)
	case *wire.Read:
		return read(ctx, tx, handler.book, member, typed, now)
	case *wire.Close:
		return closeConversation(ctx, tx, member, typed.Summary, now)
	case *wire.Reopen:
		return reopenConversation(ctx, tx, member, typed.Note, now)
	case *wire.Release:
		return release(ctx, tx, member, now)
	}
	return wire.Value{}, errUnknownOperation
}

// listClosed lists the closed conversations without creating a store.
func (handler *Handler) listClosed(ctx context.Context, request *wire.ListClosed) (core.Outcome, error) {
	if !store.Exists(handler.dataDir, request.Workspace) {
		return core.Outcome{Result: singleField("conversations", wire.ListValue(nil))}, nil
	}
	result, err := handler.readResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		return listClosed(ctx, tx, request.Archived)
	})
	return core.Outcome{Result: result}, err
}

// readClosed reads a closed conversation without creating a store.
func (handler *Handler) readClosed(ctx context.Context, request *wire.ReadClosed) (core.Outcome, error) {
	if !store.Exists(handler.dataDir, request.Workspace) {
		return core.Outcome{}, reject(unknownClosed)
	}
	result, err := handler.readResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		return readClosed(ctx, tx, request.Conversation, request.After)
	})
	return core.Outcome{Result: result}, err
}

// mcpTask reads or cancels the fresh run behind an MCP task without creating a store.
func (handler *Handler) mcpTask(ctx context.Context, workspace, taskID string, readOnly bool, action func(context.Context, *sql.Tx, string) (wire.Value, error)) (core.Outcome, error) {
	if !store.Exists(handler.dataDir, workspace) {
		return core.Outcome{}, reject(unknownMcpTask)
	}
	run := handler.writeResult
	if readOnly {
		run = handler.readResult
	}
	result, err := run(ctx, workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		return action(ctx, tx, taskID)
	})
	return core.Outcome{Result: result}, err
}
