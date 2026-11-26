//go:build bench

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// schemaVersion is the store version whose message shape this file writes.
	schemaVersion = "15"
	// messageColumns is the message table of schema version 15, in declaration order
	// (the former Python core's conversations/tables.py).
	messageColumns = "id,room,author,body,created,kind,recipient,reply_to,due_minutes"
	// roleLead is the stored value of the lead role (the former Python core's conversations/roles.py).
	roleLead = 1
	// fillSpacingSeconds spaces the created times of direct rows, ending just before now.
	fillSpacingSeconds = 20
	taskDueMinutes     = 30
)

// participantRow is one member of the benchmark conversation as stored.
type participantRow struct {
	id   int64
	role int
}

// fillRow is one message row in the shape the daemon writes. Zero values of the optional
// fields stand for NULL.
type fillRow struct {
	id         int64
	author     int64
	body       string
	created    int64
	kind       string
	recipient  int64
	replyTo    int64
	dueMinutes int
}

// openTask is a task row that has no done reply yet.
type openTask struct {
	id    int64
	owner int64
}

// fillPlanner chooses the author, kind, and links of each row. Kinds follow the task rules
// of the core: only the lead assigns a task, with an addressee and due minutes, and only
// the owner answers it with done.
type fillPlanner struct {
	text      *textGenerator
	lead      participantRow
	members   []participantRow
	workers   []participantRow
	openTasks []openTask
}

// fillDirectly writes the directly filled rows with the sqlite3 CLI into the stopped store.
func fillDirectly(s *session, data *dataset) error {
	database := s.databasePath()
	if err := checkStoreShape(database); err != nil {
		return err
	}
	existing, lastID, err := messageCounts(database)
	if err != nil {
		return err
	}
	participants, err := readParticipants(database, data.conversation)
	if err != nil {
		return err
	}
	data.existingRows = existing
	data.directRows = max(0, s.cfg.messages-socketRowCount(s.cfg.messages)-existing)
	data.lastDirectID = lastID + int64(data.directRows)

	scriptPath := filepath.Join(s.home, "bench-fill.sql")
	if err := writeFillScript(scriptPath, data.conversation, participants, lastID+1, data.directRows); err != nil {
		return err
	}
	defer os.Remove(scriptPath)
	script, err := os.Open(scriptPath)
	if err != nil {
		return err
	}
	defer script.Close()
	command := exec.Command("sqlite3", "-bail", "-batch", database)
	command.Stdin = script
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("sqlite3 fill failed: %w: %s", err, bytes.TrimSpace(output))
	}
	return nil
}

// querySQLite runs one query with the sqlite3 CLI and returns its rows, with
// columns separated by "|".
func querySQLite(database string, query string) ([]string, error) {
	output, err := exec.Command("sqlite3", "-batch", "-noheader", database, query).Output()
	if err != nil {
		return nil, fmt.Errorf("sqlite3 query failed: %w", err)
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// checkStoreShape refuses a store whose version or message columns differ from the shape
// this file writes, so the benchmark never fills a store it does not understand.
func checkStoreShape(database string) error {
	rows, err := querySQLite(database, "PRAGMA user_version")
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0] != schemaVersion {
		return fmt.Errorf("store version is %v, the benchmark writes version %s", rows, schemaVersion)
	}
	rows, err = querySQLite(database, "SELECT group_concat(name) FROM pragma_table_info('message')")
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0] != messageColumns {
		return fmt.Errorf("message columns are %v, the benchmark writes %s", rows, messageColumns)
	}
	return nil
}

// messageCounts returns the number of message rows and the highest message ID.
func messageCounts(database string) (int, int64, error) {
	rows, err := querySQLite(database, "SELECT count(*) || '|' || coalesce(max(id), 0) FROM message")
	if err != nil || len(rows) != 1 {
		return 0, 0, fmt.Errorf("count messages: %v", err)
	}
	countText, maxText, _ := strings.Cut(rows[0], "|")
	count, err := strconv.Atoi(countText)
	if err != nil {
		return 0, 0, err
	}
	maxID, err := strconv.ParseInt(maxText, 10, 64)
	return count, maxID, err
}

