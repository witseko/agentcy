//go:build conformance

package conformance

import (
	"strings"
	"testing"
)

// From tests/conversations/test_repository.py: create, join once, read from zero and after
// a cursor, and status for a lead with two worker slots.
func TestCreateJoinReadAndStatus(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates a conversation", "create", fields{
		"brief": "Review the retry behavior", "initiator": "Lead", "invitees": []any{"Reviewer", "Analyst"},
	})
	lead := created.text("token")
	joined := s.member("reviewer joins", "join", fields{"code": created.text("invitations", "Reviewer")})
	reviewer := joined.text("token")
	s.member("the same code again names the taken slot", "join", fields{"code": created.text("invitations", "Reviewer")})
	first := s.post("lead posts", lead, fields{"body": "Please check"})
	s.post("reviewer posts", reviewer, fields{"body": "I checked"})
	s.post("lead posts again", lead, fields{"body": "Thanks"})
	s.member("reviewer reads from zero", "read", fields{"token": reviewer})
	s.member("reviewer reads after the first message", "read", fields{"token": reviewer, "after": first.number("message_id")})
	s.member("reviewer reads with a numeric-string cursor", "read", fields{"token": reviewer, "after": "2"})
	s.member("lead reads status", "status", fields{"token": lead})
	s.member("reviewer reads status", "status", fields{"token": reviewer})
	s.member("analyst joins late and gets the initial page", "join", fields{
		"code": created.text("invitations", "Analyst"), "model": "test/model", "token_budget": 5000,
	})
	s.member("lead reads status after every slot joined", "status", fields{"token": lead})
}

// From test_repository.py: an invited co-lead shares leadership with the creator.
func TestCreateWithCoLead(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates with a co-lead slot", "create", fields{
		"brief": "Review", "initiator": "Lead",
		"invitees":        []any{"Reviewer", fields{"name": "Partner", "role": "co-lead", "model": "test/model", "token_budget": 900}},
		"initiator_model": "lead/model", "initiator_token_budget": 1000,
	})
	partner := s.member("partner joins as co-lead", "join", fields{"code": created.text("invitations", "Partner")})
	s.member("co-lead invites a worker", "invite", fields{"token": partner.text("token"), "name": "Analyst"})
	s.member("duplicate names differ only in case", "create", fields{
		"brief": "Review", "initiator": "Lead", "invitees": []any{"lead"},
	})
	s.member("duplicate invitees", "create", fields{
		"brief": "Review", "initiator": "Lead", "invitees": []any{"Ann", "ANN"},
	})
	s.member("create without invitees or open invite", "create", fields{
		"brief": "Review", "initiator": "Lead", "invitees": []any{},
	})
	s.member("create trims whitespace from brief and names", "create", fields{
		"brief": "  Padded brief\n\n", "initiator": "\u00a0Lead\u2003", "invitees": []any{" Worker "},
	})
}

// From test_invites.py and test_repository.py: wrong, reused, replaced, expired and
// foreign-workspace join codes.
func TestJoinCodeRules(t *testing.T) {
	s := newScenario(t)
	other := plainWorkspace(t, s.normalizer, "<other-checkout>")
	created := s.member("lead creates", "create", fields{
		"brief": "Review", "initiator": "Lead", "invitees": []any{"Worker", "Spare", "Late"},
	})
	lead := created.text("token")
	workerCode := created.text("invitations", "Worker")
	s.member("an untagged unknown code", "join", fields{"code": "not-a-real-code"})
	s.member("a tagged code with a wrong tag", "join", fields{"code": workerCode[:23] + "000000000000"})
	s.member("a valid code from another workspace", "join", fields{"code": workerCode, "workspace": other.workspace})
	reissued := s.member("lead reissues the unused worker code (case-insensitive name)", "invite", fields{"token": lead, "name": "worker"})
	s.member("the replaced code no longer works", "join", fields{"code": workerCode})
	joined := s.member("the new code works", "join", fields{"code": reissued.text("code")})
	s.member("the new code cannot be reused", "join", fields{"code": reissued.text("code")})
	s.member("lead retires the unjoined spare slot", "retire", fields{"token": lead, "name": "Spare"})
	s.member("the retired slot's code is expired", "join", fields{"code": created.text("invitations", "Spare")})
	s.member("a worker cannot invite", "invite", fields{"token": joined.text("token"), "name": "More"})
	s.member("a joined slot cannot be reissued", "invite", fields{"token": lead, "name": "wOrKeR"})
	s.member("an unknown token cannot invite", "invite", fields{"token": strings.Repeat("Z", 43), "name": "More"})
	s.member("lead closes", "close", fields{"token": lead})
	s.member("an unused code of a closed conversation is invalid", "join", fields{"code": created.text("invitations", "Late")})
	s.member("closed conversation rejects invites", "invite", fields{"token": lead, "name": "Late"})
	s.member("lead reopens", "reopen", fields{"token": lead})
	s.member("the code expired at close stays expired after reopen", "join", fields{"code": created.text("invitations", "Late")})
}
