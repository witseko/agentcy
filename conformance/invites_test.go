//go:build conformance

package conformance

import (
	"fmt"
	"strings"
	"testing"
)

// From test_invites.py: a late invite returns a prompt with the CODE placeholder, the
// joiner reads the full history, and a co-lead invite keeps its role on reissue.
func TestLateInviteAndPrompts(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates with a two-line brief", "create", fields{
		"brief": "Review\nSecond line stays out of the title.", "initiator": "Lead", "invitees": []any{"Worker"},
	})
	lead := created.text("token")
	s.post("lead posts a finding", lead, fields{"body": "First finding"})
	invited := s.member("lead invites a late worker", "invite", fields{"token": lead, "name": "Analyst"})
	joined := s.member("the late worker joins", "join", fields{"code": invited.text("code")})
	s.member("the late worker reads the full history", "read", fields{"token": joined.text("token")})
	partner := s.member("lead invites a co-lead", "invite", fields{"token": lead, "name": "Partner", "role": "co-lead"})
	s.member("reissue without a role keeps co-lead", "invite", fields{"token": lead, "name": "PARTNER"})
	s.member("the original co-lead code is replaced", "join", fields{"code": partner.text("code")})
	s.member("invite rejects a blank name", "invite", fields{"token": lead, "name": " "})
	s.member("invite rejects the lead role", "invite", fields{"token": lead, "name": "Valid", "role": "lead"})
	s.member("lead reads status", "status", fields{"token": lead})
	long := s.member("a long first line is shortened in prompts", "create", fields{
		"brief": strings.Repeat("\u00e9", 90) + "\nSecond line", "initiator": "Lead", "invitees": []any{"Worker"},
	})
	s.member("the invite prompt uses the shortened heading", "invite", fields{"token": long.text("token"), "name": "Analyst"})
}

// From test_invites.py: reinvite replaces a joined chat, brings back a retired member,
// and refuses unknown names, the caller itself and workers.
func TestReinvite(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Review", "initiator": "Lead", "invitees": []any{"Worker", "Helper"}})
	lead := created.text("token")
	old := s.member("worker joins", "join", fields{"code": created.text("invitations", "Worker")})
	s.post("worker posts evidence", old.text("token"), fields{"body": "Evidence"})
	renewed := s.member("lead reinvites the joined worker", "invite", fields{"token": lead, "name": "worker", "reinvite": true})
	s.member("the old chat lost its token", "read", fields{"token": old.text("token")})
	s.member("status shows the slot as invited again", "status", fields{"token": lead})
	rejoined := s.member("a new chat joins the slot", "join", fields{"code": renewed.text("code")})
	s.member("the new chat sees the old messages and the event", "read", fields{"token": rejoined.text("token")})
	helper := s.member("helper joins", "join", fields{"code": created.text("invitations", "Helper")})
	s.member("lead retires helper", "retire", fields{"token": lead, "name": "Helper"})
	s.member("retiring again is idempotent", "retire", fields{"token": lead, "name": "helper"})
	s.member("a retired member cannot post", "post", fields{"token": helper.text("token"), "submission_id": "x", "body": "Still here"})
	s.member("a retired member can still read", "read", fields{"token": helper.text("token")})
	s.member("a plain invite refuses a retired member", "invite", fields{"token": lead, "name": "Helper"})
	back := s.member("reinvite brings the retired member back", "invite", fields{"token": lead, "name": "Helper", "reinvite": true})
	s.member("the retired chat lost its token", "read", fields{"token": helper.text("token")})
	returned := s.member("helper rejoins", "join", fields{"code": back.text("code")})
	s.post("helper posts again", returned.text("token"), fields{"body": "Back"})
	s.member("status shows helper replied", "status", fields{"token": lead})
	s.member("reinvite of an unknown name", "invite", fields{"token": lead, "name": "Nobody", "reinvite": true})
	s.member("reinvite of yourself", "invite", fields{"token": lead, "name": "Lead", "reinvite": true})
	s.member("a worker cannot reinvite", "invite", fields{"token": rejoined.text("token"), "name": "Lead", "reinvite": true})
}

