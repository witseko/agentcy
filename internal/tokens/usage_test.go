package tokens

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/store"
)

// testKey is a valid workspace key for tests.
var testKey = strings.Repeat("cd", 32)

// roomSetup creates one conversation with three slots: 1 without a model, 2 bound to
// "m2", and 3 without a model but launched by the "zai" plugin.
const roomSetup = `
INSERT INTO conversation (id, brief, open) VALUES ('r1', 'brief', 1);
INSERT INTO participant (id, room, label, role) VALUES (1, 'r1', 'Lead', 1);
INSERT INTO participant (id, room, label, role, model) VALUES (2, 'r1', 'Worker', 0, 'm2');
INSERT INTO participant (id, room, label, role, model) VALUES (3, 'r1', 'Fresh', 0, '');
INSERT INTO assignment (id, slot, room, plugin, invite_hash, state, deadline) VALUES ('a', 3, 'r1', 'zai', 'h', 'running', 0);
`

// openRoom returns a new workspace store holding roomSetup, and its data directory.
func openRoom(t *testing.T) (*store.Store, string) {
	t.Helper()
	ctx := context.Background()
	directory := newDataDir(t)
	stores := store.NewRegistry(directory)
	t.Cleanup(func() { _ = stores.Close() })
	workspace, err := stores.Open(ctx, testKey)
	if err != nil {
		t.Fatal(err)
	}
	err = workspace.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, roomSetup)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, directory
}

