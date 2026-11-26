package conversation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/store"
	"github.com/witseko/agentcy/internal/wire"
)

// cacheHarness drives one Handler, whose room cache lives across requests, and compares
// each status and operator show with the answer of a new Handler that starts with an
// empty cache. Every comparison must be identical.
type cacheHarness struct {
	t         *testing.T
	ctx       context.Context
	dataDir   string
	directory string
	workspace string
	handler   *Handler
	now       int64
	room      string
	tokens    map[string]string
	posts     int
}

// newCacheHarness creates a data directory, a checkout directory without Git, and a
// conversation led by "Lead" with the given invitees, which all join.
func newCacheHarness(t *testing.T, create wire.Create, joiners ...string) *cacheHarness {
	t.Helper()
	h := &cacheHarness{
		t: t, ctx: context.Background(), dataDir: t.TempDir(), directory: t.TempDir(),
		now: startUnix, tokens: map[string]string{},
	}
	h.handler = h.newHandler()
	workspace, err := h.handler.checkoutWorkspace(h.directory)
	if err != nil {
		t.Fatal(err)
	}
	h.workspace = workspace
	create.Workspace, create.Brief, create.Initiator = workspace, "Cache brief", "Lead"
	created := h.member(&create)
	h.room = text(created, "conversation")
	h.tokens["Lead"] = text(created, "token")
	for _, name := range joiners {
		joined := h.member(&wire.Join{Workspace: workspace, Code: text(created, "invitations", name)})
		h.tokens[name] = text(joined, "token")
	}
	return h
}

// newHandler returns a Handler on the harness data directory with an empty cache.
func (h *cacheHarness) newHandler() *Handler {
	return NewHandler(h.dataDir, nil, log.New(io.Discard, "", 0), nil)
}

// member sends a member request through the cached Handler and fails on an error.
func (h *cacheHarness) member(request wire.Request) wire.Value {
	h.t.Helper()
	outcome, err := h.handler.Member(h.ctx, request, time.Unix(h.now, 0))
	if err != nil {
		h.t.Fatalf("%s: %v", request.Action(), err)
	}
	return outcome.Result
}

// operator sends an operator request through handler and fails on an error.
func (h *cacheHarness) operator(handler *Handler, request wire.OperatorRequest) wire.Value {
	h.t.Helper()
	outcome, err := handler.Operator(h.ctx, request, time.Unix(h.now, 0))
	if err != nil {
		h.t.Fatalf("%s: %v", request.Action(), err)
	}
	return outcome.Result
}

// conversationHeader names the harness conversation for an operator request.
func (h *cacheHarness) conversationHeader() wire.ConversationHeader {
	return wire.ConversationHeader{CheckoutHeader: wire.CheckoutHeader{Directory: h.directory}, Conversation: h.room}
}

// post sends a post as name.
func (h *cacheHarness) post(name string, post wire.Post) int64 {
	h.t.Helper()
	post.Workspace, post.Token = h.workspace, h.tokens[name]
	if post.SubmissionID == "" {
		h.posts++
		post.SubmissionID = fmt.Sprintf("submission-%d", h.posts)
	}
	if post.Body == "" {
		post.Body = "Body " + post.SubmissionID
	}
	if post.Kind == "" {
		post.Kind = "note"
	}
	result := h.member(&post)
	id, _ := field(result, "message_id").IntOf()
	return id
}

// views returns the status of every member and the operator show, as handler sees them.
func (h *cacheHarness) views(handler *Handler) string {
	h.t.Helper()
	text := ""
	for _, name := range []string{"Lead", "Worker A", "Worker B"} {
		token, found := h.tokens[name]
		if !found {
			continue
		}
		outcome, err := handler.Member(h.ctx, &wire.Status{Workspace: h.workspace, Token: token}, time.Unix(h.now, 0))
		if err != nil {
			h.t.Fatalf("status of %s: %v", name, err)
		}
		text += name + ": " + outcome.Result.Compact() + "\n"
	}
	show := h.operator(handler, &wire.OperatorShow{ConversationHeader: h.conversationHeader(), Tail: true})
	return text + "show: " + show.Compact()
}

