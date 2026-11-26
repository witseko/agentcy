package conversation

import (
	"context"
	"database/sql"
	"io"
	"log"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// Ported from the former Python test_core_wait.py and the idle check of core.py: the daemon has
// active work while a store has an open conversation or a pending assignment, and a
// data directory without stores has none.
func TestHasActiveWorkCountsOpenConversationsAndPendingAssignments(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	handler := NewHandler(newTestShared(t, dataDir, log.New(io.Discard, "", 0)), nil, nil)
	active := func() bool {
		t.Helper()
		found, err := handler.HasActiveWork(ctx, time.Unix(startUnix, 0))
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	if active() {
		t.Fatal("an empty data directory has active work")
	}
	opened, err := handler.stores.Open(ctx, testWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, store: opened, book: handler.book}
	created := f.createRoom(wire.Create{Invitees: []wire.Slot{{Name: "Worker"}}}, startUnix)
	if !active() {
		t.Error("an open conversation is not active work")
	}
	lead := f.member(text(created, "token"))
	f.mustWrite(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		return closeConversation(ctx, tx, lead, nil, startUnix)
	})
	if active() {
		t.Error("a closed conversation is still active work")
	}
	room := text(created, "conversation")
	for _, state := range []string{"finished", "running"} {
		f.mustWrite(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
			_, err := tx.ExecContext(ctx, `DELETE FROM assignment`)
			if err != nil {
				return wire.Value{}, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO assignment (id, slot, room, plugin, invite_hash, state, deadline)
				SELECT 'a', participant.id, ?, 'fixture', 'hash', ?, ? FROM participant WHERE participant.label = 'Worker'`,
				room, state, startUnix+300)
			return wire.Value{}, err
		})
		if want := state == "running"; active() != want {
			t.Errorf("assignment %s: active = %v, want %v", state, !want, want)
		}
	}
}
