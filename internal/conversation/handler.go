package conversation

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/fresh"
	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/store"
	"github.com/witseko/agentcy/internal/tokens"
	"github.com/witseko/agentcy/internal/wire"
)

// Handler implements core.Handler: it opens the workspace stores and runs each request
// in one transaction. It holds no conversation state between requests apart from the
// cached settings files and workspace keys, and is safe for concurrent use.
type Handler struct {
	dataDir       string
	book          *usageBook
	plugins       *fresh.SettingsFile
	workspaceKeys *paths.WorkspaceKeyCache
}

// NewHandler returns the handler for the checked data directory dataDir. environment is
// the process environment in os.Environ form, used to find Git for workspace keys, and
// logger receives the warnings that Python logs.
func NewHandler(dataDir string, environment []string, logger *log.Logger) *Handler {
	return &Handler{
		dataDir:       dataDir,
		book:          &usageBook{dataDir: dataDir, settings: tokens.NewSettingsFile(dataDir), logger: logger},
		plugins:       fresh.NewSettingsFile(dataDir),
		workspaceKeys: paths.NewWorkspaceKeyCache(environment, nil),
	}
}

// The handler must satisfy the daemon's interface.
var _ core.Handler = (*Handler)(nil)

// writeResult runs work in a write transaction of a workspace store, creating the store
// when it is missing, as Python's transaction() does.
func (handler *Handler) writeResult(ctx context.Context, workspace string, work func(tx *sql.Tx) (wire.Value, error)) (wire.Value, error) {
	workspaceStore, err := store.Open(ctx, handler.dataDir, workspace)
	if err != nil {
		return wire.Value{}, err
	}
	var result wire.Value
	err = workspaceStore.Write(ctx, func(tx *sql.Tx) error {
		var workErr error
		result, workErr = work(tx)
		return workErr
	})
	return result, err
}

// readResult runs work in a read transaction of a workspace store that exists.
func (handler *Handler) readResult(ctx context.Context, workspace string, work func(tx *sql.Tx) (wire.Value, error)) (wire.Value, error) {
	workspaceStore, err := store.Open(ctx, handler.dataDir, workspace)
	if err != nil {
		return wire.Value{}, err
	}
	var result wire.Value
	err = workspaceStore.Read(ctx, func(tx *sql.Tx) error {
		var workErr error
		result, workErr = work(tx)
		return workErr
	})
	return result, err
}

// OpenWait authenticates the token of a waiting read and returns its room and sealed
// round deadline (the first transaction of wait_for_read).
func (handler *Handler) OpenWait(ctx context.Context, request *wire.Read, now time.Time) (core.WaitStart, error) {
	var member *participant
	_, err := handler.writeResult(ctx, request.Workspace, func(tx *sql.Tx) (wire.Value, error) {
		var authErr error
		member, authErr = authenticate(ctx, tx, request.Token)
		return wire.Value{}, authErr
	})
	if err != nil {
		return core.WaitStart{}, err
	}
	start := core.WaitStart{Room: core.Room{Workspace: request.Workspace, Conversation: member.Room}}
	if member.RoundDeadline.Valid {
		deadline := member.RoundDeadline.Int64
		start.RoundDeadline = &deadline
	}
	return start, nil
}

// ReadPage refreshes the participant and loads one page without recording usage
// (read_page in concurrency.py).
func (handler *Handler) ReadPage(ctx context.Context, request *wire.Read, now time.Time) (wire.Value, error) {
	return handler.writeResult(ctx, request.Workspace, func(tx *sql.Tx) (wire.Value, error) {
		member, err := refreshParticipant(ctx, tx, request.Token, true, now.Unix())
		if err != nil {
			return wire.Value{}, err
		}
		page, _, err := readPage(ctx, tx, member, request.After)
		return page, err
	})
}

// RecordDelivery records the estimated input tokens of a page that a waiting read
// delivers (_record_delivered_usage in concurrency.py).
func (handler *Handler) RecordDelivery(ctx context.Context, request *wire.Read, page wire.Value, now time.Time) error {
	messages, _ := page.Get("messages")
	bodies := make([]string, 0, len(messages.Items))
	for _, message := range messages.Items {
		body, _ := message.Get("body")
		bodies = append(bodies, body.StringOf())
	}
	brief, _ := page.Get("brief")
	_, err := handler.writeResult(ctx, request.Workspace, func(tx *sql.Tx) (wire.Value, error) {
		member, err := authenticate(ctx, tx, request.Token)
		if err != nil {
			return wire.Value{}, err
		}
		return wire.Value{}, recordReadUsage(ctx, tx, handler.book, member, request.Workspace, bodies, brief.StringOf(), now.Unix())
	})
	return err
}

// HasActiveWork reports whether any workspace store has an open conversation or a
// pending assignment (Core.handle_timeout in core.py).
func (handler *Handler) HasActiveWork(ctx context.Context, now time.Time) (bool, error) {
	keys, err := store.WorkspaceKeys(handler.dataDir)
	if err != nil {
		return false, err
	}
	for _, key := range keys {
		active := false
		_, err := handler.writeResult(ctx, key, func(tx *sql.Tx) (wire.Value, error) {
			pending, err := hasPendingAssignment(ctx, tx)
			if err != nil || pending {
				active = pending
				return wire.Value{}, err
			}
			active, err = hasOpenConversation(ctx, tx)
			return wire.Value{}, err
		})
		if err != nil || active {
			return active, err
		}
	}
	return false, nil
}
