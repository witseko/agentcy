//go:build bench

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

const (
	leadName  = "Lead"
	benchName = "Bench"
	// A conversation holds at most 17 participants: the lead, Bench, and 15 listener slots.
	// More listeners than slots share slot tokens.
	maxListenerSlots = 15
	// socketFillLimit caps the rows posted through the daemon, which also checks that rows
	// written by the daemon and rows written directly sit side by side.
	socketFillLimit = 200
	fillSeed        = 42
	// fillMarker starts the first directly written body, so search can find exactly that row.
	fillMarker = "Fillcheck"
)

var conversationIDPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)

// dataset describes the benchmark conversation and the credentials of its members.
// Tokens stay in memory only; the report never contains them.
type dataset struct {
	conversation   string
	leadToken      string
	benchToken     string
	listenerTokens []string
	existingRows   int
	directRows     int
	socketRows     int
	lastDirectID   int64
	lastMessageID  int64
}

// datasetSummary is the dataset section of the report.
type datasetSummary struct {
	Messages      int64  `json:"messages"`
	DirectRows    int    `json:"direct_rows"`
	SocketRows    int    `json:"socket_rows"`
	FillMethod    string `json:"fill_method"`
	ListenerSlots int    `json:"listener_slots"`
}

// fillMethod describes how the rows were written, for the report.
func (d *dataset) fillMethod() string {
	return fmt.Sprintf("%d rows written with the sqlite3 CLI while the daemon was stopped "+
		"(FTS triggers fired by SQLite), %d rows posted through the daemon socket, "+
		"%d rows written by the daemon during setup", d.directRows, d.socketRows, d.existingRows)
}

// summary returns the dataset section of the report.
func (d *dataset) summary() datasetSummary {
	return datasetSummary{
		Messages:      d.lastMessageID,
		DirectRows:    d.directRows,
		SocketRows:    d.socketRows,
		FillMethod:    d.fillMethod(),
		ListenerSlots: len(d.listenerTokens),
	}
}

// buildDataset creates the conversation through the daemon, writes most rows directly into
// the stopped store, restarts the daemon, posts the rest through it, and reads the result
// back through the daemon. It leaves the daemon running.
func buildDataset(ctx context.Context, s *session) (*dataset, error) {
	if err := s.startDaemon(); err != nil {
		return nil, err
	}
	data, err := createConversation(s)
	if err != nil {
		return nil, err
	}
	if err := s.stopDaemon(); err != nil {
		return nil, err
	}
	if err := fillDirectly(s, data); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.startDaemon(); err != nil {
		return nil, err
	}
	if err := fillThroughSocket(ctx, s, data); err != nil {
		return nil, err
	}
	return data, verifyDataset(s, data)
}

// createConversation creates the room with Lead as creator, then joins Bench and every
// listener slot so each has a token. It also checks that the benchmark's workspace key
// matches the key the daemon derives from the directory.
func createConversation(s *session) (*dataset, error) {
	slots := min(s.cfg.listeners, maxListenerSlots)
	invitees := []string{benchName}
	for index := range slots {
		invitees = append(invitees, fmt.Sprintf("Listener%02d", index+1))
	}
	raw, err := exchange(s.socketPath(), s.memberRequest("create", map[string]any{
		"brief":     "Benchmark conversation: review the retry and cursor handling of the bridge core.",
		"initiator": leadName,
		"invitees":  invitees,
	}))
	if err != nil {
		return nil, err
	}
	var created struct {
		Conversation string            `json:"conversation"`
		Token        string            `json:"token"`
		Invitations  map[string]string `json:"invitations"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return nil, err
	}
	if !conversationIDPattern.MatchString(created.Conversation) {
		return nil, fmt.Errorf("unexpected conversation ID format")
	}
	data := &dataset{conversation: created.Conversation, leadToken: created.Token}
	for _, name := range invitees {
		token, err := joinSlot(s, created.Invitations[name])
		if err != nil {
			return nil, fmt.Errorf("join %s: %w", name, err)
		}
		if name == benchName {
			data.benchToken = token
		} else {
			data.listenerTokens = append(data.listenerTokens, token)
		}
	}
	return data, checkWorkspaceKey(s, data.conversation)
}

// joinSlot redeems one join code and returns the participant token.
func joinSlot(s *session, code string) (string, error) {
	raw, err := exchange(s.socketPath(), s.memberRequest("join", map[string]any{"code": code}))
	if err != nil {
		return "", err
	}
	var joined struct {
		Token string `json:"token"`
	}
	err = json.Unmarshal(raw, &joined)
	return joined.Token, err
}

// checkWorkspaceKey lists the conversations of the workspace directory through the operator
// protocol and expects the conversation created with the computed key.
func checkWorkspaceKey(s *session, conversation string) error {
	operator, err := newOperatorClient(s)
	if err != nil {
		return err
	}
	raw, err := operator.call("operator_list", nil)
	if err != nil {
		return err
	}
	var listed struct {
		Conversations []struct {
			Conversation string `json:"conversation"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return err
	}
	for _, entry := range listed.Conversations {
		if entry.Conversation == conversation {
			return nil
		}
	}
	return fmt.Errorf("the daemon derives a different workspace key for %s", s.workspace)
}

