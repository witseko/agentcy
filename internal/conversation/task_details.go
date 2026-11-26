package conversation

import (
	"context"
	"database/sql"
	"maps"
	"slices"

	"github.com/witseko/agentcy/internal/wire"
)

const selectTaskTimeline = `SELECT message.id, message.kind, message.author, participant.label AS author_label, message.recipient, recipient.label AS "to", message.body, message.created, message.reply_to, message.due_minutes 
FROM message JOIN participant ON message.author = participant.id LEFT OUTER JOIN participant AS recipient ON message.recipient = recipient.id 
WHERE message.room = ? AND (message.id IN (SELECT message.id 
FROM message 
WHERE message.room = ? AND message.kind = ?) OR message.reply_to IN (SELECT message.id 
FROM message 
WHERE message.room = ? AND message.kind = ?) OR message.reply_to IN (SELECT message.id 
FROM message 
WHERE message.room = ? AND message.kind = ? AND message.reply_to IN (SELECT message.id 
FROM message 
WHERE message.room = ? AND message.kind = ?))) ORDER BY message.id`

// timelineRow is one message of a task timeline.
type timelineRow struct {
	event       taskEvent
	authorLabel string
	to          sql.NullString
	body        string
}

// payload formats the row as a timeline event in Python's field order.
func (row timelineRow) payload() wire.Value {
	entry := wire.NewObject()
	entry.Set("id", wire.IntValue(row.event.id))
	entry.Set("author", wire.TextValue(row.authorLabel))
	entry.Set("body", wire.TextValue(row.body))
	entry.Set("created", wire.IntValue(row.event.created))
	if row.event.kind != "" {
		entry.Set("kind", wire.TextValue(row.event.kind))
	}
	if row.to.Valid {
		entry.Set("to", wire.TextValue(row.to.String))
	}
	if row.event.replyTo.Valid {
		entry.Set("reply_to", wire.IntValue(row.event.replyTo.Int64))
	}
	return entry
}

// taskTimelines collects the timeline rows of every task and resolves each row to its
// root task.
type taskTimelines struct {
	origins       map[int64]timelineRow
	events        map[int64][]wire.Value
	lastCreated   map[int64]int64
	handoffToTask map[int64]int64
	handoffTarget map[int64]string
	labels        map[int64]string
}

// resolve associates one row with its root task ID, or returns false.
func (timelines *taskTimelines) resolve(row timelineRow) (int64, bool) {
	if row.event.kind == kindTask {
		timelines.origins[row.event.id] = row
		return row.event.id, true
	}
	if !row.event.replyTo.Valid {
		return 0, false
	}
	parent := row.event.replyTo.Int64
	if _, isTask := timelines.origins[parent]; isTask {
		if row.event.kind == kindHandoff {
			timelines.handoffToTask[row.event.id] = parent
			if row.to.Valid {
				timelines.handoffTarget[row.event.id] = row.to.String
			}
		}
		return parent, true
	}
	root, found := timelines.handoffToTask[parent]
	return root, found
}

// stateLabel returns the external state name of one task.
func (state *task) stateLabel() string {
	switch {
	case state.deprecated:
		return "deprecated"
	case state.done:
		return "done"
	case state.blocked:
		return "blocked"
	}
	return "open"
}

// listTaskDetails returns every task of a conversation, newest first, with its owner,
// state, due time and event history (list_task_details in tasks.py).
func listTaskDetails(ctx context.Context, tx *sql.Tx, room string, now int64) (wire.Value, error) {
	recovery, err := roomRecoverySets(ctx, tx, room)
	if err != nil {
		return wire.Value{}, err
	}
	rows, err := tx.QueryContext(ctx, selectTaskTimeline, room, room, kindTask, room, kindTask, room, kindHandoff, room, kindTask)
	if err != nil {
		return wire.Value{}, err
	}
	defer rows.Close()
	timelines := &taskTimelines{
		origins: map[int64]timelineRow{}, events: map[int64][]wire.Value{}, lastCreated: map[int64]int64{},
		handoffToTask: map[int64]int64{}, handoffTarget: map[int64]string{}, labels: map[int64]string{},
	}
	board := newTaskBoard(recovery)
	for rows.Next() {
		row, err := scanTimelineRow(rows)
		if err != nil {
			return wire.Value{}, err
		}
		timelines.labels[row.event.author] = row.authorLabel
		if row.event.recipient.Valid && row.to.Valid {
			timelines.labels[row.event.recipient.Int64] = row.to.String
		}
		taskID, found := timelines.resolve(row)
		if !found {
			continue
		}
		timelines.events[taskID] = append(timelines.events[taskID], row.payload())
		timelines.lastCreated[taskID] = row.event.created
		if isTaskEventKind(row.event.kind) {
			board.apply(row.event)
		}
	}
	if err := rows.Err(); err != nil {
		return wire.Value{}, err
	}
	return taskDetailList(timelines, board, now), nil
}

// scanTimelineRow reads one row of selectTaskTimeline.
func scanTimelineRow(rows *sql.Rows) (timelineRow, error) {
	var row timelineRow
	var kind sql.NullString
	err := rows.Scan(&row.event.id, &kind, &row.event.author, &row.authorLabel, &row.event.recipient,
		&row.to, &row.body, &row.event.created, &row.event.replyTo, &row.event.dueMinutes)
	row.event.kind = kind.String
	return row, err
}

// taskDetailList builds the detail of every task, newest first.
func taskDetailList(timelines *taskTimelines, board *taskBoard, now int64) wire.Value {
	ids := slices.Sorted(maps.Keys(timelines.origins))
	slices.Reverse(ids)
	details := make([]wire.Value, 0, len(ids))
	for _, id := range ids {
		state := board.tasks[id]
		origin := timelines.origins[id]
		detail := wire.NewObject()
		detail.Set("id", wire.IntValue(id))
		detail.Set("title", wire.TextValue(taskTitle(origin.body)))
		detail.Set("body", wire.TextValue(origin.body))
		detail.Set("author", wire.TextValue(origin.authorLabel))
		detail.Set("owner", wire.TextValue(timelines.labels[state.owner.Int64]))
		detail.Set("state", wire.TextValue(state.stateLabel()))
		detail.Set("created", wire.IntValue(origin.event.created))
		detail.Set("updated", wire.IntValue(timelines.lastCreated[id]))
		detail.Set("events", wire.ListValue(timelines.events[id]))
		if target, found := timelines.handoffTarget[state.handoff]; state.handoff != 0 && found {
			detail.Set("pending_handoff_to", wire.TextValue(target))
		}
		if state.due.Valid {
			detail.Set("due", wire.IntValue(state.due.Int64))
		}
		if state.overdue(now) {
			detail.Set("overdue", wire.BoolValue(true))
		}
		details = append(details, detail)
	}
	return wire.ListValue(details)
}
