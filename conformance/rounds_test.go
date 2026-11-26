//go:build conformance

package conformance

import "testing"

// From test_sealed_rounds.py: proposals stay private until the last joined worker submits,
// then they publish with fresh message IDs. Release by deadline needs a clock and is skipped.
func TestSealedRoundAutoRelease(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead opens a sealed round", "create", fields{
		"brief": "Propose a test independently", "initiator": "Lead", "invitees": []any{"First", "Second", "Late"},
		"sealed": true, "deadline_minutes": 1,
	})
	lead := created.text("token")
	first := s.member("First joins", "join", fields{"code": created.text("invitations", "First")}).text("token")
	second := s.member("Second joins", "join", fields{"code": created.text("invitations", "Second")}).text("token")
	s.post("lead posts a note during the round", lead, fields{"body": "Work alone"})
	s.post("First submits a proposal", first, fields{"submission_id": "p1", "body": "Test the timeout", "kind": "proposal"})
	s.post("First retries the same proposal", first, fields{"submission_id": "p1", "body": "Test the timeout", "kind": "proposal"})
	s.post("First submits the same body with a new ID", first, fields{"submission_id": "p1b", "body": "Test the timeout", "kind": "proposal"})
	s.post("First cannot submit a second proposal", first, fields{"submission_id": "p2", "body": "Another idea", "kind": "proposal"})
	s.post("a held proposal's ID cannot post a note", first, fields{"submission_id": "p1", "body": "A note", "kind": "note"})
	s.member("Second's status shows it has not submitted", "status", fields{"token": second})
	s.member("First's status shows it submitted", "status", fields{"token": first})
	s.member("lead's status has no submission flag", "status", fields{"token": lead})
	s.member("Second reads: proposals stay hidden", "read", fields{"token": second})
	s.post("Second submits, which releases the round", second, fields{"submission_id": "s1", "body": "Test the retry", "kind": "proposal"})
	s.post("First's retry after release returns the message ID", first, fields{"submission_id": "p1", "body": "Test the timeout", "kind": "proposal"})
	s.post("First's new ID with the released body returns the message ID", first, fields{"submission_id": "p3", "body": "Test the timeout", "kind": "proposal"})
	s.post("a new proposal after release is not accepted", first, fields{"submission_id": "p4", "body": "Late idea", "kind": "proposal"})
	late := s.member("Late joins after release", "join", fields{"code": created.text("invitations", "Late")}).text("token")
	s.post("Late's proposal in the critique round", late, fields{"body": "Too late", "kind": "proposal"})
	s.member("Late reads the released proposals", "read", fields{"token": late})
	s.member("lead releases again: critique round", "release", fields{"token": lead})
	s.member("lead status in the critique round", "status", fields{"token": lead})
}

// From test_sealed_rounds.py: manual release permissions, proposal validation and
// release in a conversation without a round.
func TestSealedRoundManualRelease(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead opens a sealed round with the default deadline", "create", fields{
		"brief": "Propose", "initiator": "Lead", "invitees": []any{"Worker", "Idle"}, "sealed": true,
	})
	lead := created.text("token")
	worker := s.member("Worker joins", "join", fields{"code": created.text("invitations", "Worker")}).text("token")
	s.member("Idle joins", "join", fields{"code": created.text("invitations", "Idle")})
	s.post("the lead cannot post a proposal", lead, fields{"body": "Lead idea", "kind": "proposal"})
	s.post("a proposal cannot have an addressee", worker, fields{"body": "To lead", "kind": "proposal", "to": "Lead"})
	s.post("a proposal cannot reply", worker, fields{"body": "Reply", "kind": "proposal", "reply_to": 1})
	s.post("Worker submits", worker, fields{"submission_id": "w1", "body": "My proposal", "kind": "proposal"})
	s.member("a worker cannot release", "release", fields{"token": worker})
	s.member("lead releases early", "release", fields{"token": lead})
	s.member("lead reads the release event naming the missing worker", "read", fields{"token": lead})
	plain := s.member("lead creates a plain conversation", "create", fields{"brief": "Plain", "initiator": "Lead", "invitees": []any{"Worker"}})
	s.member("release without a round", "release", fields{"token": plain.text("token")})
	plainWorker := s.member("worker joins the plain conversation", "join", fields{"code": plain.text("invitations", "Worker")}).text("token")
	s.post("a proposal without a round", plainWorker, fields{"body": "Idea", "kind": "proposal"})
	s.member("deadline without a sealed round", "create", fields{"brief": "Bad", "initiator": "Lead", "invitees": []any{"W"}, "deadline_minutes": 5})
	s.member("deadline above 60 minutes", "create", fields{"brief": "Bad", "initiator": "Lead", "invitees": []any{"W"}, "sealed": true, "deadline_minutes": 61})
	s.member("deadline of zero", "create", fields{"brief": "Bad", "initiator": "Lead", "invitees": []any{"W"}, "sealed": true, "deadline_minutes": 0})
}

// From test_sealed_rounds.py and test_operator.py: retiring the last pending worker releases
// the round and discards a retired author's held proposal.
func TestSealedRoundRetireReleases(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead opens a sealed round", "create", fields{
		"brief": "Propose", "initiator": "Lead", "invitees": []any{"First", "Second", "Third"}, "sealed": true, "deadline_minutes": 30,
	})
	lead := created.text("token")
	first := s.member("First joins", "join", fields{"code": created.text("invitations", "First")}).text("token")
	second := s.member("Second joins", "join", fields{"code": created.text("invitations", "Second")}).text("token")
	third := s.member("Third joins", "join", fields{"code": created.text("invitations", "Third")}).text("token")
	s.post("First submits", first, fields{"body": "First idea", "kind": "proposal"})
	s.post("Second submits", second, fields{"body": "Second idea", "kind": "proposal"})
	s.member("lead retires Second, discarding its proposal", "retire", fields{"token": lead, "name": "Second"})
	s.member("status after the retirement", "status", fields{"token": lead})
	s.member("lead retires Third, the last pending worker", "retire", fields{"token": lead, "name": "Third"})
	s.member("First reads the released round", "read", fields{"token": first})
	s.member("Third reads as a retired member", "read", fields{"token": third})
}

// From test_sealed_rounds.py, test_history.py and test_operator.py: closing discards held
// proposals, so they never reach the closed record or search.
func TestSealedRoundCloseDiscards(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human opens a sealed round", "operator_create", fields{
		"brief": "Propose privately", "lead": "Lead", "invitees": []any{"First", "Second"}, "sealed": true, "deadline_minutes": 15,
	})
	conversation := created.text("conversation")
	first := s.member("First joins", "join", fields{"code": created.invitationCode("First")}).text("token")
	s.member("Second joins", "join", fields{"code": created.invitationCode("Second")})
	s.post("First submits a secret proposal", first, fields{"body": "Secret zebra plan", "kind": "proposal"})
	s.operator("search does not find a held proposal", "operator_search", fields{"query": "zebra"})
	s.operator("show hides the held proposal", "operator_show", fields{"conversation": conversation})
	s.operator("the human posts during the round", "operator_post", fields{"conversation": conversation, "submission_id": "h", "body": "Take your time"})
	s.operator("the human closes with a summary", "operator_close", fields{"conversation": conversation, "summary": "Stopped early"})
	s.member("the closed record has no proposal", "read_closed", fields{"conversation": conversation})
	s.operator("search still finds nothing", "operator_search", fields{"query": "zebra"})
	s.member("First reads the closed conversation", "read", fields{"token": first})
	s.post("First cannot post after close", first, fields{"body": "Late", "kind": "proposal"})
}
