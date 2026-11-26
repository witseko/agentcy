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

// operatorMeta holds the target directory, optional conversation, read-only flag, and
// room-notification flag for one operator request.
type operatorMeta struct {
	directory    string
	conversation string
	readOnly     bool
	notifiesRoom bool
}

// describeOperatorRequest extracts routing metadata for one operator request in a single
// switch.
func describeOperatorRequest(request wire.OperatorRequest) operatorMeta {
	switch typed := request.(type) {
	case *wire.OperatorList:
		return operatorMeta{directory: typed.Directory, readOnly: true}
	case *wire.OperatorSearch:
		return operatorMeta{directory: typed.Directory, readOnly: true}
	case *wire.OperatorCreate:
		return operatorMeta{directory: typed.Directory}
	case *wire.OperatorShow:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, readOnly: true}
	case *wire.OperatorTasks:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, readOnly: true}
	case *wire.OperatorInvite:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, notifiesRoom: true}
	case *wire.OperatorOpenInvite:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, notifiesRoom: true}
	case *wire.OperatorLaunch:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation}
	case *wire.OperatorRetire:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, notifiesRoom: true}
	case *wire.OperatorSetRole:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, notifiesRoom: true}
	case *wire.OperatorPost:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, notifiesRoom: true}
	case *wire.OperatorClose:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, notifiesRoom: true}
	case *wire.OperatorReopen:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, notifiesRoom: true}
	case *wire.OperatorArchive:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, notifiesRoom: typed.DeprecateTasks}
	case *wire.OperatorRename:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation}
	case *wire.OperatorSetMemberBudget:
		return operatorMeta{directory: typed.Directory, conversation: typed.Conversation, notifiesRoom: true}
	}
	return operatorMeta{}
}

// operator dispatches one operator request.
func (handler *Handler) operator(ctx context.Context, request wire.OperatorRequest, now time.Time) (core.Outcome, error) {
	if _, isPlugins := request.(*wire.OperatorPlugins); isPlugins {
		result, err := handler.pluginNames()
		return core.Outcome{Result: result}, err
	}
	meta := describeOperatorRequest(request)
	workspace, err := handler.checkoutWorkspace(meta.directory)
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
	if meta.readOnly {
		result, err = handler.readResult(ctx, workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
			return handler.applyOperatorRead(ctx, tx, request, unix)
		})
	} else {
		result, err = handler.writeResult(ctx, workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
			return handler.applyOperatorWrite(ctx, tx, workspace, meta.conversation, request, unix)
		})
	}
	if err != nil {
		return core.Outcome{}, err
	}
	outcome := core.Outcome{Result: result}
	if meta.notifiesRoom {
		outcome.Notify = []core.Room{{Workspace: workspace, Conversation: meta.conversation}}
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
func (handler *Handler) applyOperatorWrite(ctx context.Context, tx *sql.Tx, workspace, conversation string, request wire.OperatorRequest, now int64) (wire.Value, error) {
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
	return handler.applyHumanAction(ctx, tx, conversation, request, now)
}

// applyHumanAction runs an action that the human performs as a member of the room.
func (handler *Handler) applyHumanAction(ctx context.Context, tx *sql.Tx, conversation string, request wire.OperatorRequest, now int64) (wire.Value, error) {
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
		// The human's usage is not recorded, so the scope names no workspace.
		scope := writeScope{tx: tx, book: handler.book, now: now}
		return postInRound(ctx, scope, &human.participant, submissionRequest{
			submissionID: typed.SubmissionID, body: typed.Body, kind: typed.Kind, to: typed.To,
			replyTo: typed.ReplyTo, dueMinutes: typed.DueMinutes,
		})
	case *wire.OperatorReopen:
		return reopenConversation(ctx, tx, &human.participant, typed.Note, now)
	}
	return wire.Value{}, errUnknownOperation
}
