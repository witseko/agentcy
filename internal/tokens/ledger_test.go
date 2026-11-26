package tokens

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/store"
)

// openLedger returns a new token ledger and its data directory.
func openLedger(t *testing.T) (*store.Store, string) {
	t.Helper()
	directory := newDataDir(t)
	t.Cleanup(func() { _ = store.CloseAll() })
	ledger, err := store.OpenLedger(context.Background(), directory)
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

// pythonModelSpend reads the ledger of the AGENTCY_HOME in argv[1] with the Python
// model_spend and then records one more slot through Python.
const pythonModelSpend = `
import json, os, sys
os.environ["AGENTCY_HOME"] = sys.argv[1]
from agentcy.tokens import ledger
spend = [
    ledger.model_spend(["claude-sonnet-4-6", "m", "none"]),
    ledger.model_spend(["claude-sonnet-4-6"], {"claude-sonnet-4-6": 1_700_050_000}),
]
ledger.record_slot_usage(workspace="py", room="r", slot=1, model="m", input_tokens=7, output_tokens=8, source="reported", now=5)
print(json.dumps(spend, sort_keys=True, separators=(",", ":")))
`

// TestLedgerMatchesPython lets Python read a ledger that Go created and wrote, then reads
// Python's write back in Go. It skips when uv is unavailable.
func TestLedgerMatchesPython(t *testing.T) {
	pathValue, pathSet := os.LookupEnv("PATH")
	uv := paths.FindOnPath("uv", pathValue, pathSet)
	if uv == "" {
		t.Skip("uv is unavailable")
	}
	ledger, directory := openLedger(t)
	record(t, ledger,
		claude("w1", "old", 1, 40_000, 10_000, 1_700_000_000),
		claude("w1", "new", 2, 8_000, 2_000, 1_700_100_000),
		LedgerEntry{Workspace: "w2", Room: "r", Slot: 4, Model: "m", InputTokens: 1, OutputTokens: 1, Source: SourcePlugin, Updated: 1},
	)
	ctx := context.Background()
	all, err := ModelSpend(ctx, ledger, []string{"claude-sonnet-4-6", "m", "none"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	windowed, err := ModelSpend(ctx, ledger, []string{"claude-sonnet-4-6"}, map[string]*int64{"claude-sonnet-4-6": pointer(1_700_050_000)})
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := filepath.Abs("../..")
	command := exec.Command(uv, "run", "--locked", "--project", repository, "python", "-I", "-c", pythonModelSpend, directory)
	command.Env = append(os.Environ(), paths.HomeVariable+"="+directory)
	output, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("Python failed: %v\n%s", err, exit.Stderr)
		}
		t.Skipf("Python unavailable: %v", err)
	}
	if got := jsonText([]any{all, windowed}); strings.TrimSpace(string(output)) != got {
		t.Errorf("Go spend %s, Python spend %s", got, output)
	}
	after, err := ModelSpend(ctx, ledger, []string{"m"}, nil)
	if err != nil || after["m"] != 17 {
		t.Errorf("after the Python write: spend = %v, %v; want m = 17", after, err)
	}
}
