//go:build conformance

package conformance

import (
	"encoding/json"
	"fmt"
	"testing"
)

// From test_repository.py and test_message_kinds.py: submission IDs and the 60-second
// content deduplication, which respects kind, addressee and reply target.
func TestPostDeduplication(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Review", "initiator": "Lead", "invitees": []any{"Worker"}})
	lead := created.text("token")
	s.member("worker joins", "join", fields{"code": created.text("invitations", "Worker")})
	s.post("first post", lead, fields{"submission_id": "initial", "body": "The retry worked"})
	s.post("a retry with the same submission ID", lead, fields{"submission_id": "initial", "body": "The retry worked"})
	s.post("the same body with a new submission ID", lead, fields{"submission_id": "retry", "body": "The retry worked"})
	s.post("the same submission ID with another body", lead, fields{"submission_id": "initial", "body": "Something else"})
	s.post("the same body as a question", lead, fields{"body": "The retry worked", "kind": "question"})
	s.post("the same body addressed to the worker", lead, fields{"body": "The retry worked", "to": "Worker"})
	s.post("the same body as a reply", lead, fields{"body": "The retry worked", "reply_to": 1})
	s.post("the same submission ID with another kind", lead, fields{"submission_id": "initial", "body": "The retry worked", "kind": "finding"})
	s.member("lead reads the transcript", "read", fields{"token": lead})
}

// From test_repository.py and test_message_kinds.py: recipients resolve case-insensitively,
// unknown recipients list active members, reply targets stay in the conversation, and the
// wake flag follows the reader's role.
func TestRecipientsRepliesAndWake(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Review", "initiator": "Lead", "invitees": []any{"Worker", "Peer", "Gone"}})
	lead := created.text("token")
	worker := s.member("worker joins", "join", fields{"code": created.text("invitations", "Worker")}).text("token")
	peer := s.member("peer joins", "join", fields{"code": created.text("invitations", "Peer")}).text("token")
	s.member("lead retires Gone", "retire", fields{"token": lead, "name": "Gone"})
	other := s.member("lead creates a second conversation", "create", fields{"brief": "Other", "initiator": "Lead", "invitees": []any{"Worker"}})
	foreign := s.post("a message in the second conversation", other.text("token"), fields{"body": "Elsewhere"})
	s.post("lead addresses the worker in lower case", lead, fields{"body": "Check timeout path", "to": "worker"})
	s.post("an unknown recipient lists active members", lead, fields{"body": "Wrong target", "to": "Ghost"})
	s.post("an unknown reply target", lead, fields{"body": "Reply", "reply_to": 999})
	s.post("a reply target from another conversation", lead, fields{"body": "Reply", "reply_to": foreign.number("message_id")})
	s.post("a reply_to of zero fails validation", lead, fields{"body": "Reply", "reply_to": 0})
	s.post("a string reply_to fails validation", lead, fields{"body": "Reply", "reply_to": "1"})
	s.post("peer posts an unaddressed blocker", peer, fields{"body": "Blocked on access", "kind": "blocker"})
	after := s.member("worker catches up", "read", fields{"token": worker}).number("cursor")
	s.post("peer posts a note to lead", peer, fields{"body": "For the lead only", "to": "Lead"})
	s.member("worker reads: an addressed message elsewhere does not wake", "read", fields{"token": worker, "after": after})
	s.post("peer posts an unaddressed finding", peer, fields{"body": "Found the cause", "kind": "finding"})
	s.member("worker reads: an unaddressed finding wakes", "read", fields{"token": worker, "after": after})
	s.member("lead reads after the same cursor: leads always wake", "read", fields{"token": lead, "after": after})
	s.member("peer reads after its own messages: own posts are skipped", "read", fields{"token": peer, "after": after})
}

// From test_message_kinds.py: a page holds 16 messages; "more" and "wake" look past the page.
func TestReadPaging(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Paging", "initiator": "Lead", "invitees": []any{"Worker"}})
	lead := created.text("token")
	worker := s.member("worker joins", "join", fields{"code": created.text("invitations", "Worker")}).text("token")
	s.post("worker posts a note to the lead", worker, fields{"body": "Hello lead", "to": "Lead"})
	for index := 1; index <= 17; index++ {
		s.post(fmt.Sprintf("lead posts addressed note %d", index), lead, fields{"body": fmt.Sprintf("Note %d for nobody", index), "to": "Lead"})
	}
	s.post("lead posts an unaddressed note last", lead, fields{"body": "For everyone"})
	page := s.member("worker reads the first page", "read", fields{"token": worker, "after": 1})
	s.member("worker reads the next page", "read", fields{"token": worker, "after": page.number("cursor")})
	s.member("worker reads from zero: 16 messages and more", "read", fields{"token": worker})
	s.member("a cursor beyond the end", "read", fields{"token": worker, "after": 1000})
	s.member("a negative cursor fails validation", "read", fields{"token": worker, "after": -1})
	s.member("a cursor above the 63-bit limit fails validation", "read", fields{"token": worker, "after": json.Number("9223372036854775808")})
	s.member("a fractional cursor fails validation", "read", fields{"token": worker, "after": json.Number("1.5")})
	s.member("a whole float cursor is accepted", "read", fields{"token": worker, "after": json.Number("18.0")})
}

// From test_close.py and the core's waiting read: wait_seconds returns at once when a page
// is ready and after the wait when it is not. Wake-ups by concurrent writes need timing and
// are left to the end-to-end tests.
func TestWaitingRead(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Waiting", "initiator": "Lead", "invitees": []any{"Worker"}})
	lead := created.text("token")
	worker := s.member("worker joins", "join", fields{"code": created.text("invitations", "Worker")}).text("token")
	s.post("lead posts", lead, fields{"body": "Start now"})
	page := s.member("a waiting read with a ready page returns at once", "read", fields{"token": worker, "wait_seconds": 5})
	s.member("a waiting read with nothing new returns after one second", "read", fields{"token": worker, "after": page.number("cursor"), "wait_seconds": 1})
	s.member("wait_seconds above 45 fails validation", "read", fields{"token": worker, "wait_seconds": 46})
	s.member("a string wait_seconds fails validation", "read", fields{"token": worker, "wait_seconds": "1"})
	s.member("lead closes", "close", fields{"token": lead, "summary": "Finished"})
	s.member("a waiting read on a closed conversation returns at once", "read", fields{"token": worker, "after": page.number("cursor"), "wait_seconds": 5})
	s.member("worker status shows usage from delivered pages", "status", fields{"token": worker})
}
