//go:build conformance

package conformance

import (
	"fmt"
	"strings"
	"testing"
)

// From test_close.py and test_repository.py: who may close and reopen, the closing summary
// recorded once, writes refused and reads allowed after close.
func TestCloseAndReopen(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Close me", "initiator": "Lead", "invitees": []any{"Worker", "Gone", "Unused"}})
	lead := created.text("token")
	worker := s.member("worker joins", "join", fields{"code": created.text("invitations", "Worker")}).text("token")
	gone := s.member("Gone joins", "join", fields{"code": created.text("invitations", "Gone")}).text("token")
	s.member("lead retires Gone", "retire", fields{"token": lead, "name": "Gone"})
	s.member("a worker cannot close", "close", fields{"token": worker})
	s.member("a retired member cannot close", "close", fields{"token": gone})
	s.member("lead closes with a summary", "close", fields{"token": lead, "summary": "  All checks passed.  "})
	s.member("closing again records nothing", "close", fields{"token": lead, "summary": "Second summary"})
	s.post("a worker cannot post after close", worker, fields{"body": "Late"})
	s.member("report_usage after close", "report_usage", fields{"token": worker, "input_tokens": 1, "output_tokens": 1})
	s.member("set_role after close", "set_role", fields{"token": lead, "name": "Worker", "role": "co-lead"})
	s.member("retire after close", "retire", fields{"token": lead, "name": "Worker"})
	s.member("release after close", "release", fields{"token": lead})
	s.member("the worker reads the closed conversation", "read", fields{"token": worker})
	s.member("status of a closed conversation", "status", fields{"token": worker})
	s.member("an invalid token is still refused after close", "read", fields{"token": strings.Repeat("Q", 43)})
	s.member("a worker cannot reopen", "reopen", fields{"token": worker})
	s.member("a retired member cannot reopen", "reopen", fields{"token": gone})
	s.member("lead reopens with a note", "reopen", fields{"token": lead, "note": "One more check"})
	s.member("reopening again records nothing", "reopen", fields{"token": lead, "note": "Ignored"})
	s.member("the unused code stays expired", "join", fields{"code": created.text("invitations", "Unused")})
	s.post("the worker posts again", worker, fields{"body": "Back to work"})
	s.member("lead reads the full transcript", "read", fields{"token": lead})
}

// From test_history.py and test_operator_core.py: any chat of the workspace lists and reads
// closed conversations without a token; open and unknown IDs get the same error.
func TestClosedHistory(t *testing.T) {
	s := newScenario(t)
	s.member("list_closed without a store", "list_closed", fields{})
	s.member("read_closed without a store", "read_closed", fields{"conversation": "0123456789abcdef01234567"})
	created := s.operator("human creates", "operator_create", fields{"brief": "History brief\nwith details", "lead": "Lead", "invitees": []any{"Worker"}})
	conversation := created.text("conversation")
	lead := s.member("lead joins", "join", fields{"code": created.invitationCode("Lead")}).text("token")
	s.member("an open conversation looks unknown", "read_closed", fields{"conversation": conversation})
	s.member("an unknown conversation", "read_closed", fields{"conversation": "0123456789abcdef01234567"})
	for index := 1; index <= 17; index++ {
		s.post(fmt.Sprintf("lead posts %d", index), lead, fields{"body": fmt.Sprintf("Entry %d", index)})
	}
	s.member("lead closes", "close", fields{"token": lead, "summary": "All checks passed."})
	second := s.member("lead creates a second conversation", "create", fields{"brief": "Second", "initiator": "Lead", "invitees": []any{"Worker"}})
	s.member("the second conversation closes without messages", "close", fields{"token": second.text("token")})
	s.member("lead creates a third, still open", "create", fields{"brief": "Third", "initiator": "Lead", "invitees": []any{"Worker"}})
	s.member("list_closed shows both closed conversations, newest first", "list_closed", fields{})
	page := s.member("read_closed from zero: brief, roster and 16 messages", "read_closed", fields{"conversation": conversation})
	s.member("read_closed after the cursor", "read_closed", fields{"conversation": conversation, "after": page.number("cursor")})
	s.operator("the human archives the first conversation", "operator_archive", fields{"conversation": conversation, "archived": true})
	s.member("list_closed archived only", "list_closed", fields{"archived": true})
	s.member("list_closed active only", "list_closed", fields{"archived": false})
	s.member("list_closed with null archived lists both", "list_closed", fields{"archived": nil})
	s.member("read_closed of an archived conversation", "read_closed", fields{"conversation": conversation, "after": 17})
	s.member("a malformed conversation ID", "read_closed", fields{"conversation": "../etc"})
	s.member("an uppercase conversation ID", "read_closed", fields{"conversation": "0123456789ABCDEF01234567"})
	s.member("a token is not accepted", "read_closed", fields{"conversation": conversation, "token": "x"})
	s.member("list_closed refuses unknown fields", "list_closed", fields{"after": 0})
}
