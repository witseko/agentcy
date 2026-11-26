package conversation

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
)

// roomCacheLimit bounds the rooms whose derived state the daemon keeps. The least
// recently used room is dropped first, so idle and closed rooms leave the cache.
const roomCacheLimit = 32

// roomSnapshot is the state that status derives from all messages of one room up to and
// including message lastMessage (port plan fix L4): the unfinished tasks and each
// author's activity. It holds nothing that depends on the clock: overdue flags and idle
// times are computed per call. A snapshot is immutable once built.
type roomSnapshot struct {
	lastMessage int64
	// messages counts every message of the room, lifecycle events included.
	messages int64
	// board holds only unfinished tasks. A finished task never changes again, and every
	// reader skips finished tasks, so dropping them keeps copies small.
	board    *taskBoard
	activity map[int64]authorActivity
}

// roomCache keeps one roomSnapshot per room of every store the daemon opened. mutex
// guards entries and clock. Snapshots are immutable, so a reader may use one after the
// mutex is released.
//
// A snapshot is valid for exactly one store state: the room's highest message ID and its
// recovery sets (leaders and inactive members, which the task replay depends on). Every
// use checks both against the store inside the caller's transaction, so a snapshot is
// never trusted only because no write was announced. This relies on messages being
// append-only: rows are never updated or deleted, and a committed message always gets a
// higher ID than every committed message before it. The daemon is the only process that
// writes stores, but the check would also catch another writer that appends.
type roomCache struct {
	mutex   sync.Mutex
	entries map[roomCacheKey]*roomCacheEntry
	clock   uint64
	// builds and extensions count full replays and replays of new messages only.
	builds     atomic.Int64
	extensions atomic.Int64
}

// roomCacheKey names one room of one store file.
type roomCacheKey struct {
	store string
	room  string
}

// roomCacheEntry is a cached snapshot with the recovery sets it was built for and its
// last use, for eviction.
type roomCacheEntry struct {
	snapshot *roomSnapshot
	recovery recoverySets
	used     uint64
}

// newRoomCache returns an empty cache.
func newRoomCache() *roomCache {
	return &roomCache{entries: map[roomCacheKey]*roomCacheEntry{}}
}

// lookup returns the cached entry of a room, or nil.
func (cache *roomCache) lookup(key roomCacheKey) *roomCacheEntry {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	entry, found := cache.entries[key]
	if !found {
		return nil
	}
	cache.clock++
	entry.used = cache.clock
	return &roomCacheEntry{snapshot: entry.snapshot, recovery: entry.recovery}
}

// store keeps a snapshot unless the cache already holds a later one for the room, and
// drops the least recently used room when the cache is full.
func (cache *roomCache) store(key roomCacheKey, snapshot *roomSnapshot, recovery recoverySets) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if current, found := cache.entries[key]; found && current.snapshot.lastMessage > snapshot.lastMessage {
		return
	}
	cache.clock++
	cache.entries[key] = &roomCacheEntry{snapshot: snapshot, recovery: recovery, used: cache.clock}
	if len(cache.entries) <= roomCacheLimit {
		return
	}
	var oldest roomCacheKey
	oldestUse := cache.clock + 1
	for candidate, entry := range cache.entries {
		if entry.used < oldestUse {
			oldest, oldestUse = candidate, entry.used
		}
	}
	delete(cache.entries, oldest)
}

// size returns the number of cached rooms.
func (cache *roomCache) size() int {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	return len(cache.entries)
}

// transactionRooms is the cache as one transaction sees it. Snapshots built inside the
// transaction may include its uncommitted writes, so they wait in pending and reach the
// shared cache only after a commit. It is used by one goroutine.
type transactionRooms struct {
	cache   *roomCache
	store   string
	pending map[string]pendingSnapshot
}

// pendingSnapshot is a snapshot built inside a transaction that has not committed yet.
type pendingSnapshot struct {
	snapshot *roomSnapshot
	recovery recoverySets
}

// transactionRoomsKey is the context key of the transaction's cache view.
type transactionRoomsKey struct{}

// withRoomCache returns ctx carrying a cache view for one transaction on the store file
// storePath, and the view, whose publish must be called after a successful commit.
func withRoomCache(ctx context.Context, cache *roomCache, storePath string) (context.Context, *transactionRooms) {
	view := &transactionRooms{cache: cache, store: storePath, pending: map[string]pendingSnapshot{}}
	return context.WithValue(ctx, transactionRoomsKey{}, view), view
}

// lookup returns the snapshot this transaction built for a room, or else the shared
// one, or nil.
func (view *transactionRooms) lookup(room string) *roomCacheEntry {
	if pending, found := view.pending[room]; found {
		return &roomCacheEntry{snapshot: pending.snapshot, recovery: pending.recovery}
	}
	return view.cache.lookup(roomCacheKey{store: view.store, room: room})
}

// publish moves the snapshots of a committed transaction into the shared cache.
func (view *transactionRooms) publish() {
	for room, pending := range view.pending {
		view.cache.store(roomCacheKey{store: view.store, room: room}, pending.snapshot, pending.recovery)
	}
}

