package conversation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/witseko/agentcy/internal/wire"
)

// selectRoster has no ORDER BY on purpose: SQLite walks the (room, label) unique index,
// so the roster comes out in byte order of the labels, as it does from Python.
const selectRoster = `SELECT participant.label, participant.role 
FROM participant 
WHERE participant.room = ?`

// roster lists every slot and its role without exposing credentials.
func roster(ctx context.Context, tx *sql.Tx, room string) (wire.Value, error) {
	rows, err := tx.QueryContext(ctx, selectRoster, room)
	if err != nil {
		return wire.Value{}, err
	}
	defer rows.Close()
	result := wire.NewObject()
	for rows.Next() {
		var label string
		var role int64
		if err := rows.Scan(&label, &role); err != nil {
			return wire.Value{}, err
		}
		result.Set(label, wire.TextValue(roleLabel(role)))
	}
	return result, rows.Err()
}

// eventKind is the kind of a lifecycle event, which every member sees.
const eventKind = "event"

const insertEvent = `INSERT INTO message (room, author, body, created, kind) VALUES (?, ?, ?, ?, ?)`

// recordEvent appends a lifecycle event to a conversation, authored by actor.
func recordEvent(ctx context.Context, tx *sql.Tx, room string, actor int64, body string, now int64) error {
	_, err := tx.ExecContext(ctx, insertEvent, room, actor, body, now, eventKind)
	return err
}

const insertPlainMessage = `INSERT INTO message (room, author, body, created) VALUES (?, ?, ?, ?)`

// insertNote appends a message without a kind, such as a close summary or reopen note.
func insertNote(ctx context.Context, tx *sql.Tx, room string, author int64, body string, now int64) error {
	_, err := tx.ExecContext(ctx, insertPlainMessage, room, author, body, now)
	return err
}

// collectStrings runs a query that returns one text column and collects it.
func collectStrings(ctx context.Context, tx *sql.Tx, query string, arguments ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// firstValueSet is Python's connection.scalar(query) is not None: true when the query
// returns a row whose first column is not NULL.
func firstValueSet(ctx context.Context, tx *sql.Tx, query string, arguments ...any) (bool, error) {
	var value any
	err := tx.QueryRowContext(ctx, query, arguments...).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return value != nil, nil
}

// firstInteger is connection.scalar(query) for an integer column: the first column of
// the first row, invalid when there is no row or the value is NULL.
func firstInteger(ctx context.Context, tx *sql.Tx, query string, arguments ...any) (sql.NullInt64, error) {
	var value sql.NullInt64
	err := tx.QueryRowContext(ctx, query, arguments...).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullInt64{}, nil
	}
	return value, err
}
