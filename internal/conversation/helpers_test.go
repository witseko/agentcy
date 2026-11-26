package conversation

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/store"
	"github.com/witseko/agentcy/internal/wire"
)

// testWorkspace is the workspace key of every test store.
var testWorkspace = strings.Repeat("ab", 32)

// startUnix is the fixed request time the tests start from.
const startUnix = int64(1_800_000_000)

// fixture is one test's data directory, store and usage book.
type fixture struct {
	t     *testing.T
	store *store.Store
	book  *usageBook
}

// newTestShared returns the shared resources of dataDir and closes their stores when
// the test ends.
func newTestShared(t *testing.T, dataDir string, logger *log.Logger) Shared {
	t.Helper()
	shared := NewShared(dataDir, logger)
	t.Cleanup(func() {
		if err := shared.Stores.Close(); err != nil {
			t.Error(err)
		}
	})
	return shared
}

// newFixture opens a new store in a private temp data directory.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	shared := newTestShared(t, t.TempDir(), log.New(io.Discard, "", 0))
	opened, err := shared.Stores.Open(context.Background(), testWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, store: opened, book: shared.newUsageBook()}
}

// write runs work in a write transaction and returns its result. A refusal is returned
// as its message; any other error fails the test.
func (f *fixture) write(work func(ctx context.Context, tx *sql.Tx) (wire.Value, error)) (wire.Value, string) {
	f.t.Helper()
	ctx := context.Background()
	var result wire.Value
	var refusal string
	err := f.store.Write(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = work(ctx, tx)
		return err
	})
	if err != nil {
		if !isRejection(err) {
			f.t.Fatalf("unexpected error: %v", err)
		}
		refusal = err.Error()
	}
	return result, refusal
}

// mustWrite runs work and fails the test on a refusal.
func (f *fixture) mustWrite(work func(ctx context.Context, tx *sql.Tx) (wire.Value, error)) wire.Value {
	f.t.Helper()
	result, refusal := f.write(work)
	if refusal != "" {
		f.t.Fatalf("refused: %s", refusal)
	}
	return result
}

// isRejection reports a refusal that the client may see.
func isRejection(err error) bool {
	var rejection *core.Rejection
	return errors.As(err, &rejection)
}

// createRoom creates a conversation led by "Lead" with the given invitees at now and
// returns the result.
func (f *fixture) createRoom(request wire.Create, now int64) wire.Value {
	f.t.Helper()
	request.Workspace = testWorkspace
	if request.Initiator == "" {
		request.Initiator = "Lead"
	}
	if request.Brief == "" {
		request.Brief = "Test brief"
	}
	return f.mustWrite(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		return create(ctx, tx, &request, now)
	})
}

// joinWith redeems a code at now and returns the result and any refusal.
func (f *fixture) joinWith(code string, now int64) (wire.Value, string) {
	f.t.Helper()
	return f.write(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		return join(ctx, tx, f.book, &wire.Join{Workspace: testWorkspace, Code: code}, now)
	})
}

// member authenticates a token without refreshing it.
func (f *fixture) member(token string) *participant {
	f.t.Helper()
	var member *participant
	f.mustWrite(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		var err error
		member, err = authenticate(ctx, tx, token)
		return wire.Value{}, err
	})
	return member
}

// post sends a note-like submission as the token's owner at now.
func (f *fixture) post(token string, submission submissionRequest, now int64) (wire.Value, string) {
	f.t.Helper()
	return f.write(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		member, err := refreshParticipant(ctx, tx, token, true, now)
		if err != nil {
			return wire.Value{}, err
		}
		return postInRound(ctx, writeScope{tx: tx, book: f.book, workspace: testWorkspace, now: now}, member, submission)
	})
}

// statusOf returns the status of the token's owner at now.
func (f *fixture) statusOf(token string, now int64) wire.Value {
	f.t.Helper()
	return f.mustWrite(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		member, err := refreshParticipant(ctx, tx, token, true, now)
		if err != nil {
			return wire.Value{}, err
		}
		current, err := status(ctx, tx, f.book, member, now)
		return current.value, err
	})
}

// text reads a string from a result.
func text(value wire.Value, keys ...string) string {
	for _, key := range keys {
		value, _ = value.Get(key)
	}
	return value.StringOf()
}

// field reads a nested value from a result.
func field(value wire.Value, keys ...string) wire.Value {
	for _, key := range keys {
		value, _ = value.Get(key)
	}
	return value
}