// socketRowCount returns how many of the messages are posted through the daemon.
func socketRowCount(messages int) int {
	return min(socketFillLimit, messages/10)
}

// fillThroughSocket posts the last rows through the daemon, rotating among the members.
func fillThroughSocket(ctx context.Context, s *session, data *dataset) error {
	data.socketRows = socketRowCount(s.cfg.messages)
	tokens := append([]string{data.leadToken, data.benchToken}, data.listenerTokens...)
	text := newTextGenerator(fillSeed + 1)
	for index := range data.socketRows {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := exchange(s.socketPath(), s.memberRequest("post", map[string]any{
			"token":         tokens[index%len(tokens)],
			"submission_id": fmt.Sprintf("bench-socket-%d", index),
			"body":          text.body(),
		}))
		if err != nil {
			return err
		}
	}
	return nil
}

// verifyDataset reads the dataset back through the daemon: the last message ID, a search
// hit on the marked direct row, and a member read near the end of the transcript.
func verifyDataset(s *session, data *dataset) error {
	operator, err := newOperatorClient(s)
	if err != nil {
		return err
	}
	show, err := operator.showTail(data.conversation)
	if err != nil {
		return err
	}
	expected := data.lastDirectID + int64(data.socketRows)
	if show.Cursor != expected {
		return fmt.Errorf("daemon reports last message %d, expected %d", show.Cursor, expected)
	}
	data.lastMessageID = show.Cursor

	if err := verifySearch(operator, data); err != nil {
		return err
	}
	raw, err := exchange(s.socketPath(), s.memberRequest("read", map[string]any{
		"token": data.benchToken, "after": data.lastDirectID - 5,
	}))
	if err != nil {
		return err
	}
	var page struct {
		Messages []messageEntry `json:"messages"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return err
	}
	if len(page.Messages) == 0 {
		return fmt.Errorf("member read after message %d returned no messages", data.lastDirectID-5)
	}
	return nil
}

// searchHit holds the part of an operator_search hit the benchmark checks.
type searchHit struct {
	MessageID *int64 `json:"message_id"`
}

// verifySearch expects operator_search to find the marked first direct row.
func verifySearch(operator *operatorClient, data *dataset) error {
	raw, err := operator.call("operator_search", map[string]any{"query": fillMarker})
	if err != nil {
		return err
	}
	var found struct {
		Hits []searchHit `json:"hits"`
	}
	if err := json.Unmarshal(raw, &found); err != nil {
		return err
	}
	firstDirectID := data.lastDirectID - int64(data.directRows) + 1
	matches := false
	for _, hit := range found.Hits {
		if hit.MessageID != nil && *hit.MessageID == firstDirectID {
			matches = true
		}
	}
	if !matches {
		return fmt.Errorf("search did not find the directly written row %d", firstDirectID)
	}
	return nil
}