// From test_invites.py and test_open_invite.py: the cap of 17 participants for create,
// invite and open-invite joins. The human does not count.
func TestParticipantCap(t *testing.T) {
	s := newScenario(t)
	sixteen := []any{}
	for index := 0; index < 16; index++ {
		sixteen = append(sixteen, fmt.Sprintf("Worker %d", index))
	}
	created := s.member("lead creates with 16 invitees", "create", fields{"brief": "Cap", "initiator": "Lead", "invitees": sixteen})
	lead := created.text("token")
	s.member("an 18th slot is refused", "invite", fields{"token": lead, "name": "Another"})
	s.member("reissuing an existing slot still works", "invite", fields{"token": lead, "name": "Worker 0"})
	s.member("17 invitees fail validation", "create", fields{
		"brief": "Cap", "initiator": "Lead", "invitees": append(append([]any{}, sixteen...), "Worker 16"),
	})
	opened := s.member("lead creates an open room", "create", fields{"brief": "Open cap", "initiator": "Lead", "invitees": []any{}, "open_invite": true})
	code := opened.text("open_invite", "code")
	for index := 1; index <= 16; index++ {
		s.member(fmt.Sprintf("open join %d", index), "join", fields{"code": code})
	}
	s.member("the 18th open join is refused", "join", fields{"code": code, "name": "Late"})
	s.member("lead reads status of the full room", "status", fields{"token": opened.text("token")})
}

// From test_open_invite.py: one shared code, name disambiguation, rotation, disabling,
// closing and authorization.
func TestOpenInvite(t *testing.T) {
	s := newScenario(t)
	other := plainWorkspace(t, s.normalizer, "<other-checkout>")
	created := s.member("lead creates an open room", "create", fields{
		"brief": "Investigate flaky build", "initiator": "Lead", "invitees": []any{}, "open_invite": true,
	})
	lead := created.text("token")
	code := created.text("open_invite", "code")
	alice := s.member("Alice joins", "join", fields{"code": code, "name": "Alice"})
	s.member("a second Alice gets a suffix", "join", fields{"code": code, "name": "Alice"})
	s.member("lowercase alice gets the next suffix", "join", fields{"code": code, "name": "alice"})
	s.member("a long name is cut to fit the suffix", "join", fields{"code": code, "name": strings.Repeat("n", 64)})
	s.member("the same long name again", "join", fields{"code": code, "name": strings.Repeat("n", 64)})
	s.member("no name gets Worker 1", "join", fields{"code": code})
	s.member("no name again gets Worker 2", "join", fields{"code": code})
	s.member("the reserved name is refused", "join", fields{"code": code, "name": "Human"})
	s.member("another workspace is refused", "join", fields{"code": code, "workspace": other.workspace})
	s.member("lead reads the join events", "read", fields{"token": lead})
	s.member("lead status shows open_invite", "status", fields{"token": lead})
	s.member("a worker cannot rotate", "open_invite", fields{"token": alice.text("token")})
	rotated := s.member("lead rotates the code", "open_invite", fields{"token": lead})
	s.member("the old code stops working", "join", fields{"code": code})
	s.member("lead disables the open invite", "open_invite", fields{"token": lead, "enabled": false})
	s.member("disabling again records nothing", "open_invite", fields{"token": lead, "enabled": false})
	s.member("the rotated code stops working", "join", fields{"code": rotated.text("code")})
	enabled := s.member("lead enables a new code", "open_invite", fields{"token": lead, "enabled": true})
	s.member("lead closes", "close", fields{"token": lead})
	s.member("closing revokes the open code", "join", fields{"code": enabled.text("code")})
	s.member("open invite on a closed room", "open_invite", fields{"token": lead})
	s.member("lead reads the lifecycle events", "read", fields{"token": lead})
}
