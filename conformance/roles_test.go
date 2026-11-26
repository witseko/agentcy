//go:build conformance

package conformance

import "testing"

// From test_set_role.py: lead, co-lead and worker transitions keep exactly one leader.
func TestSetRoleTransitions(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Roles", "initiator": "Lead", "invitees": []any{"Alice", "Bob", "Carol"}})
	lead := created.text("token")
	alice := s.member("Alice joins", "join", fields{"code": created.text("invitations", "Alice")}).text("token")
	bob := s.member("Bob joins", "join", fields{"code": created.text("invitations", "Bob")}).text("token")
	s.member("lead makes Alice a co-lead", "set_role", fields{"token": lead, "name": "alice", "role": "co-lead"})
	s.member("making Alice a co-lead again records nothing", "set_role", fields{"token": lead, "name": "Alice", "role": "co-lead"})
	s.member("Alice makes Bob the lead, demoting both co-leads", "set_role", fields{"token": alice, "name": "Bob", "role": "lead"})
	s.member("Alice is a worker now and cannot change roles", "set_role", fields{"token": alice, "name": "Carol", "role": "worker"})
	s.member("the former lead cannot change roles", "set_role", fields{"token": lead, "name": "Bob", "role": "worker"})
	s.member("status after the lead change", "status", fields{"token": lead})
	s.member("Bob makes the unjoined Carol a co-lead", "set_role", fields{"token": bob, "name": "Carol", "role": "co-lead"})
	s.member("Bob demotes himself, leaving Carol as the lead", "set_role", fields{"token": bob, "name": "Bob", "role": "worker"})
	s.member("status with Carol as the lead", "status", fields{"token": lead})
	s.member("lead reads the role events", "read", fields{"token": lead})
}

// From test_set_role.py: unauthorized callers, invalid roles, retired targets, the human,
// demoting the only lead and closed conversations.
func TestSetRoleRejections(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human creates", "operator_create", fields{"brief": "Roles", "lead": "Lead", "invitees": []any{"Alice", "Bob"}})
	conversation := created.text("conversation")
	lead := s.member("lead joins", "join", fields{"code": created.invitationCode("Lead")}).text("token")
	alice := s.member("Alice joins", "join", fields{"code": created.invitationCode("Alice")}).text("token")
	s.member("a worker cannot change roles", "set_role", fields{"token": alice, "name": "Bob", "role": "co-lead"})
	s.member("the human role fails validation", "set_role", fields{"token": lead, "name": "Alice", "role": "human"})
	s.member("the only lead cannot be demoted", "set_role", fields{"token": lead, "name": "Lead", "role": "worker"})
	s.member("making the lead lead again records nothing", "set_role", fields{"token": lead, "name": "Lead", "role": "lead"})
	s.member("an unknown participant", "set_role", fields{"token": lead, "name": "Ghost", "role": "lead"})
	s.operator("the human retires Bob", "operator_retire", fields{"conversation": conversation, "name": "Bob"})
	s.member("a retired participant cannot change role", "set_role", fields{"token": lead, "name": "Bob", "role": "lead"})
	s.member("the human's role cannot change", "set_role", fields{"token": lead, "name": "Human", "role": "lead"})
	s.operator("the human makes Alice a co-lead", "operator_set_role", fields{"conversation": conversation, "name": "Alice", "role": "co-lead"})
	s.operator("the human demotes the lead, leaving Alice as sole lead", "operator_set_role", fields{"conversation": conversation, "name": "Lead", "role": "worker"})
	s.operator("the human cannot change its own role", "operator_set_role", fields{"conversation": conversation, "name": "Human", "role": "worker"})
	s.operator("show lists retired and active members", "operator_show", fields{"conversation": conversation})
	s.member("Alice closes", "close", fields{"token": alice})
	s.member("set_role on a closed conversation", "set_role", fields{"token": alice, "name": "Lead", "role": "lead"})
	s.operator("operator_set_role on a closed conversation", "operator_set_role", fields{"conversation": conversation, "name": "Lead", "role": "lead"})
	s.operator("operator_retire on a closed conversation", "operator_retire", fields{"conversation": conversation, "name": "Lead"})
}
