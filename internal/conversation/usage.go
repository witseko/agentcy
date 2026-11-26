package conversation

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/witseko/agentcy/internal/store"
	"github.com/witseko/agentcy/internal/tokens"
)

// usageBook gives the rules access to the user-level token ledger and to tokens.json.
// It is shared by all requests and safe for concurrent use.
type usageBook struct {
	stores   *store.Registry
	settings *tokens.SettingsFile
	logger   *log.Logger
}

// writeScope is what travels together through one write transaction: the transaction,
// the usage book, the workspace whose ledger rows the write syncs, and the request time
// in Unix seconds. workspace is "" for a write whose usage is not synced, such as the
// human's post.
type writeScope struct {
	tx        *sql.Tx
	book      *usageBook
	workspace string
	now       int64
}

// pendingLedgerSync is a slot snapshot waiting for its workspace transaction to commit
// before being copied into the user-level token ledger.
type pendingLedgerSync struct {
	workspace string
	snapshot  *tokens.SlotSnapshot
}

// ledgerSyncBuffer collects ledger syncs during a workspace write transaction and flushes
// them only after the transaction commits. It is used by one goroutine.
type ledgerSyncBuffer struct {
	pending []pendingLedgerSync
}

type ledgerSyncKey struct{}

// withLedgerSync attaches a post-commit ledger sync buffer to ctx.
func withLedgerSync(ctx context.Context) (context.Context, *ledgerSyncBuffer) {
	buffer := &ledgerSyncBuffer{}
	return context.WithValue(ctx, ledgerSyncKey{}, buffer), buffer
}

// flush writes all buffered snapshots into the token ledger after commit.
func (buffer *ledgerSyncBuffer) flush(ctx context.Context, book *usageBook) error {
	for _, entry := range buffer.pending {
		if err := book.syncNow(ctx, entry.workspace, entry.snapshot); err != nil {
			return err
		}
	}
	return nil
}

// sync copies a slot snapshot into the ledger when the slot is bound to a model,
// creating the ledger on first use (sync_ledger in Python). Inside a workspace write
// transaction carrying a ledgerSyncBuffer, the write is deferred until after commit.
func (book *usageBook) sync(ctx context.Context, workspace string, snapshot *tokens.SlotSnapshot) error {
	if snapshot == nil || snapshot.Model == nil || *snapshot.Model == "" {
		return nil
	}
	if buffer, ok := ctx.Value(ledgerSyncKey{}).(*ledgerSyncBuffer); ok && buffer != nil {
		buffer.pending = append(buffer.pending, pendingLedgerSync{workspace: workspace, snapshot: snapshot})
		return nil
	}
	return book.syncNow(ctx, workspace, snapshot)
}

// syncNow writes one slot snapshot directly to the user-level token ledger.
func (book *usageBook) syncNow(ctx context.Context, workspace string, snapshot *tokens.SlotSnapshot) error {
	ledger, err := book.stores.OpenLedger(ctx)
	if err != nil {
		return err
	}
	return tokens.SyncLedger(ctx, ledger, workspace, snapshot)
}

// spend sums the ledger spend of each model, or nothing when the ledger file does not
// exist yet (model_spend in Python does not create it).
func (book *usageBook) spend(ctx context.Context, models []string, sinceByModel map[string]*int64) (map[string]int64, error) {
	if len(models) == 0 {
		return map[string]int64{}, nil
	}
	info, err := os.Stat(filepath.Join(book.stores.DataDir(), store.LedgerFilename))
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	ledger, err := book.stores.OpenLedger(ctx)
	if err != nil {
		return nil, err
	}
	return tokens.ModelSpend(ctx, ledger, models, sinceByModel)
}

// tokenSettings loads tokens.json for a status. An unsafe or invalid file is logged and
// ignored, as read_token_config does in Python.
func (book *usageBook) tokenSettings() *tokens.Settings {
	settings, err := book.settings.Load()
	if err != nil {
		book.logger.Print("Ignoring invalid token configuration in status")
		return nil
	}
	return settings
}
