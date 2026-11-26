//go:build conformance

package conformance

import "testing"

// From test_operator.py and test_open_invite.py: the human adds, reissues and reinvites
// slots, toggles the open invite and retires members.
func TestOperatorInviteRetireAndOpenInvite(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human creates", "operator_create", fields{"brief": "Team work", "lead": "Lead", "invitees": []any{"Worker", "Spare"}})
	conversation := created.text("conversation")
	lead := s.member("lead joins", "join", fields{"code": created.invitationCode("Lead")}).text("token")
	worker := s.member("worker joins", "join", fields{"code": created.invitationCode("Worker")}).text("token")
	s.operator("the human adds a slot", "operator_invite", fields{"conversation": conversation, "name": "Analyst"})
	s.operator("the human adds a co-lead slot", "operator_invite", fields{"conversation": conversation, "name": "Partner", "role": "co-lead"})
	s.operator("the human reissues an unused code", "operator_invite", fields{"conversation": conversation, "name": "spare"})
	s.operator("the human cannot reissue a joined slot", "operator_invite", fields{"conversation": conversation, "name": "Worker"})
	s.operator("the human reinvites a joined worker", "operator_invite", fields{"conversation": conversation, "name": "Worker", "reinvite": true})
	s.member("the old worker chat lost access", "status", fields{"token": worker})
	s.operator("the human cannot reinvite itself", "operator_invite", fields{"conversation": conversation, "name": "Human", "reinvite": true})
	s.operator("the human retires the unjoined spare slot", "operator_retire", fields{"conversation": conversation, "name": "Spare"})
	s.operator("a plain invite refuses the retired slot", "operator_invite", fields{"conversation": conversation, "name": "Spare"})
	s.operator("the human cannot retire itself", "operator_retire", fields{"conversation": conversation, "name": "Human"})
	s.operator("the human cannot retire the lead", "operator_retire", fields{"conversation": conversation, "name": "Lead"})
	s.operator("retire an unknown name", "operator_retire", fields{"conversation": conversation, "name": "Ghost"})
	enabled := s.operator("the human enables the open invite", "operator_open_invite", fields{"conversation": conversation})
	s.operator("the human rotates the open invite", "operator_open_invite", fields{"conversation": conversation, "enabled": true})
	s.member("the first open code no longer works", "join", fields{"code": enabled.text("code")})
	s.operator("the human disables the open invite", "operator_open_invite", fields{"conversation": conversation, "enabled": false})
	s.operator("show reflects the changes", "operator_show", fields{"conversation": conversation})
	s.member("lead reads the human's lifecycle events", "read", fields{"token": lead})
	s.operator("the human closes", "operator_close", fields{"conversation": conversation})
	s.operator("invite on a closed conversation", "operator_invite", fields{"conversation": conversation, "name": "Late"})
	s.operator("open invite on a closed conversation", "operator_open_invite", fields{"conversation": conversation})
	s.operator("invite in an unknown conversation", "operator_invite", fields{"conversation": "0123456789abcdef01234567", "name": "X"})
}

// From test_operator.py: operator_tasks lists every task newest first with owner, state,
// pending handoff and timeline; close and archive can deprecate unfinished tasks.
func TestOperatorTasksAndDeprecation(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human creates", "operator_create", fields{"brief": "Tasks", "lead": "Lead", "invitees": []any{"Alpha", "Bravo"}})
	conversation := created.text("conversation")
	lead := s.member("lead joins", "join", fields{"code": created.invitationCode("Lead")}).text("token")
	alpha := s.member("Alpha joins", "join", fields{"code": created.invitationCode("Alpha")}).text("token")
	bravo := s.member("Bravo joins", "join", fields{"code": created.invitationCode("Bravo")}).text("token")
	s.operator("no tasks yet", "operator_tasks", fields{"conversation": conversation})
	finished := s.post("a finished task", lead, fields{"body": "Check the parser\nwith all fixtures", "kind": "task", "to": "Alpha", "due_minutes": 20}).number("message_id")
	handed := s.post("a task to hand off", lead, fields{"body": "   Review   the\tretry   limits   ", "kind": "task", "to": "Alpha", "due_minutes": 45}).number("message_id")
	blocked := s.post("a task to block", lead, fields{"body": "A task title that is longer than one hundred and twenty characters so that the operator task list has to shorten it with an ellipsis", "kind": "task", "to": "Bravo", "due_minutes": 60}).number("message_id")
	s.post("Alpha finishes the first task", alpha, fields{"body": "Parser checked", "kind": "done", "reply_to": finished})
	offer := s.post("Alpha offers the second task", alpha, fields{"body": "Bravo, please take it", "kind": "handoff", "to": "Bravo", "reply_to": handed}).number("message_id")
	s.post("Bravo comments on the offer", bravo, fields{"body": "Looking at it", "reply_to": offer})
	s.post("Bravo blocks the third task", bravo, fields{"body": "Need credentials", "kind": "blocker", "reply_to": blocked})
	s.post("lead replies to the blocker task", lead, fields{"body": "Use the fixture account", "to": "Bravo", "reply_to": blocked})
	s.operator("tasks show states, the pending handoff and timelines", "operator_tasks", fields{"conversation": conversation})
	s.post("the human cannot own a task", lead, fields{"body": "Review this", "kind": "task", "to": "human", "due_minutes": 5})
	s.post("a handoff to the human is refused", alpha, fields{"body": "Yours", "kind": "handoff", "to": "Human", "reply_to": handed})
	s.post("a member cannot post a deprecate message", alpha, fields{"body": "Drop it.", "kind": "deprecate", "reply_to": handed})
	s.operator("a plain close keeps the tasks open", "operator_close", fields{"conversation": conversation})
	s.operator("tasks after a plain close", "operator_tasks", fields{"conversation": conversation})
	s.operator("archive with deprecate_tasks", "operator_archive", fields{"conversation": conversation, "archived": true, "deprecate_tasks": true})
	s.operator("tasks after the deprecation", "operator_tasks", fields{"conversation": conversation})
	s.operator("the human reopens", "operator_reopen", fields{"conversation": conversation})
	s.post("lead assigns a new task", lead, fields{"body": "One more", "kind": "task", "to": "Alpha", "due_minutes": 5})
	s.operator("close with deprecate_tasks", "operator_close", fields{"conversation": conversation, "summary": "Wrapping up", "deprecate_tasks": true})
	s.operator("show after deprecation has no open tasks", "operator_show", fields{"conversation": conversation, "tail": true})
	s.operator("tasks of an unknown conversation", "operator_tasks", fields{"conversation": "0123456789abcdef01234567"})
}