// readParticipants returns the members of the conversation. The conversation ID was
// checked against its hex pattern, so it is safe inside the SQL literal.
func readParticipants(database string, conversation string) ([]participantRow, error) {
	if !conversationIDPattern.MatchString(conversation) {
		return nil, fmt.Errorf("unexpected conversation ID format")
	}
	rows, err := querySQLite(database, fmt.Sprintf(
		"SELECT id || '|' || role FROM participant WHERE room = '%s' ORDER BY id", conversation))
	if err != nil {
		return nil, err
	}
	var participants []participantRow
	for _, row := range rows {
		idText, roleText, _ := strings.Cut(row, "|")
		id, idErr := strconv.ParseInt(idText, 10, 64)
		role, roleErr := strconv.Atoi(roleText)
		if idErr != nil || roleErr != nil {
			return nil, fmt.Errorf("unexpected participant row %q", row)
		}
		participants = append(participants, participantRow{id: id, role: role})
	}
	return participants, nil
}

// newFillPlanner splits the members into the lead and the others.
func newFillPlanner(participants []participantRow) (*fillPlanner, error) {
	planner := &fillPlanner{text: newTextGenerator(fillSeed), members: participants}
	for _, participant := range participants {
		if participant.role == roleLead {
			planner.lead = participant
		} else {
			planner.workers = append(planner.workers, participant)
		}
	}
	if planner.lead.id == 0 || len(planner.workers) == 0 {
		return nil, fmt.Errorf("conversation needs a lead and at least one other member")
	}
	return planner, nil
}

// next returns the row with the given ID: about 3% tasks, 3% done replies, 10% questions
// to another member, 14% findings, and the rest notes.
func (p *fillPlanner) next(id int64, created int64) fillRow {
	row := fillRow{id: id, created: created, body: p.text.body()}
	roll := p.text.random.IntN(100)
	switch {
	case roll < 3:
		owner := p.workers[p.text.random.IntN(len(p.workers))]
		row.author, row.kind, row.recipient, row.dueMinutes = p.lead.id, "task", owner.id, taskDueMinutes
		p.openTasks = append(p.openTasks, openTask{id: id, owner: owner.id})
	case roll < 6 && len(p.openTasks) > 0:
		task := p.openTasks[0]
		p.openTasks = p.openTasks[1:]
		row.author, row.kind, row.replyTo = task.owner, "done", task.id
	case roll < 16:
		row.author, row.kind = p.randomMember().id, "question"
		row.recipient = p.otherMember(row.author).id
	case roll < 30:
		row.author, row.kind = p.randomMember().id, "finding"
	default:
		row.author = p.randomMember().id
	}
	return row
}

// randomMember returns any member of the conversation.
func (p *fillPlanner) randomMember() participantRow {
	return p.members[p.text.random.IntN(len(p.members))]
}

// otherMember returns a member other than the author.
func (p *fillPlanner) otherMember(author int64) participantRow {
	for {
		candidate := p.randomMember()
		if candidate.id != author {
			return candidate
		}
	}
}

// writeFillScript writes one transaction that inserts each message and its submission
// receipt, as a post through the daemon does. The insert trigger fills the search index.
func writeFillScript(path string, conversation string, participants []participantRow, firstID int64, rows int) error {
	planner, err := newFillPlanner(participants)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	fmt.Fprintln(writer, "PRAGMA foreign_keys=ON;")
	fmt.Fprintln(writer, "BEGIN;")
	start := time.Now().Unix() - int64(rows+10)*fillSpacingSeconds
	for index := range rows {
		id := firstID + int64(index)
		row := planner.next(id, start+int64(index)*fillSpacingSeconds)
		if index == 0 {
			row.body = fillMarker + " " + row.body
		}
		writeRow(writer, conversation, row)
	}
	fmt.Fprintln(writer, "COMMIT;")
	if err := writer.Flush(); err != nil {
		return err
	}
	return file.Close()
}

// writeRow writes the message insert and its receipt insert.
func writeRow(writer *bufio.Writer, conversation string, row fillRow) {
	fmt.Fprintf(writer,
		"INSERT INTO message (%s) VALUES (%d, '%s', %d, %s, %d, %s, %s, %s, %s);\n",
		messageColumns, row.id, conversation, row.author, sqlText(row.body), row.created,
		sqlNullableText(row.kind), sqlNullableInt(row.recipient), sqlNullableInt(row.replyTo),
		sqlNullableInt(int64(row.dueMinutes)))
	fmt.Fprintf(writer,
		"INSERT INTO submission (author, key, message_id) VALUES (%d, 'bench-fill-%d', %d);\n",
		row.author, row.id, row.id)
}

// sqlText quotes a string as an SQL literal.
func sqlText(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// sqlNullableText quotes a string, or returns NULL for the empty string.
func sqlNullableText(value string) string {
	if value == "" {
		return "NULL"
	}
	return sqlText(value)
}

// sqlNullableInt formats an integer, or returns NULL for zero.
func sqlNullableInt(value int64) string {
	if value == 0 {
		return "NULL"
	}
	return strconv.FormatInt(value, 10)
}
