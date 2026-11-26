package tokens

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/witseko/agentcy/internal/store"
)

// LedgerEntry is one conversation slot's cumulative spend in the user-level ledger.
type LedgerEntry struct {
	Workspace    string
	Room         string
	Slot         int64
	Model        string
	InputTokens  int64
	OutputTokens int64
	Source       string
	Updated      int64
}

// SyncLedger copies a slot snapshot into the user-level ledger when the slot is bound to
// a model; a nil snapshot or one without a model is skipped. It is the one place that
// writes the ledger on the request path, so that the writes can later move to a
// background writer (port plan fix L6). For now it writes synchronously, as Python does.
func SyncLedger(ctx context.Context, ledger *store.Store, workspace string, snapshot *SlotSnapshot) error {
	if snapshot == nil || snapshot.Model == nil || *snapshot.Model == "" {
		return nil
	}
	return RecordSlotUsage(ctx, ledger, LedgerEntry{
		Workspace:    workspace,
		Room:         snapshot.Room,
		Slot:         snapshot.Slot,
		Model:        *snapshot.Model,
		InputTokens:  snapshot.InputTokens,
		OutputTokens: snapshot.OutputTokens,
		Source:       snapshot.Source,
		Updated:      snapshot.Updated,
	})
}

const upsertModelUsage = `
INSERT INTO model_usage
	(workspace, room, slot, model, input_tokens, output_tokens, total_tokens, source, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (workspace, room, slot) DO UPDATE SET
	model = excluded.model,
	input_tokens = excluded.input_tokens,
	output_tokens = excluded.output_tokens,
	total_tokens = excluded.total_tokens,
	source = excluded.source,
	updated = excluded.updated`

// RecordSlotUsage upserts one slot's cumulative spend: a later record for the same
// workspace, room and slot replaces the earlier one instead of adding to it.
func RecordSlotUsage(ctx context.Context, ledger *store.Store, entry LedgerEntry) error {
	return ledger.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, upsertModelUsage,
			entry.Workspace, entry.Room, entry.Slot, entry.Model, entry.InputTokens,
			entry.OutputTokens, entry.InputTokens+entry.OutputTokens, entry.Source, entry.Updated)
		return err
	})
}

// ModelSpend sums the spend of each named model across all conversations. A model with a
// cutoff in sinceByModel counts only slots updated at or after it; a model without one
// counts everything. Models without ledger rows and empty names are left out of the
// result.
func ModelSpend(ctx context.Context, ledger *store.Store, models []string, sinceByModel map[string]*int64) (map[string]int64, error) {
	spend := map[string]int64{}
	unique := uniqueModels(models)
	if len(unique) == 0 {
		return spend, nil
	}
	clauses := make([]string, 0, len(unique))
	args := make([]any, 0, len(unique)*2)
	for _, model := range unique {
		if since := sinceByModel[model]; since != nil {
			clauses = append(clauses, "(model = ? AND updated >= ?)")
			args = append(args, model, *since)
		} else {
			clauses = append(clauses, "model = ?")
			args = append(args, model)
		}
	}
	query := "SELECT model, coalesce(sum(total_tokens), 0) FROM model_usage WHERE " +
		strings.Join(clauses, " OR ") + " GROUP BY model"
	err := ledger.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var model string
			var total int64
			if err := rows.Scan(&model, &total); err != nil {
				return err
			}
			spend[model] = total
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return spend, nil
}

// uniqueModels returns the distinct non-empty names in sorted order.
func uniqueModels(models []string) []string {
	unique := slices.DeleteFunc(slices.Clone(models), func(model string) bool { return model == "" })
	slices.Sort(unique)
	return slices.Compact(unique)
}