// check compares the cached views with those of a Handler without a cache, twice, so
// the second round reads a cached snapshot without new messages. A first pass records
// every member's activity, so both views see the same idle times.
func (h *cacheHarness) check(step string) {
	h.t.Helper()
	h.views(h.newHandler())
	for round := range 2 {
		cached := h.views(h.handler)
		fresh := h.views(h.newHandler())
		if cached != fresh {
			h.t.Fatalf("%s (round %d): the cached views differ\ncached: %s\nfresh:  %s", step, round, cached, fresh)
		}
	}
}

// pointer returns a pointer to value.
func pointer[T any](value T) *T {
	return &value
}

// writeStore runs SQL on the store directly, as a writer outside the Handler would.
func (h *cacheHarness) writeStore(query string, arguments ...any) {
	h.t.Helper()
	opened, err := store.Open(h.ctx, h.dataDir, h.workspace)
	if err != nil {
		h.t.Fatal(err)
	}
	err = opened.Write(h.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(h.ctx, query, arguments...)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

// Port plan fix L4: after every kind of write, the cached task and activity state gives
// exactly the status and operator show that a cold Handler computes. Some writes here
// wake no room (operator archive with deprecated tasks, a fresh-run state change written
// straight to the store), so the cache must not depend on notifications.
func TestRoomCacheMatchesAColdHandlerAfterEveryWrite(t *testing.T) {
	h := newCacheHarness(t, wire.Create{Invitees: []wire.Slot{{Name: "Worker A"}, {Name: "Worker B"}}}, "Worker A", "Worker B")
	h.check("join")
	steps := []struct {
		name  string
		write func()
	}{
		{"note", func() { h.post("Worker A", wire.Post{}) }},
		{"task with due time", func() {
			h.post("Lead", wire.Post{Kind: "task", To: pointer("Worker A"), DueMinutes: pointer(int64(1))})
		}},
		{"blocker and resume", func() {
			task := h.lastTask("Worker A")
			h.post("Worker A", wire.Post{Kind: "blocker", ReplyTo: &task})
			h.now++
			h.post("Worker A", wire.Post{Kind: "resume", ReplyTo: &task})
		}},
		{"handoff and accept", func() {
			task := h.lastTask("Worker A")
			handoff := h.post("Worker A", wire.Post{Kind: "handoff", To: pointer("Worker B"), ReplyTo: &task})
			h.post("Worker B", wire.Post{Kind: "accept", ReplyTo: &handoff})
		}},
		{"handoff and decline", func() {
			task := h.lastTask("Worker B")
			handoff := h.post("Worker B", wire.Post{Kind: "handoff", To: pointer("Worker A"), ReplyTo: &task})
			h.post("Worker A", wire.Post{Kind: "decline", ReplyTo: &handoff})
		}},
		{"done", func() {
			task := h.lastTask("Worker B")
			h.post("Worker B", wire.Post{Kind: "done", ReplyTo: &task})
		}},
		{"done without a task", func() { h.post("Worker B", wire.Post{Kind: "done"}) }},
		{"second task", func() {
			h.post("Lead", wire.Post{Kind: "task", To: pointer("Worker B"), DueMinutes: pointer(int64(30))})
		}},
		{"set_role", func() {
			h.member(&wire.SetRole{Workspace: h.workspace, Token: h.tokens["Lead"], Name: "Worker A", Role: "co-lead"})
		}},
		{"retire with an open task", func() {
			h.member(&wire.Retire{Workspace: h.workspace, Token: h.tokens["Lead"], Name: "Worker B"})
		}},
		{"read", func() { h.member(&wire.Read{Workspace: h.workspace, Token: h.tokens["Worker A"]}) }},
		{"report_usage", func() {
			h.member(&wire.ReportUsage{Workspace: h.workspace, Token: h.tokens["Worker A"], InputTokens: 10, OutputTokens: 5})
		}},
		{"operator post", func() {
			h.operator(h.handler, &wire.OperatorPost{ConversationHeader: h.conversationHeader(), SubmissionID: "human-1", Body: "Human note", Kind: "note"})
		}},
		{"operator set_role", func() {
			h.operator(h.handler, &wire.OperatorSetRole{ConversationHeader: h.conversationHeader(), Name: "Worker A", Role: "worker"})
		}},
		{"operator invite", func() {
			h.operator(h.handler, &wire.OperatorInvite{ConversationHeader: h.conversationHeader(), Name: "Worker C"})
		}},
		{"fresh run starts, outside the Handler", func() {
			h.writeStore(`INSERT INTO assignment (id, slot, room, plugin, invite_hash, state, deadline)
				SELECT 'run-1', participant.id, participant.room, 'fixture', participant.invite_hash, 'running', ?
				FROM participant WHERE participant.label = 'Worker C'`, h.now+300)
		}},
		{"fresh run ends, outside the Handler", func() {
			h.writeStore(`UPDATE assignment SET state = 'finished' WHERE id = 'run-1'`)
		}},
		{"task for the ended run and a lead's done", func() {
			task := h.post("Lead", wire.Post{Kind: "task", To: pointer("Worker A"), DueMinutes: pointer(int64(30))})
			h.writeStore(`UPDATE assignment SET slot = (SELECT id FROM participant WHERE label = 'Worker A') WHERE id = 'run-1'`)
			h.post("Lead", wire.Post{Kind: "done", ReplyTo: &task})
		}},
		{"lead's done on an active member's task, which the replay ignores", func() {
			task := h.post("Lead", wire.Post{Kind: "task", To: pointer("Worker A"), DueMinutes: pointer(int64(30))})
			h.writeStore(`INSERT INTO message (room, author, body, created, kind, reply_to)
				SELECT room, id, 'Lead finishes it', ?, 'done', ? FROM participant WHERE label = 'Lead'`, h.now, task)
		}},
		{"the member's fresh run ends without a message, so the replay applies the done", func() {
			h.writeStore(`UPDATE assignment SET invite_hash = (SELECT invite_hash FROM participant WHERE label = 'Worker A')
				WHERE id = 'run-1'`)
		}},
		{"close", func() {
			h.member(&wire.Close{Workspace: h.workspace, Token: h.tokens["Lead"], Summary: pointer("Closed")})
		}},
		{"reopen", func() {
			h.member(&wire.Reopen{Workspace: h.workspace, Token: h.tokens["Lead"], Note: pointer("Again")})
		}},
		{"operator close with deprecated tasks", func() {
			h.post("Lead", wire.Post{Kind: "task", To: pointer("Worker A"), DueMinutes: pointer(int64(30))})
			h.operator(h.handler, &wire.OperatorClose{ConversationHeader: h.conversationHeader(), DeprecateTasks: true})
		}},
		{"operator reopen", func() {
			h.operator(h.handler, &wire.OperatorReopen{ConversationHeader: h.conversationHeader()})
		}},
		{"operator archive with deprecated tasks", func() {
			h.post("Lead", wire.Post{Kind: "task", To: pointer("Worker A"), DueMinutes: pointer(int64(30))})
			h.member(&wire.Close{Workspace: h.workspace, Token: h.tokens["Lead"]})
			h.operator(h.handler, &wire.OperatorArchive{ConversationHeader: h.conversationHeader(), Archived: true, DeprecateTasks: true})
		}},
	}
	for _, step := range steps {
		h.now += 7
		step.write()
		h.check(step.name)
	}
	if h.handler.rooms.size() != 1 {
		t.Errorf("cached rooms = %d, want 1", h.handler.rooms.size())
	}
}

// lastTask returns the newest unfinished task of owner as status shows it.
func (h *cacheHarness) lastTask(owner string) int64 {
	h.t.Helper()
	outcome, err := h.handler.Member(h.ctx, &wire.Status{Workspace: h.workspace, Token: h.tokens["Lead"]}, time.Unix(h.now, 0))
	if err != nil {
		h.t.Fatal(err)
	}
	tasks := field(outcome.Result, "members", owner, "open_tasks")
	if tasks.Len() == 0 {
		h.t.Fatalf("%s has no open task", owner)
	}
	id, _ := field(tasks.Items[tasks.Len()-1], "id").IntOf()
	return id
}

// A sealed round released by the lead, and one released by its deadline passing, also
// leave the cache in step.
func TestRoomCacheFollowsSealedRoundReleases(t *testing.T) {
	create := wire.Create{Invitees: []wire.Slot{{Name: "Worker A"}, {Name: "Worker B"}}, Sealed: true, DeadlineMinutes: pointer(int64(5))}
	h := newCacheHarness(t, create, "Worker A", "Worker B")
	h.check("sealed join")
	h.post("Worker A", wire.Post{Kind: "proposal"})
	h.check("proposal")
	h.member(&wire.Release{Workspace: h.workspace, Token: h.tokens["Lead"]})
	h.check("release")

	timed := newCacheHarness(t, create, "Worker A", "Worker B")
	timed.post("Worker A", wire.Post{Kind: "proposal"})
	timed.check("proposal before the deadline")
	timed.now += 6 * 60
	timed.check("after the deadline")
}

// Overdue flags and idle times come from the request time, so a cached room with no new
// message shows a task as overdue once its due time passes.
func TestRoomCacheDerivesTimeFieldsPerCall(t *testing.T) {
	h := newCacheHarness(t, wire.Create{Invitees: []wire.Slot{{Name: "Worker A"}}}, "Worker A")
	h.post("Lead", wire.Post{Kind: "task", To: pointer("Worker A"), DueMinutes: pointer(int64(1))})
	h.check("task")
	before := h.handler.rooms.builds.Load() + h.handler.rooms.extensions.Load()
	overdue := func() bool {
		outcome, err := h.handler.Member(h.ctx, &wire.Status{Workspace: h.workspace, Token: h.tokens["Lead"]}, time.Unix(h.now, 0))
		if err != nil {
			t.Fatal(err)
		}
		return field(outcome.Result, "members", "Worker A", "open_tasks").Items[0].Has("overdue")
	}
	if overdue() {
		t.Fatal("the task is overdue before its due time")
	}
	h.now += 61
	if !overdue() {
		t.Error("a cached room did not show the task as overdue after its due time")
	}
	h.check("after the due time")
	if after := h.handler.rooms.builds.Load() + h.handler.rooms.extensions.Load(); after != before {
		t.Errorf("status replayed messages %d times without a write", after-before)
	}
}

// A done post reads the author's open tasks before and after its insert. With a warm
// cache neither read replays the room: the second applies only the new message.
func TestDonePostReplaysOnlyTheNewMessage(t *testing.T) {
	h := newCacheHarness(t, wire.Create{Invitees: []wire.Slot{{Name: "Worker A"}}}, "Worker A")
	task := h.post("Lead", wire.Post{Kind: "task", To: pointer("Worker A"), DueMinutes: pointer(int64(30))})
	h.check("task")
	builds, extensions := h.handler.rooms.builds.Load(), h.handler.rooms.extensions.Load()
	h.post("Worker A", wire.Post{Kind: "done", ReplyTo: &task})
	if got := h.handler.rooms.builds.Load() - builds; got != 0 {
		t.Errorf("the done post replayed the whole room %d times", got)
	}
	if got := h.handler.rooms.extensions.Load() - extensions; got != 1 {
		t.Errorf("the done post applied new messages %d times, want 1", got)
	}
	h.check("done")
}

// A snapshot built inside a transaction that rolls back never reaches the cache, because
// it may count a message that was never committed.
func TestRoomCacheIgnoresRolledBackTransactions(t *testing.T) {
	h := newCacheHarness(t, wire.Create{Invitees: []wire.Slot{{Name: "Worker A"}}}, "Worker A")
	h.check("join")
	failed := errors.New("fixture failure")
	_, err := h.handler.writeResult(h.ctx, h.workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		if err := insertNote(ctx, tx, h.room, 1, "Never committed", h.now); err != nil {
			return wire.Value{}, err
		}
		if _, err := roomState(ctx, tx, h.room); err != nil {
			return wire.Value{}, err
		}
		return wire.Value{}, failed
	})
	if !errors.Is(err, failed) {
		t.Fatalf("err = %v, want the fixture failure", err)
	}
	h.post("Worker A", wire.Post{Body: "Committed in place of the rolled back note"})
	h.check("after the rollback")
}

// The cache holds at most roomCacheLimit rooms and drops the least recently used one.
func TestRoomCacheDropsTheLeastRecentlyUsedRoom(t *testing.T) {
	cache := newRoomCache()
	snapshot := &roomSnapshot{board: newTaskBoard(recoverySets{})}
	for index := range roomCacheLimit + 5 {
		cache.store(roomCacheKey{store: "store", room: fmt.Sprint(index)}, snapshot, recoverySets{})
		cache.lookup(roomCacheKey{store: "store", room: "0"})
	}
	if cache.size() != roomCacheLimit {
		t.Errorf("size = %d, want %d", cache.size(), roomCacheLimit)
	}
	if cache.lookup(roomCacheKey{store: "store", room: "0"}) == nil {
		t.Error("the recently used room was dropped")
	}
	if cache.lookup(roomCacheKey{store: "store", room: "1"}) != nil {
		t.Error("the least recently used room was kept")
	}
}
