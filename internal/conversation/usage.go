package conversation

import (
	"context"
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
	dataDir  string
	settings *tokens.SettingsFile
	logger   *log.Logger
}

// sync copies a slot snapshot into the ledger when the slot is bound to a model,
// creating the ledger on first use (sync_ledger in Python).
func (book *usageBook) sync(ctx context.Context, workspace string, snapshot *tokens.SlotSnapshot) error {
	if snapshot == nil || snapshot.Model == nil || *snapshot.Model == "" {
		return nil
	}
	ledger, err := store.OpenLedger(ctx, book.dataDir)
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
	info, err := os.Stat(filepath.Join(book.dataDir, store.LedgerFilename))
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	ledger, err := store.OpenLedger(ctx, book.dataDir)
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