// From test_token_usage.py: reported usage supersedes estimates, the human binds models and
// budgets, and invalid usage reports are refused. No tokens.json is configured.
func TestTokenUsageAndBudgets(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human creates", "operator_create", fields{"brief": "Budget checks", "lead": "Lead", "invitees": []any{"Worker", "Spare"}})
	conversation := created.text("conversation")
	lead := s.member("lead joins with a model and budget", "join", fields{"code": created.invitationCode("Lead"), "model": "lead/model", "token_budget": 100000}).text("token")
	worker := s.member("worker joins", "join", fields{"code": created.invitationCode("Worker")}).text("token")
	s.post("lead posts", lead, fields{"body": "Please estimate the work for the retry module."})
	s.member("worker reads", "read", fields{"token": worker})
	s.operator("the human sets the worker's model and budget", "operator_set_member_budget", fields{"conversation": conversation, "name": "worker", "model": "gemini-2.5-pro", "token_budget": 250000})
	s.operator("the human sets only a budget", "operator_set_member_budget", fields{"conversation": conversation, "name": "Spare", "token_budget": 1000})
	s.operator("the human sets neither", "operator_set_member_budget", fields{"conversation": conversation, "name": "Worker"})
	s.operator("a budget of zero fails validation", "operator_set_member_budget", fields{"conversation": conversation, "name": "Worker", "token_budget": 0})
	s.operator("a model with spaces fails validation", "operator_set_member_budget", fields{"conversation": conversation, "name": "Worker", "model": "bad model"})
	s.operator("the human cannot have a budget", "operator_set_member_budget", fields{"conversation": conversation, "name": "Human", "token_budget": 10})
	s.operator("an unknown participant", "operator_set_member_budget", fields{"conversation": conversation, "name": "Ghost", "token_budget": 10})
	s.member("worker reports exact usage", "report_usage", fields{"token": worker, "input_tokens": 12000, "output_tokens": 3000})
	s.member("lead reports usage with a model", "report_usage", fields{"token": lead, "input_tokens": 90000, "output_tokens": 5000, "model": "lead/model"})
	s.member("negative tokens fail validation", "report_usage", fields{"token": worker, "input_tokens": -1, "output_tokens": 10})
	s.member("a model with spaces fails validation", "report_usage", fields{"token": worker, "input_tokens": 1, "output_tokens": 1, "model": "bad model name"})
	s.member("more than a billion tokens fails validation", "report_usage", fields{"token": worker, "input_tokens": 1000000001, "output_tokens": 0})
	s.member("lead status with usage and balance", "status", fields{"token": lead})
	s.operator("show with token usage", "operator_show", fields{"conversation": conversation, "after": 100})
	s.member("lead retires the worker", "retire", fields{"token": lead, "name": "Worker"})
	s.member("a retired worker cannot report usage", "report_usage", fields{"token": worker, "input_tokens": 1, "output_tokens": 1})
	s.operator("the human closes", "operator_close", fields{"conversation": conversation, "summary": "Done"})
	s.member("report usage after close", "report_usage", fields{"token": lead, "input_tokens": 1, "output_tokens": 1})
	s.operator("set budget after close", "operator_set_member_budget", fields{"conversation": conversation, "name": "Lead", "token_budget": 100000})
}
