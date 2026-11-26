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
	}
	return outcome, err
}

// member dispatches one member request.
func (handler *Handler) member(ctx context.Context, request wire.Request, unix int64) (core.Outcome, error) {
	switch typed := request.(type) {
	case *wire.Create:
		result, err := handler.writeResult(ctx, typed.Workspace, func(tx *sql.Tx) (wire.Value, error) {
			return create(ctx, tx, typed, unix)
		})
		return core.Outcome{Result: result}, err
	case *wire.Join:
		result, err := handler.writeResult(ctx, typed.Workspace, func(tx *sql.Tx) (wire.Value, error) {
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
		return handler.mcpTask(ctx, typed.Workspace, typed.TaskID, mcpTaskStatus)
	case *wire.McpTaskCancel:
		outcome, err := handler.mcpTask(ctx, typed.Workspace, typed.TaskID, cancelMcpTask)
		if err == nil {
			handler.wakeFreshRuns(typed.Workspace)
		}
		return outcome, err
	case *wire.Plugins:
		return handler.memberPlugins(ctx, typed)
	}
	return handler.participantRequest(ctx, request, unix)
}

// participantRequest applies a request of an authenticated member and wakes its room,
// except for a status, which writes nothing that others wait for.
func (handler *Handler) participantRequest(ctx context.Context, request wire.Request, now int64) (core.Outcome, error) {
	token, workspace := memberCredentials(request)
	var room string
	result, err := handler.writeResult(ctx, workspace, func(tx *sql.Tx) (wire.Value, error) {
		member, err := refreshParticipant(ctx, tx, token, releasesRound(request), now)
		if err != nil {
			return wire.Value{}, err
		}
		room = member.Room
		return handler.applyParticipant(ctx, tx, member, request, now)
	})
	if err != nil {
		return core.Outcome{}, err
	}
	outcome := core.Outcome{Result: result}
	if _, isStatus := request.(*wire.Status); !isStatus {
		outcome.Notify = []core.Room{{Workspace: workspace, Conversation: room}}
	}
	return outcome, nil
}

// applyParticipant dispatches one request of an authenticated member (apply in
// repository.py).
func (handler *Handler) applyParticipant(ctx context.Context, tx *sql.Tx, member *participant, request wire.Request, now int64) (wire.Value, error) {
	switch typed := request.(type) {
	case *wire.Status:
		result, _, err := status(ctx, tx, handler.book, member, now)
		return result, err
	case *wire.Invite:
		return invite(ctx, tx, member, inviteRequest{
			workspace: typed.Workspace, name: typed.Name, role: typed.Role, model: typed.Model,
			tokenBudget: typed.TokenBudget, reinvite: typed.Reinvite,
		}, now)
	case *wire.Retire:
		return retire(ctx, tx, member, typed.Name, now)
	case *wire.SetRole:
		return setRole(ctx, tx, member, typed.Name, typed.Role, now)
	case *wire.OpenInvite:
		return setOpenInvite(ctx, tx, member, typed.Workspace, typed.Enabled, now)
	case *wire.ReportUsage:
		return reportUsage(ctx, tx, handler.book, member, typed, now)
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

// memberCredentials returns the token and workspace of a request that a member makes
// with its participant token.
func memberCredentials(request wire.Request) (string, string) {
	switch typed := request.(type) {
	case *wire.Status:
		return typed.Token, typed.Workspace
	case *wire.Invite:
		return typed.Token, typed.Workspace
	case *wire.Retire:
		return typed.Token, typed.Workspace
	case *wire.SetRole:
		return typed.Token, typed.Workspace
	case *wire.OpenInvite:
		return typed.Token, typed.Workspace
	case *wire.ReportUsage:
		return typed.Token, typed.Workspace
	case *wire.Post:
		return typed.Token, typed.Workspace
	case *wire.Read:
		return typed.Token, typed.Workspace
	case *wire.Close:
		return typed.Token, typed.Workspace
	case *wire.Reopen:
		return typed.Token, typed.Workspace
	case *wire.Release:
		return typed.Token, typed.Workspace
	}
	return "", ""
}

// releasesRound reports the requests before which a sealed round past its deadline is
// published: read, post and status.
func releasesRound(request wire.Request) bool {
	switch request.(type) {
	case *wire.Read, *wire.Post, *wire.Status:
		return true
	}
	return false
}

// listClosed lists the closed conversations without creating a store.
func (handler *Handler) listClosed(ctx context.Context, request *wire.ListClosed) (core.Outcome, error) {
	if !store.Exists(handler.dataDir, request.Workspace) {
		return core.Outcome{Result: singleField("conversations", wire.ListValue(nil))}, nil
	}
	result, err := handler.readResult(ctx, request.Workspace, func(tx *sql.Tx) (wire.Value, error) {
		return listClosed(ctx, tx, request.Archived)
	})
	return core.Outcome{Result: result}, err
}

// readClosed reads a closed conversation without creating a store.
func (handler *Handler) readClosed(ctx context.Context, request *wire.ReadClosed) (core.Outcome, error) {
	if !store.Exists(handler.dataDir, request.Workspace) {
		return core.Outcome{}, reject(unknownClosed)
	}
	result, err := handler.readResult(ctx, request.Workspace, func(tx *sql.Tx) (wire.Value, error) {
		return readClosed(ctx, tx, request.Conversation, request.After)
	})
	return core.Outcome{Result: result}, err
}

// mcpTask reads or cancels the fresh run behind an MCP task without creating a store.
func (handler *Handler) mcpTask(ctx context.Context, workspace, taskID string, action func(context.Context, *sql.Tx, string) (wire.Value, error)) (core.Outcome, error) {
	if !store.Exists(handler.dataDir, workspace) {
		return core.Outcome{}, reject(unknownMcpTask)
	}
	result, err := handler.writeResult(ctx, workspace, func(tx *sql.Tx) (wire.Value, error) {
		return action(ctx, tx, taskID)
	})
	return core.Outcome{Result: result}, err
}