// roomState returns the snapshot of a room as the transaction sees it. Without a cache
// view in ctx (tests, the assignment store) it builds the snapshot from scratch, as the
// Python core does on every call. With a view it reuses a cached snapshot when the room
// has no new message and the same recovery sets, and otherwise replays only the messages
// after the cached one.
func roomState(ctx context.Context, tx *sql.Tx, room string) (*roomSnapshot, error) {
	recovery, err := roomRecoverySets(ctx, tx, room)
	if err != nil {
		return nil, err
	}
	lastMessage, err := lastRoomMessage(ctx, tx, room)
	if err != nil {
		return nil, err
	}
	view, _ := ctx.Value(transactionRoomsKey{}).(*transactionRooms)
	if view == nil {
		return buildRoomSnapshot(ctx, tx, room, lastMessage)
	}
	cached := view.lookup(room)
	var snapshot *roomSnapshot
	switch {
	case cached == nil || !cached.recovery.equal(recovery) || cached.snapshot.lastMessage > lastMessage:
		view.cache.builds.Add(1)
		snapshot, err = buildRoomSnapshot(ctx, tx, room, lastMessage)
	case cached.snapshot.lastMessage == lastMessage:
		return cached.snapshot, nil
	default:
		view.cache.extensions.Add(1)
		snapshot, err = extendRoomSnapshot(ctx, tx, room, cached.snapshot, recovery)
	}
	if err != nil {
		return nil, err
	}
	view.pending[room] = pendingSnapshot{snapshot: snapshot, recovery: recovery}
	return snapshot, nil
}

// selectLastRoomMessage is a bare max() so SQLite answers it from one entry of the
// message_room_cursor index.
const selectLastRoomMessage = `SELECT max(message.id) FROM message WHERE message.room = ?`

// lastRoomMessage returns the highest message ID of a room, or zero without messages.
func lastRoomMessage(ctx context.Context, tx *sql.Tx, room string) (int64, error) {
	var lastMessage sql.NullInt64
	err := tx.QueryRowContext(ctx, selectLastRoomMessage, room).Scan(&lastMessage)
	return lastMessage.Int64, err
}

// buildRoomSnapshot replays every task event of a room and counts every author's posts.
// lastMessage must come from the same transaction, so all three agree.
func buildRoomSnapshot(ctx context.Context, tx *sql.Tx, room string, lastMessage int64) (*roomSnapshot, error) {
	board, err := readTasks(ctx, tx, room, 0)
	if err != nil {
		return nil, err
	}
	board.dropFinished()
	activity, err := memberActivity(ctx, tx, room)
	if err != nil {
		return nil, err
	}
	messages, err := firstInteger(ctx, tx, selectRoomMessageCount, room)
	if err != nil {
		return nil, err
	}
	return &roomSnapshot{lastMessage: lastMessage, messages: messages.Int64, board: board, activity: activity}, nil
}

const selectRoomMessageCount = "SELECT count(*) AS count_1 \nFROM message \nWHERE message.room = ?"

const selectMessagesAfter = `SELECT message.id, message.kind, message.author, message.recipient, message.reply_to, message.created, message.due_minutes
FROM message
WHERE message.room = ? AND message.id > ? ORDER BY message.id`

// extendRoomSnapshot applies the messages after a cached snapshot to a copy of it. The
// query reads the message_room_cursor index from the cached message on, so its cost
// grows with the new messages only.
func extendRoomSnapshot(ctx context.Context, tx *sql.Tx, room string, cached *roomSnapshot, recovery recoverySets) (*roomSnapshot, error) {
	messages, err := queryTaskEvents(ctx, tx, selectMessagesAfter, room, cached.lastMessage)
	if err != nil {
		return nil, err
	}
	snapshot := &roomSnapshot{
		lastMessage: cached.lastMessage,
		messages:    cached.messages + int64(len(messages)),
		board:       cached.board.clone(recovery),
		activity:    maps.Clone(cached.activity),
	}
	for _, message := range messages {
		snapshot.lastMessage = message.id
		countActivity(snapshot.activity, message)
		if isTaskEvent(message) {
			snapshot.board.apply(message)
		}
	}
	snapshot.board.dropFinished()
	return snapshot, nil
}

// isTaskEvent applies the filter of selectRoomTaskEvents to one message: a task, or a
// reply of another task event kind.
func isTaskEvent(message taskEvent) bool {
	return isTaskEventKind(message.kind) && (message.kind == kindTask || message.replyTo.Valid)
}

// countActivity adds one message to the activity of its author, as memberActivity counts
// it: lifecycle events are not posts, and the latest progress post sets the marker.
func countActivity(activity map[int64]authorActivity, message taskEvent) {
	if message.kind == eventKind {
		return
	}
	entry := activity[message.author]
	entry.count++
	entry.latestID = max(entry.latestID, message.id)
	if slices.Contains(progressKinds, message.kind) && message.id > entry.markerID {
		entry.marker = message.kind
		entry.markerID = message.id
	}
	activity[message.author] = entry
}

// equal reports whether two recovery sets name the same leaders and inactive members.
func (sets recoverySets) equal(other recoverySets) bool {
	return maps.Equal(sets.leaders, other.leaders) && maps.Equal(sets.inactive, other.inactive)
}

// clone returns a deep copy of the board that uses recovery for later events.
func (board *taskBoard) clone(recovery recoverySets) *taskBoard {
	copied := newTaskBoard(recovery)
	copied.order = slices.Clone(board.order)
	copies := make(map[*task]*task, len(board.tasks))
	for id, state := range board.tasks {
		duplicate := *state
		copied.tasks[id] = &duplicate
		copies[state] = &duplicate
	}
	for id, state := range board.offers {
		copied.offers[id] = copies[state]
	}
	return copied
}

// dropFinished removes finished tasks. Later events cannot change a finished task: every
// rule ignores an event for a missing task the same way it ignores one for a finished
// task, and finishing a task already removed its pending handoff offer.
func (board *taskBoard) dropFinished() {
	board.order = slices.DeleteFunc(board.order, func(id int64) bool {
		if board.tasks[id].done {
			delete(board.tasks, id)
			return true
		}
		return false
	})
}
