package conversation

import (
	"context"
	"database/sql"
	"errors"
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
// in one transaction. Between requests it keeps only caches: the settings files, the
// workspace keys, and the derived task and activity state of recent rooms (roomCache),
// which every use checks against the store. It is safe for concurrent use.
type Handler struct {
	dataDir       string
	book          *usageBook
	plugins       *fresh.SettingsFile
	workspaceKeys *paths.WorkspaceKeyCache
	freshRuns     FreshRuns
	rooms         *roomCache
	logger        *log.Logger
}

// FreshRuns is the fresh-run worker as the rules see it. *fresh.Worker implements it.
type FreshRuns interface {
	// Ready reports whether the worker has reconciled earlier runs and takes launches.
	Ready() bool
	// Dispatch hands a committed launch with its join code to the worker.
	Dispatch(job fresh.Dispatch)
	// Wake tells the worker that a write to the workspace may have stopped a run.
	Wake(workspace string)
}

// NewHandler returns the handler for the checked data directory dataDir. environment is
// the process environment in os.Environ form, used to find Git for workspace keys, and
// logger receives the warnings that Python logs. freshRuns is the fresh-run worker; with
// nil every launch is refused as not ready.
func NewHandler(dataDir string, environment []string, logger *log.Logger, freshRuns FreshRuns) *Handler {
	return &Handler{
		dataDir:       dataDir,
		book:          &usageBook{dataDir: dataDir, settings: tokens.NewSettingsFile(dataDir), logger: logger},
		plugins:       fresh.NewSettingsFile(dataDir),
		workspaceKeys: paths.NewWorkspaceKeyCache(environment, nil),
		freshRuns:     freshRuns,
		rooms:         newRoomCache(),
		logger:        logger,
	}
}

// The handler must satisfy the daemon's interface.
var _ core.Handler = (*Handler)(nil)

// unsupportedStoreHelp is the core.log line for a store at another schema version. The
// client gets only the Python core's message, store.ErrUnsupportedVersion, so the steps
// that fix the store go to the log, where the user looks after a failed call.
const unsupportedStoreHelp = "%s in %s.sqlite3: this core opens schema version %d only. " +
	"If an older agentcy last opened the store, run 'make stop-core' and then " +
	"'make migrate-stores' in the agentcy clone, then retry. If a newer agentcy wrote it, " +
	"upgrade agentcy."

// openStore opens the store of a workspace key. A store at another schema version is
// refused with store.ErrUnsupportedVersion as the client's message, and logged with the
// steps that fix it.
func (handler *Handler) openStore(ctx context.Context, workspace string) (*store.Store, error) {
	workspaceStore, err := store.Open(ctx, handler.dataDir, workspace)
	if errors.Is(err, store.ErrUnsupportedVersion) {
		handler.logger.Printf(unsupportedStoreHelp, store.ErrUnsupportedVersion, workspace, store.SchemaVersion)
		return nil, reject(store.ErrUnsupportedVersion.Error())
	}
	return workspaceStore, err
}

// writeResult runs work in a write transaction of a workspace store, creating the store
// when it is missing, as Python's transaction() does.
func (handler *Handler) writeResult(ctx context.Context, workspace string, work func(ctx context.Context, tx *sql.Tx) (wire.Value, error)) (wire.Value, error) {
	workspaceStore, err := handler.openStore(ctx, workspace)
	if err != nil {
		return wire.Value{}, err
	}
	ctx, rooms := withRoomCache(ctx, handler.rooms, workspaceStore.Path())
	ctx, syncs := withLedgerSync(ctx)
	var result wire.Value
	err = workspaceStore.Write(ctx, func(tx *sql.Tx) error {
		var workErr error
		result, workErr = work(ctx, tx)
		return workErr
	})
	if err != nil {
		return wire.Value{}, err
	}
	rooms.publish()
	if err := syncs.flush(ctx, handler.book); err != nil {
		return wire.Value{}, err
	}
	return result, nil
}

// readResult runs work in a read transaction of a workspace store that exists.
func (handler *Handler) readResult(ctx context.Context, workspace string, work func(ctx context.Context, tx *sql.Tx) (wire.Value, error)) (wire.Value, error) {
	workspaceStore, err := handler.openStore(ctx, workspace)
	if err != nil {
		return wire.Value{}, err
	}
	ctx, rooms := withRoomCache(ctx, handler.rooms, workspaceStore.Path())
	var result wire.Value
	err = workspaceStore.Read(ctx, func(tx *sql.Tx) error {
		var workErr error
		result, workErr = work(ctx, tx)
		return workErr
	})
	if err == nil {
		rooms.publish()
	}
	return result, err
}

// OpenWait authenticates the token of a waiting read and returns its room and sealed
// round deadline (the first transaction of wait_for_read).
func (handler *Handler) OpenWait(ctx context.Context, request *wire.Read, now time.Time) (core.WaitStart, error) {
	var member *participant
	_, err := handler.readResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
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
// (read_page in concurrency.py). The filter describes the reader as it read the page.
func (handler *Handler) ReadPage(ctx context.Context, request *wire.Read, now time.Time) (core.WaitPage, error) {
	var waitPage core.WaitPage
	_, err := handler.writeResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		member, err := refreshParticipant(ctx, tx, request.Token, true, now.Unix())
		if err != nil {
			return wire.Value{}, err
		}
		page, _, err := readPage(ctx, tx, member, request.After)
		if err != nil {
			return wire.Value{}, err
		}
		page.Filter = newReaderFilter(member, request.After)
		waitPage = page
		return page.Page, nil
	})
	if err != nil {
		return core.WaitPage{}, err
	}
	return waitPage, nil
}

// RecordDelivery records the estimated input tokens of a page that a waiting read
// delivers (_record_delivered_usage in concurrency.py).
func (handler *Handler) RecordDelivery(ctx context.Context, request *wire.Read, page wire.Value, now time.Time) error {
	_, err := handler.writeResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		return wire.Value{}, handler.recordDelivery(ctx, tx, request, page, now.Unix())
	})
	return err
}

// recordDelivery records the estimated input tokens of a delivered page in tx.
func (handler *Handler) recordDelivery(ctx context.Context, tx *sql.Tx, request *wire.Read, page wire.Value, now int64) error {
	messages, _ := page.Get("messages")
	bodies := make([]string, 0, len(messages.Items))
	for _, message := range messages.Items {
		body, _ := message.Get("body")
		bodies = append(bodies, body.StringOf())
	}
	brief, _ := page.Get("brief")
	member, err := authenticate(ctx, tx, request.Token)
	if err != nil {
		return err
	}
	return recordReadUsage(ctx, tx, handler.book, member, request.Workspace, bodies, brief.StringOf(), now)
}

// FinishListen records the delivery of a listen's page, when deliver is true, and
// returns the caller's status in one transaction. The steps are those of a delivery
// record followed by a status request, which is what the shim sent before listen.
func (handler *Handler) FinishListen(ctx context.Context, request *wire.Read, page wire.Value, deliver bool, now time.Time) (wire.Value, error) {
	unix := now.Unix()
	return handler.writeResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		if deliver {
			if err := handler.recordDelivery(ctx, tx, request, page, unix); err != nil {
				return wire.Value{}, err
			}
		}
		member, err := refreshParticipant(ctx, tx, request.Token, true, unix)
		if err != nil {
			return wire.Value{}, err
		}
		current, err := status(ctx, tx, handler.book, member, unix)
		return current.value, err
	})
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
		_, err := handler.readResult(ctx, key, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
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
