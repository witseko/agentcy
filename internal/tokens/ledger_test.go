package tokens

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/witseko/agentcy/internal/store"
)

// openLedger returns a new token ledger and its data directory.
func openLedger(t *testing.T) (*store.Store, string) {
	t.Helper()
	directory := newDataDir(t)
	stores := store.NewRegistry(directory)
	t.Cleanup(func() { _ = stores.Close() })
	ledger, err := stores.OpenLedger(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ledger, directory
}

// record writes ledger entries or fails the test.
func record(t *testing.T, ledger *store.Store, entries ...LedgerEntry) {
	t.Helper()
	for _, entry := range entries {
		if err := RecordSlotUsage(context.Background(), ledger, entry); err != nil {
			t.Fatal(err)
		}
	}
}

// claude returns a reported ledger entry for the claude-sonnet-4-6 model.
func claude(workspace, room string, slot, input, output, updated int64) LedgerEntry {
	return LedgerEntry{
		Workspace: workspace, Room: room, Slot: slot, Model: "claude-sonnet-4-6",
		InputTokens: input, OutputTokens: output, Source: SourceReported, Updated: updated,
	}
}

// Ported from test_ledger_aggregates_across_conversations_and_is_idempotent.
func TestLedgerAggregatesAcrossConversationsAndIsIdempotent(t *testing.T) {
	ledger, _ := openLedger(t)
	record(t, ledger,
		claude("w1", "r1", 1, 10_000, 5_000, 1_700_000_000),
		claude("w2", "r2", 2, 20_000, 10_000, 1_700_000_500),
		// Updating the same (w1, r1, 1) slot supersedes its earlier count.
		claude("w1", "r1", 1, 12_000, 8_000, 1_700_001_000),
	)
	spend, err := ModelSpend(context.Background(), ledger, []string{"claude-sonnet-4-6", "gemini-2.5-pro"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int64{"claude-sonnet-4-6": 50_000}; !reflect.DeepEqual(spend, want) {
		t.Errorf("spend = %v, want %v", spend, want)
	}
}

// Ported from test_ledger_respects_reset_window_cutoffs.
func TestLedgerRespectsResetWindowCutoffs(t *testing.T) {
	ledger, _ := openLedger(t)
	record(t, ledger,
		claude("w1", "old", 1, 40_000, 10_000, 1_700_000_000),
		claude("w1", "new", 2, 8_000, 2_000, 1_700_100_000),
	)
	cases := []struct {
		since map[string]*int64
		want  int64
	}{
		{map[string]*int64{"claude-sonnet-4-6": pointer(1_700_050_000)}, 10_000},
		{map[string]*int64{"claude-sonnet-4-6": pointer(1_700_100_000)}, 10_000},
		{map[string]*int64{"claude-sonnet-4-6": nil}, 60_000},
		{map[string]*int64{"other": pointer(1_800_000_000)}, 60_000},
	}
	for _, test := range cases {
		spend, err := ModelSpend(context.Background(), ledger, []string{"claude-sonnet-4-6"}, test.since)
		if err != nil {
			t.Fatal(err)
		}
		if spend["claude-sonnet-4-6"] != test.want {
			t.Errorf("since %v: spend = %v, want %d", test.since, spend, test.want)
		}
	}
	spend, err := ModelSpend(context.Background(), ledger, []string{"claude-sonnet-4-6"}, map[string]*int64{"claude-sonnet-4-6": pointer(1_800_000_000)})
	if err != nil || len(spend) != 0 {
		t.Errorf("window with no rows: spend = %v, %v; want an empty map", spend, err)
	}
}

func TestModelSpendSkipsEmptyAndDuplicateNames(t *testing.T) {
	ledger, _ := openLedger(t)
	record(t, ledger, claude("w", "r", 1, 1, 2, 3))
	for _, models := range [][]string{nil, {""}, {"", ""}} {
		spend, err := ModelSpend(context.Background(), ledger, models, nil)
		if err != nil || len(spend) != 0 {
			t.Errorf("ModelSpend(%q) = %v, %v", models, spend, err)
		}
	}
	spend, err := ModelSpend(context.Background(), ledger, []string{"claude-sonnet-4-6", "", "claude-sonnet-4-6"}, nil)
	if err != nil || !reflect.DeepEqual(spend, map[string]int64{"claude-sonnet-4-6": 3}) {
		t.Errorf("spend = %v, %v", spend, err)
	}
}

// TestSyncLedgerNeedsAModel copies only snapshots of slots bound to a model.
func TestSyncLedgerNeedsAModel(t *testing.T) {
	ledger, _ := openLedger(t)
	ctx := context.Background()
	snapshots := []*SlotSnapshot{
		nil,
		{Room: "r", Slot: 1, Model: nil, InputTokens: 5, Source: SourceEstimated, Updated: 1},
		{Room: "r", Slot: 2, Model: text(""), InputTokens: 5, Source: SourceEstimated, Updated: 1},
		{Room: "r", Slot: 3, Model: text("m"), InputTokens: 5, OutputTokens: 6, Source: SourcePlugin, Updated: 9},
	}
	for _, snapshot := range snapshots {
		if err := SyncLedger(ctx, ledger, "w", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	var rows []string
	err := ledger.Read(ctx, func(tx *sql.Tx) error {
		result, err := tx.QueryContext(ctx, "SELECT workspace, room, slot, model, input_tokens, output_tokens, total_tokens, source, updated FROM model_usage")
		if err != nil {
			return err
		}
		defer result.Close()
		for result.Next() {
			var workspace, room, model, source string
			var slot, input, output, total, updated int64
			if err := result.Scan(&workspace, &room, &slot, &model, &input, &output, &total, &source, &updated); err != nil {
				return err
			}
			rows = append(rows, jsonText([]any{workspace, room, slot, model, input, output, total, source, updated}))
		}
		return result.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`["w","r",3,"m",5,6,11,"plugin",9]`}; !reflect.DeepEqual(rows, want) {
		t.Errorf("ledger rows = %v, want %v", rows, want)
	}
}
