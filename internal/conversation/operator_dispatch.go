package conversation

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/wire"
)

// Operator applies one operator request whose key the daemon accepted (Core.operate in
// core.py after its key check). Like Member, a write that wakes a room also wakes the
// fresh-run worker for that workspace.
func (handler *Handler) Operator(ctx context.Context, request wire.OperatorRequest, now time.Time) (core.Outcome, error) {
	outcome, err := handler.operator(ctx, request, now)
	if err == nil {
		for _, room := range outcome.Notify {
			handler.wakeFreshRuns(room.Workspace)
		}
	}
	return outcome, err
}

// operator dispatches one operator request.
func (handler *Handler) operator(ctx context.Context, request wire.OperatorRequest, now time.Time) (core.Outcome, error) {
	if _, isPlugins := request.(*wire.OperatorPlugins); isPlugins {
		result, err := handler.pluginNames()
		return core.Outcome{Result: result}, err
	}
	directory, conversation := operatorTarget(request)
	workspace, err := handler.checkoutWorkspace(directory)
	if err != nil {
		return core.Outcome{}, err
	}
	if _, isCreate := request.(*wire.OperatorCreate); !isCreate && !handler.storeExists(workspace) {
		// Listing and searching must not create a store for a checkout that has none.
		return withoutStore(request)
	}
	if launch, isLaunch := request.(*wire.OperatorLaunch); isLaunch {
		return handler.operatorLaunch(ctx, workspace, launch, now.Unix())
	}
	unix := now.Unix()
	var result wire.Value
	if readsOnly(request) {
		result, err = handler.readResult(ctx, workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
			return handler.applyOperatorRead(ctx, tx, request, unix)
		})
	} else {
		result, err = handler.writeResult(ctx, workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
			return handler.applyOperatorWrite(ctx, tx, workspace, request, unix)
		})
	}
	if err != nil {
		return core.Outcome{}, err
	}
	outcome := core.Outcome{Result: result}
	if notifiesRoom(request) {
		outcome.Notify = []core.Room{{Workspace: workspace, Conversation: conversation}}
	}
	return outcome, nil
}

// checkoutWorkspace derives the workspace key from an existing absolute checkout path,
// from the workspace key cache (checkout_workspace in operator.py, plan fix L7).
func (handler *Handler) checkoutWorkspace(directory string) (string, error) {
	if !filepath.IsAbs(directory) {
		return "", reject("Directory must be an absolute path")
	}
	key, err := handler.workspaceKeys.Key(directory)
	if err != nil {
		return "", reject("Directory must be an existing directory")
	}
	return key, nil
}

// withoutStore answers a request for a checkout that has no store.
func withoutStore(request wire.OperatorRequest) (core.Outcome, error) {
	switch request.(type) {
	case *wire.OperatorList:
		return core.Outcome{Result: singleField("conversations", wire.ListValue(nil))}, nil
	case *wire.OperatorSearch:
		return core.Outcome{Result: singleField("hits", wire.ListValue(nil))}, nil
	}
	return core.Outcome{}, reject(unknownConversation)
}

// operatorTarget returns the checkout directory and the conversation of a request; the
// conversation is "" for a workspace-level action.
func operatorTarget(request wire.OperatorRequest) (string, string) {
	switch typed := request.(type) {
	case *wire.OperatorList:
		return typed.Directory, ""
	case *wire.OperatorSearch:
		return typed.Directory, ""
	case *wire.OperatorCreate:
		return typed.Directory, ""
	case *wire.OperatorShow:
		return typed.Directory, typed.Conversation
	case *wire.OperatorTasks:
		return typed.Directory, typed.Conversation
	case *wire.OperatorInvite:
		return typed.Directory, typed.Conversation
	case *wire.OperatorOpenInvite:
		return typed.Directory, typed.Conversation
	case *wire.OperatorLaunch:
		return typed.Directory, typed.Conversation
	case *wire.OperatorRetire:
		return typed.Directory, typed.Conversation
	case *wire.OperatorSetRole:
		return typed.Directory, typed.Conversation
	case *wire.OperatorPost:
		return typed.Directory, typed.Conversation
	case *wire.OperatorClose:
		return typed.Directory, typed.Conversation
	case *wire.OperatorReopen:
		return typed.Directory, typed.Conversation
	case *wire.OperatorArchive:
		return typed.Directory, typed.Conversation
	case *wire.OperatorRename:
		return typed.Directory, typed.Conversation
	case *wire.OperatorSetMemberBudget:
		return typed.Directory, typed.Conversation
	}
	return "", ""
}