// usageOperations runs the same sequence of usage updates as pythonUsage and returns
// the snapshots.
func usageOperations(ctx context.Context, tx *sql.Tx) ([]*SlotSnapshot, error) {
	var snapshots []*SlotSnapshot
	steps := []func() (*SlotSnapshot, error){
		func() (*SlotSnapshot, error) { return AddEstimated(ctx, tx, "r1", 1, 0, 0, 100) },
		func() (*SlotSnapshot, error) { return AddEstimated(ctx, tx, "r1", 1, 10, 0, 101) },
		func() (*SlotSnapshot, error) { return AddEstimated(ctx, tx, "r1", 1, 5, 7, 102) },
		func() (*SlotSnapshot, error) { return AddEstimated(ctx, tx, "r1", 2, 0, 3, 103) },
		func() (*SlotSnapshot, error) {
			return RecordExact(ctx, tx, "r1", 2, ExactUsage{InputTokens: 50, OutputTokens: 20, Source: SourceReported}, 104)
		},
		func() (*SlotSnapshot, error) { return AddEstimated(ctx, tx, "r1", 2, 4, 4, 105) },
		func() (*SlotSnapshot, error) {
			return RecordExact(ctx, tx, "r1", 2, ExactUsage{InputTokens: 60, OutputTokens: 25, Source: SourcePlugin, Model: text("m2b")}, 106)
		},
		func() (*SlotSnapshot, error) { return SetMemberBudget(ctx, tx, 1, text("m1"), pointer(9000)) },
		func() (*SlotSnapshot, error) { return SetMemberBudget(ctx, tx, 3, nil, pointer(500)) },
		func() (*SlotSnapshot, error) { return AddEstimated(ctx, tx, "r1", 3, 8, 0, 107) },
	}
	for _, step := range steps {
		snapshot, err := step()
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

// snapshotJSON encodes snapshots with Python's dataclass field names.
func snapshotJSON(snapshots []*SlotSnapshot) []any {
	encoded := []any{}
	for _, snapshot := range snapshots {
		if snapshot == nil {
			encoded = append(encoded, nil)
			continue
		}
		encoded = append(encoded, map[string]any{
			"room": snapshot.Room, "slot": snapshot.Slot, "model": snapshot.Model,
			"input_tokens": snapshot.InputTokens, "output_tokens": snapshot.OutputTokens,
			"source": snapshot.Source, "updated": snapshot.Updated,
		})
	}
	return encoded
}

// roomUsageJSON encodes RoomUsage like Python's room_usage, keyed by slot.
func roomUsageJSON(slots []SlotUsage) map[string]any {
	encoded := map[string]any{}
	for _, slot := range slots {
		encoded[jsonText(slot.Slot)] = map[string]any{
			"model": slot.Model, "token_budget": slot.TokenBudget, "input_tokens": slot.InputTokens,
			"output_tokens": slot.OutputTokens, "conversation_tokens": slot.ConversationTokens,
			"estimated_tokens": slot.EstimatedTokens, "actual_tokens": slot.ActualTokens,
			"source": slot.Source, "has_usage": slot.HasUsage,
		}
	}
	return encoded
}

// jsonText encodes a value as JSON text.
func jsonText(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// runUsage runs usageOperations and returns the snapshots and room usage as JSON text.
func runUsage(t *testing.T, workspace *store.Store) string {
	t.Helper()
	ctx := context.Background()
	var result string
	err := workspace.Write(ctx, func(tx *sql.Tx) error {
		snapshots, err := usageOperations(ctx, tx)
		if err != nil {
			return err
		}
		slots, err := RoomUsage(ctx, tx, "r1")
		if err != nil {
			return err
		}
		result = jsonText([]any{snapshotJSON(snapshots), roomUsageJSON(slots)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestUsageUpdatesAndRoomUsage(t *testing.T) {
	workspace, _ := openRoom(t)
	result := runUsage(t, workspace)
	var decoded []json.RawMessage
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatal(err)
	}
	wantSnapshots := `[null,` +
		`{"input_tokens":10,"model":null,"output_tokens":0,"room":"r1","slot":1,"source":"estimated","updated":101},` +
		`{"input_tokens":15,"model":null,"output_tokens":7,"room":"r1","slot":1,"source":"estimated","updated":102},` +
		`{"input_tokens":0,"model":"m2","output_tokens":3,"room":"r1","slot":2,"source":"estimated","updated":103},` +
		`{"input_tokens":50,"model":"m2","output_tokens":20,"room":"r1","slot":2,"source":"reported","updated":104},` +
		`{"input_tokens":50,"model":"m2","output_tokens":20,"room":"r1","slot":2,"source":"reported","updated":105},` +
		`{"input_tokens":60,"model":"m2b","output_tokens":25,"room":"r1","slot":2,"source":"plugin","updated":106},` +
		`{"input_tokens":15,"model":"m1","output_tokens":7,"room":"r1","slot":1,"source":"estimated","updated":102},` +
		`null,` +
		`{"input_tokens":8,"model":"","output_tokens":0,"room":"r1","slot":3,"source":"estimated","updated":107}]`
	if string(decoded[0]) != wantSnapshots {
		t.Errorf("snapshots:\n got %s\nwant %s", decoded[0], wantSnapshots)
	}
	wantRoom := `{"1":{"actual_tokens":null,"conversation_tokens":22,"estimated_tokens":22,"has_usage":true,"input_tokens":15,"model":"m1","output_tokens":7,"source":"estimated","token_budget":9000},` +
		`"2":{"actual_tokens":85,"conversation_tokens":85,"estimated_tokens":11,"has_usage":true,"input_tokens":60,"model":"m2b","output_tokens":25,"source":"plugin","token_budget":null},` +
		`"3":{"actual_tokens":null,"conversation_tokens":8,"estimated_tokens":8,"has_usage":true,"input_tokens":8,"model":"zai","output_tokens":0,"source":"estimated","token_budget":500}}`
	if string(decoded[1]) != wantRoom {
		t.Errorf("room usage:\n got %s\nwant %s", decoded[1], wantRoom)
	}
}

func TestRoomUsageWithoutUsageRows(t *testing.T) {
	workspace, _ := openRoom(t)
	ctx := context.Background()
	err := workspace.Read(ctx, func(tx *sql.Tx) error {
		slots, err := RoomUsage(ctx, tx, "r1")
		if err != nil {
			return err
		}
		if len(slots) != 3 {
			t.Fatalf("%d slots", len(slots))
		}
		for _, slot := range slots {
			if slot.HasUsage || slot.Source != SourceEstimated || slot.ActualTokens != nil || slot.ConversationTokens != 0 {
				t.Errorf("slot without usage: %+v", slot)
			}
		}
		snapshot, err := ReadSlotSnapshot(ctx, tx, 1)
		if snapshot != nil || err != nil {
			t.Errorf("ReadSlotSnapshot without a row = %+v, %v", snapshot, err)
		}
		empty, err := RoomUsage(ctx, tx, "missing")
		if err != nil || len(empty) != 0 {
			t.Errorf("RoomUsage of a missing room = %v, %v", empty, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
