package conversation

import (
	"context"
	"database/sql"
	"sort"

	"github.com/witseko/agentcy/internal/wire"
)

// The task message kinds (tasks.py). Only the core writes reassign, when it moves a
// retired member's tasks, and deprecate, when the human drops unfinished tasks.
const (
	kindTask      = "task"
	kindHandoff   = "handoff"
	kindDone      = "done"
	kindBlocker   = "blocker"
	kindResume    = "resume"
	kindAccept    = "accept"
	kindDecline   = "decline"
	kindReassign  = "reassign"
	kindDeprecate = "deprecate"
)

// taskEventKinds are the kinds that change task state, in the order Python binds them.
var taskEventKinds = []string{kindTask, kindHandoff, kindDone, kindBlocker, kindResume, kindReassign, kindDeprecate, kindAccept, kindDecline}

// isTaskEventKind reports a kind that changes task state.
func isTaskEventKind(kind string) bool {
	for _, candidate := range taskEventKinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

// task is the current state of one task, built from its messages in order.
type task struct {
	owner      sql.NullInt64
	blocked    bool
	done       bool
	deprecated bool
	// handoff is the message ID of the pending handoff offer, zero without one.
	handoff int64
	due     sql.NullInt64
}

// taskEvent is one message that may change task state.
type taskEvent struct {
	id         int64
	kind       string
	author     int64
	recipient  sql.NullInt64
	replyTo    sql.NullInt64
	created    int64
	dueMinutes sql.NullInt64
}

// overdue recognizes an unfinished task whose due time has passed.
func (state *task) overdue(now int64) bool {
	ended := state.done || state.deprecated
	return !ended && state.due.Valid && state.due.Int64 < now
}

// recoverySets are the leader IDs and the inactive (retired or ended fresh-run) member
// IDs of a room, which decide whether a lead may act on another member's task.
type recoverySets struct {
	leaders  map[int64]bool
	inactive map[int64]bool
}

// taskBoard replays task events in message order (apply_event in tasks.py). tasks keeps
// the insertion order in order, as the Python dict does.
type taskBoard struct {
	tasks    map[int64]*task
	order    []int64
	offers   map[int64]*task
	recovery recoverySets
}

// newTaskBoard returns an empty board that uses recovery for its ownership checks.
func newTaskBoard(recovery recoverySets) *taskBoard {
	return &taskBoard{tasks: map[int64]*task{}, offers: map[int64]*task{}, recovery: recovery}
}

// apply applies one event.
func (board *taskBoard) apply(event taskEvent) {
	switch event.kind {
	case kindTask:
		if _, exists := board.tasks[event.id]; !exists {
			board.order = append(board.order, event.id)
		}
		state := &task{owner: event.recipient}
		if event.dueMinutes.Valid {
			state.due = sql.NullInt64{Int64: event.created + event.dueMinutes.Int64*60, Valid: true}
		}
		board.tasks[event.id] = state
		return
	case kindAccept, kindDecline:
		board.applyResponse(event)
		return
	}
	state := board.lookup(event.replyTo)
	switch event.kind {
	case kindReassign:
		board.applyReassign(state, event)
	case kindDeprecate:
		board.applyDeprecate(state)
	default:
		board.applyOwnerEvent(state, event)
	}
}

// lookup returns the task a reply targets, or nil.
func (board *taskBoard) lookup(replyTo sql.NullInt64) *task {
	if !replyTo.Valid {
		return nil
	}
	return board.tasks[replyTo.Int64]
}

// applyResponse resolves an accept or decline reply against a pending handoff offer.
func (board *taskBoard) applyResponse(event taskEvent) {
	if !event.replyTo.Valid {
		return
	}
	state, found := board.offers[event.replyTo.Int64]
	if !found {
		return
	}
	delete(board.offers, event.replyTo.Int64)
	if event.kind == kindAccept {
		state.owner = sql.NullInt64{Int64: event.author, Valid: true}
		state.blocked = false
	}
	state.handoff = 0
}

// applyReassign gives an open task to its new owner and drops any pending handoff and
// blocker.
func (board *taskBoard) applyReassign(state *task, event taskEvent) {
	if state == nil || state.done {
		return
	}
	state.owner = event.recipient
	state.blocked = false
	if state.handoff != 0 {
		delete(board.offers, state.handoff)
		state.handoff = 0
	}
}

// finish ends a task and drops its pending handoff offer.
func (board *taskBoard) finish(state *task) {
	state.done = true
	if state.handoff != 0 {
		delete(board.offers, state.handoff)
		state.handoff = 0
	}
}

// applyDeprecate ends an unfinished task as deprecated. A finished task keeps its state.
func (board *taskBoard) applyDeprecate(state *task) {
	if state == nil || state.done {
		return
	}
	state.deprecated = true
	board.finish(state)
}

// canModify allows the owner, or a lead recovering an inactive owner's task with a
// handoff or done, to change task state.
func (board *taskBoard) canModify(state *task, event taskEvent) bool {
	if state.owner.Valid && state.owner.Int64 == event.author {
		return true
	}
	ownerInactive := state.owner.Valid && board.recovery.inactive[state.owner.Int64]
	return (event.kind == kindHandoff || event.kind == kindDone) &&
		board.recovery.leaders[event.author] && ownerInactive
}

// applyOwnerEvent applies a handoff, blocker, resume or done from the owner.
func (board *taskBoard) applyOwnerEvent(state *task, event taskEvent) {
	if state == nil || state.done || !board.canModify(state, event) {
		return
	}
	switch event.kind {
	case kindHandoff:
		state.handoff = event.id
		board.offers[event.id] = state
	case kindBlocker, kindResume:
		state.blocked = event.kind == kindBlocker
	case kindDone:
		board.finish(state)
	}
}

const selectRecoveryRows = `SELECT participant.id, participant.role, participant.retired, assignment.state AS launch 
FROM participant LEFT OUTER JOIN assignment ON assignment.slot = participant.id AND assignment.invite_hash = participant.invite_hash 
WHERE participant.room = ?`

// roomRecoverySets returns the leader IDs and inactive member IDs of a room.
func roomRecoverySets(ctx context.Context, tx *sql.Tx, room string) (recoverySets, error) {
	sets := recoverySets{leaders: map[int64]bool{}, inactive: map[int64]bool{}}
	rows, err := tx.QueryContext(ctx, selectRecoveryRows, room)
	if err != nil {
		return sets, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, role int64
		var retired bool
		var launch sql.NullString
		if err := rows.Scan(&id, &role, &retired, &launch); err != nil {
			return sets, err
		}
		if isLeader(role) && !retired {
			sets.leaders[id] = true
		}
		if retired || launchEnded(launch) {
			sets.inactive[id] = true
		}
	}
	return sets, rows.Err()
}

// pendingStates are the assignment states of a fresh run that is still active.
var pendingStates = []string{"queued", "starting", "running"}

// isPendingState reports an active fresh-run state.
func isPendingState(state string) bool {
	for _, pending := range pendingStates {
		if pending == state {
			return true
		}
	}
	return false
}

// launchEnded reports a fresh run that the bridge no longer tracks as active.
func launchEnded(launch sql.NullString) bool {
	return launch.Valid && !isPendingState(launch.String)
}

const selectRoomTaskEvents = `SELECT message.id, message.kind, message.author, message.recipient, message.reply_to, message.created, message.due_minutes 
FROM message 
WHERE message.room = ? AND message.kind IN (?, ?, ?, ?, ?, ?, ?, ?, ?) AND (message.kind = ? OR message.reply_to IS NOT NULL)`

const selectOneTaskEvents = selectRoomTaskEvents + ` AND (message.id = ? OR message.reply_to = ? OR message.reply_to IN (SELECT message.id 
FROM message 
WHERE message.room = ? AND message.kind = ? AND message.reply_to = ?))`

// readTasks replays the task events of a room, or only those of one task when taskID is
// above zero, and returns the board (read_tasks in tasks.py). Port plan fix L4 caches
// this per room later; keep every caller going through this one function.
func readTasks(ctx context.Context, tx *sql.Tx, room string, taskID int64) (*taskBoard, error) {
	query := selectRoomTaskEvents
	arguments := []any{room}
	for _, kind := range taskEventKinds {
		arguments = append(arguments, kind)
	}
	arguments = append(arguments, kindTask)
	if taskID > 0 {
		query = selectOneTaskEvents
		arguments = append(arguments, taskID, taskID, room, kindHandoff, taskID)
	}
	recovery, err := roomRecoverySets(ctx, tx, room)
	if err != nil {
		return nil, err
	}
	events, err := queryTaskEvents(ctx, tx, query, arguments...)
	if err != nil {
		return nil, err
	}
	// SQLite reads every message of the conversation when the query orders by ID, and
	// only the task events through the message_room_kind index when it does not.
	sort.Slice(events, func(left, right int) bool { return events[left].id < events[right].id })
	board := newTaskBoard(recovery)
	for _, event := range events {
		board.apply(event)
	}
	return board, nil
}

// queryTaskEvents runs a query that selects taskEvent columns.
func queryTaskEvents(ctx context.Context, tx *sql.Tx, query string, arguments ...any) ([]taskEvent, error) {
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []taskEvent
	for rows.Next() {
		var event taskEvent
		var kind sql.NullString
		if err := rows.Scan(&event.id, &kind, &event.author, &event.recipient, &event.replyTo, &event.created, &event.dueMinutes); err != nil {
			return nil, err
		}
		event.kind = kind.String
		events = append(events, event)
	}
	return events, rows.Err()
}

// taskSummary is one unfinished task of an owner, as status shows it.
type taskSummary struct {
	id      int64
	blocked bool
	due     sql.NullInt64
	overdue bool
}

// value returns the summary in Python's field order.
func (summary taskSummary) value() wire.Value {
	entry := wire.NewObject()
	entry.Set("id", wire.IntValue(summary.id))
	if summary.blocked {
		entry.Set("blocked", wire.BoolValue(true))
	}
	if summary.due.Valid {
		entry.Set("due", wire.IntValue(summary.due.Int64))
	}
	if summary.overdue {
		entry.Set("overdue", wire.BoolValue(true))
	}
	return entry
}

// openTasks groups the unfinished tasks of a room by current owner, oldest first, with
// blocker, due and overdue flags (open_tasks in tasks.py). Tasks without an owner are
// grouped under owner zero.
func openTasks(ctx context.Context, tx *sql.Tx, room string, now int64) (map[int64][]taskSummary, error) {
	board, err := readTasks(ctx, tx, room, 0)
	if err != nil {
		return nil, err
	}
	byOwner := map[int64][]taskSummary{}
	for _, id := range board.order {
		state := board.tasks[id]
		if state.done {
			continue
		}
		summary := taskSummary{id: id, blocked: state.blocked, due: state.due, overdue: state.overdue(now)}
		byOwner[state.owner.Int64] = append(byOwner[state.owner.Int64], summary)
	}
	return byOwner, nil
}

// taskSummaryList returns summaries as a JSON list.
func taskSummaryList(summaries []taskSummary) wire.Value {
	items := make([]wire.Value, 0, len(summaries))
	for _, summary := range summaries {
		items = append(items, summary.value())
	}
	return wire.ListValue(items)
}
