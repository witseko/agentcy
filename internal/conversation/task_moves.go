package conversation

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// availableWorker is an active worker who can take over tasks.
type availableWorker struct {
	id    int64
	label string
}

const selectWorkerCandidates = `SELECT participant.id, participant.label, participant.token_hash, participant.expires, assignment.state AS launch 
FROM participant LEFT OUTER JOIN assignment ON assignment.slot = participant.id AND assignment.invite_hash = participant.invite_hash 
WHERE participant.room = ? AND participant.role = ? AND participant.retired = ? ORDER BY participant.id`

// availableWorkers returns the active workers that joined, hold a live invitation, or
// have a running launch.
func availableWorkers(ctx context.Context, tx *sql.Tx, room string, now int64) ([]availableWorker, error) {
	rows, err := tx.QueryContext(ctx, selectWorkerCandidates, room, roleWorker, 0)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var workers []availableWorker
	for rows.Next() {
		var worker availableWorker
		var tokenHash, launch sql.NullString
		var expires sql.NullInt64
		if err := rows.Scan(&worker.id, &worker.label, &tokenHash, &expires, &launch); err != nil {
			return nil, err
		}
		if launchEnded(launch) {
			continue
		}
		if !tokenHash.Valid && !launch.Valid && expires.Int64 <= now {
			continue
		}
		workers = append(workers, worker)
	}
	return workers, rows.Err()
}

const selectActiveLeaders = `SELECT participant.id, participant.label, participant.role 
FROM participant 
WHERE participant.room = ? AND participant.role IN (?, ?) AND participant.retired = ? ORDER BY participant.id`

// fallbackLeader returns the active lead, else the acting co-lead, else the first active
// co-lead.
func fallbackLeader(ctx context.Context, tx *sql.Tx, actor *participant) (availableWorker, error) {
	rows, err := tx.QueryContext(ctx, selectActiveLeaders, actor.Room, roleLead, roleCoLead, 0)
	if err != nil {
		return availableWorker{}, err
	}
	var leaders []roomSlot
	for rows.Next() {
		var leader roomSlot
		if err := rows.Scan(&leader.id, &leader.label, &leader.role); err != nil {
			rows.Close()
			return availableWorker{}, err
		}
		leaders = append(leaders, leader)
	}
	if err := closeRows(rows); err != nil {
		return availableWorker{}, err
	}
	for _, leader := range leaders {
		if leader.role == roleLead {
			return availableWorker{id: leader.id, label: leader.label}, nil
		}
	}
	for _, leader := range leaders {
		if leader.id == actor.ID {
			return availableWorker{id: leader.id, label: leader.label}, nil
		}
	}
	if len(leaders) == 0 {
		return availableWorker{}, reject("No active lead can take over the retired participant's tasks")
	}
	return availableWorker{id: leaders[0].id, label: leaders[0].label}, nil
}

const insertReassign = `INSERT INTO message (room, author, body, created, kind, recipient, reply_to) VALUES (?, ?, ?, ?, ?, ?, ?)`

// redelegateTasks moves a retired member's unfinished tasks to the least busy available
// worker, or to the lead when no worker is available. Each move is a reassign message
// addressed to the new owner, so the new owner wakes.
func redelegateTasks(ctx context.Context, tx *sql.Tx, actor *participant, retiredID int64, retiredLabel string, now int64) error {
	byOwner, err := openTasks(ctx, tx, actor.Room, now)
	if err != nil {
		return err
	}
	owned := byOwner[retiredID]
	if len(owned) == 0 {
		return nil
	}
	workers, err := availableWorkers(ctx, tx, actor.Room, now)
	if err != nil {
		return err
	}
	load := map[int64]int{}
	for _, worker := range workers {
		load[worker.id] = len(byOwner[worker.id])
	}
	var fallback availableWorker
	if len(workers) == 0 {
		if fallback, err = fallbackLeader(ctx, tx, actor); err != nil {
			return err
		}
	}
	for _, summary := range owned {
		receiver := fallback
		if len(workers) > 0 {
			receiver = leastLoaded(workers, load)
			load[receiver.id]++
		}
		body := fmt.Sprintf("Reassigned task #%d from %s to %s because %s was retired.", summary.id, retiredLabel, receiver.label, retiredLabel)
		if _, err := tx.ExecContext(ctx, insertReassign, actor.Room, actor.ID, body, now, kindReassign, receiver.id, summary.id); err != nil {
			return err
		}
	}
	return nil
}

// leastLoaded returns the worker with the fewest tasks, the lowest ID on a tie.
func leastLoaded(workers []availableWorker, load map[int64]int) availableWorker {
	best := workers[0]
	for _, worker := range workers[1:] {
		if load[worker.id] < load[best.id] || (load[worker.id] == load[best.id] && worker.id < best.id) {
			best = worker
		}
	}
	return best
}

const insertDeprecate = `INSERT INTO message (room, author, body, created, kind, reply_to) VALUES (?, ?, ?, ?, ?, ?)`

// deprecateOpenTasks ends every unfinished task of the actor's conversation as
// deprecated, with one deprecate message from the actor per task, oldest first.
func deprecateOpenTasks(ctx context.Context, tx *sql.Tx, actor *participant, now int64) error {
	board, err := readTasks(ctx, tx, actor.Room, 0)
	if err != nil {
		return err
	}
	var unfinished []int64
	for _, id := range board.order {
		if !board.tasks[id].done {
			unfinished = append(unfinished, id)
		}
	}
	sort.Slice(unfinished, func(left, right int) bool { return unfinished[left] < unfinished[right] })
	for _, id := range unfinished {
		body := fmt.Sprintf("Task #%d is deprecated. Stop work on it.", id)
		if _, err := tx.ExecContext(ctx, insertDeprecate, actor.Room, actor.ID, body, now, kindDeprecate, id); err != nil {
			return err
		}
	}
	return nil
}