// readsOnly reports the operator actions that never write.
func readsOnly(request wire.OperatorRequest) bool {
	switch request.(type) {
	case *wire.OperatorList, *wire.OperatorSearch, *wire.OperatorShow, *wire.OperatorTasks:
		return true
	}
	return false
}

// notifiesRoom reports the actions after which the conversation's waiting reads wake
// (NOTIFYING_OPERATOR_ACTIONS in core.py).
func notifiesRoom(request wire.OperatorRequest) bool {
	switch request.(type) {
	case *wire.OperatorInvite, *wire.OperatorOpenInvite, *wire.OperatorRetire, *wire.OperatorSetRole,
		*wire.OperatorSetMemberBudget, *wire.OperatorPost, *wire.OperatorClose, *wire.OperatorReopen:
		return true
	}
	return false
}

// applyOperatorRead runs one read-only operator action.
func (handler *Handler) applyOperatorRead(ctx context.Context, tx *sql.Tx, request wire.OperatorRequest, now int64) (wire.Value, error) {
	switch typed := request.(type) {
	case *wire.OperatorList:
		return listConversations(ctx, tx, typed.Archived)
	case *wire.OperatorSearch:
		return searchConversations(ctx, tx, typed.Query)
	case *wire.OperatorShow:
		return operatorShow(ctx, tx, handler.book, typed, now)
	case *wire.OperatorTasks:
		return operatorTasks(ctx, tx, typed, now)
	}
	return wire.Value{}, errUnknownOperation
}

// applyOperatorWrite runs one operator action that may write (apply in operator.py).
func (handler *Handler) applyOperatorWrite(ctx context.Context, tx *sql.Tx, workspace string, request wire.OperatorRequest, now int64) (wire.Value, error) {
	switch typed := request.(type) {
	case *wire.OperatorCreate:
		return operatorCreate(ctx, tx, workspace, typed, now)
	case *wire.OperatorArchive:
		return archiveConversation(ctx, tx, typed, now)
	case *wire.OperatorRename:
		return renameConversation(ctx, tx, typed)
	case *wire.OperatorInvite:
		return operatorInvite(ctx, tx, workspace, typed, now)
	case *wire.OperatorOpenInvite:
		return operatorOpenInvite(ctx, tx, workspace, typed, now)
	case *wire.OperatorSetMemberBudget:
		return setMemberBudget(ctx, tx, handler.book, workspace, typed)
	case *wire.OperatorClose:
		return operatorClose(ctx, tx, typed, now)
	}
	return handler.applyHumanAction(ctx, tx, request, now)
}

// applyHumanAction runs an action that the human performs as a member of the room.
func (handler *Handler) applyHumanAction(ctx context.Context, tx *sql.Tx, request wire.OperatorRequest, now int64) (wire.Value, error) {
	_, conversation := operatorTarget(request)
	human, err := writingActor(ctx, tx, conversation)
	if err != nil {
		return wire.Value{}, err
	}
	switch typed := request.(type) {
	case *wire.OperatorRetire:
		return retire(ctx, tx, &human.participant, typed.Name, now)
	case *wire.OperatorSetRole:
		return setRole(ctx, tx, &human.participant, typed.Name, typed.Role, now)
	case *wire.OperatorPost:
		return postInRound(ctx, tx, handler.book, &human.participant, submissionRequest{
			submissionID: typed.SubmissionID, body: typed.Body, kind: typed.Kind, to: typed.To,
			replyTo: typed.ReplyTo, dueMinutes: typed.DueMinutes,
		}, "", now)
	case *wire.OperatorReopen:
		return reopenConversation(ctx, tx, &human.participant, typed.Note, now)
	}
	return wire.Value{}, errUnknownOperation
}
