package conversation

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/wire"
)

// wakeCase is one message shape that the filter must judge as the read's SQL does.
type wakeCase struct {
	kind      string
	author    string
	recipient string
}

// wakeCases lists every kind (and none) from every author to every kind of recipient.
func wakeCases() []wakeCase {
	kinds := append([]string{"", eventKind}, wire.PostKinds...)
	var cases []wakeCase
	for _, kind := range kinds {
		for _, author := range []string{"Lead", "Worker A", "Worker B"} {
			for _, recipient := range []string{"", "Lead", "Worker A", "Worker B"} {
				cases = append(cases, wakeCase{kind: kind, author: author, recipient: recipient})
			}
		}
	}
	return cases
}

// insertRaw adds a message without any rule check and returns it as a notice carries it.
func (f *fixture) insertRaw(room string, ids map[string]int64, shape wakeCase, now int64) core.Message {
	f.t.Helper()
	message := core.Message{Author: ids[shape.author], Kind: shape.kind}
	var kind, recipient any
	if shape.kind != "" {
		kind = shape.kind
	}
	if shape.recipient != "" {
		recipient = ids[shape.recipient]
		message.Recipient, message.HasRecipient = ids[shape.recipient], true
	}
	f.mustWrite(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		inserted, err := tx.ExecContext(ctx, `INSERT INTO message (room, author, body, created, kind, recipient) VALUES (?, ?, ?, ?, ?, ?)`,
			room, message.Author, fmt.Sprintf("Raw %+v", shape), now, kind, recipient)
		if err != nil {
			return wire.Value{}, err
		}
		message.ID, err = inserted.LastInsertId()
		return wire.Value{}, err
	})
	return message
}

// pageWakes reads a page as the waiting read does and reports whether it is ready.
func (f *fixture) pageWakes(token string, after int64) bool {
	f.t.Helper()
	page := f.mustWrite(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		member, err := authenticate(ctx, tx, token)
		if err != nil {
			return wire.Value{}, err
		}
		page, _, err := readPage(ctx, tx, member, after)
		return page, err
	})
	return field(page, "wake").Truthy()
}

// participantIDs returns the member ID of every label in a room.
func (f *fixture) participantIDs(room string) map[string]int64 {
	f.t.Helper()
	ids := map[string]int64{}
	f.mustWrite(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		rows, err := tx.QueryContext(ctx, `SELECT label, id FROM participant WHERE room = ?`, room)
		if err != nil {
			return wire.Value{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var label string
			var id int64
			if err := rows.Scan(&label, &id); err != nil {
				return wire.Value{}, err
			}
			ids[label] = id
		}
		return wire.Value{}, rows.Err()
	})
	return ids
}

// Port plan fix L3: for every message shape and reader, the wake filter gives the answer
// of the read's SQL, after the reader's cursor, past the message, and from cursor zero.
func TestWakeFilterAgreesWithTheReadQuery(t *testing.T) {
	readers := []string{"Lead", "Worker A"}
	f := newFixture(t)
	created := f.createRoom(wire.Create{Invitees: []wire.Slot{{Name: "Worker A"}, {Name: "Worker B"}}}, startUnix)
	room := text(created, "conversation")
	tokens := map[string]string{"Lead": text(created, "token")}
	for _, name := range []string{"Worker A", "Worker B"} {
		joined, _ := f.joinWith(text(created, "invitations", name), startUnix)
		tokens[name] = text(joined, "token")
	}
	ids := f.participantIDs(room)
	for _, shape := range wakeCases() {
		message := f.insertRaw(room, ids, shape, startUnix)
		for _, reader := range readers {
			filter := newReaderFilter(f.member(tokens[reader]), message.ID-1)
			if got, want := filter.Wakes(message), f.pageWakes(tokens[reader], message.ID-1); got != want {
				t.Errorf("%s reading %+v after the previous message: filter %v, read %v", reader, shape, got, want)
			}
			past := newReaderFilter(f.member(tokens[reader]), message.ID)
			if past.Wakes(message) {
				t.Errorf("%s reading %+v past it: the filter wakes", reader, shape)
			}
		}
	}
	for _, shape := range wakeCases() {
		fresh := newFixture(t)
		freshCreated := fresh.createRoom(wire.Create{Invitees: []wire.Slot{{Name: "Worker A"}, {Name: "Worker B"}}}, startUnix)
		freshRoom := text(freshCreated, "conversation")
		joined, _ := fresh.joinWith(text(freshCreated, "invitations", "Worker A"), startUnix)
		fresh.joinWith(text(freshCreated, "invitations", "Worker B"), startUnix)
		workerToken := text(joined, "token")
		// Joins add events, which do not wake a worker, so only the new message decides.
		if fresh.pageWakes(workerToken, 0) {
			t.Fatal("the join events wake a worker reading from cursor zero")
		}
		message := fresh.insertRaw(freshRoom, fresh.participantIDs(freshRoom), shape, startUnix)
		filter := newReaderFilter(fresh.member(workerToken), 0)
		if got, want := filter.Wakes(message), fresh.pageWakes(workerToken, 0); got != want {
			t.Errorf("Worker A reading %+v from cursor zero: filter %v, read %v", shape, got, want)
		}
	}
}
