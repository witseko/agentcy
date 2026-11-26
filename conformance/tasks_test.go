//go:build conformance

package conformance

import "testing"

// taskTeam is a conversation with a lead and two joined workers, Alpha and Bravo.
type taskTeam struct {
	conversation, lead, alpha, bravo string
}

// newTaskTeam creates the conversation of test_tasks.py's task_team fixture.
func newTaskTeam(s *scenario) taskTeam {
	s.t.Helper()
	created := s.member("lead creates", "create", fields{"brief": "Review retries", "initiator": "Lead", "invitees": []any{"Alpha", "Bravo"}})
	return taskTeam{
		conversation: created.text("conversation"),
		lead:         created.text("token"),
		alpha:        s.member("Alpha joins", "join", fields{"code": created.text("invitations", "Alpha")}).text("token"),
		bravo:        s.member("Bravo joins", "join", fields{"code": created.text("invitations", "Bravo")}).text("token"),
	}
}

// task assigns a task to a worker with a 30-minute timeline and returns its message ID.
func (team taskTeam) task(s *scenario, label, body, owner string) int {
	s.t.Helper()
	return s.post(label, team.lead, fields{"body": body, "kind": "task", "to": owner, "due_minutes": 30}).number("message_id")
}

// From test_tasks.py: who may assign a task, the required timeline and its bounds, retries.
func TestTaskAssignmentRules(t *testing.T) {
	s := newScenario(t)
	team := newTaskTeam(s)
	s.post("a worker cannot assign a task", team.alpha, fields{"body": "Do this", "kind": "task", "to": "Bravo", "due_minutes": 10})
	s.post("a task needs an addressee", team.lead, fields{"body": "Do this", "kind": "task", "due_minutes": 10})
	s.post("a task needs due_minutes", team.lead, fields{"body": "Do this", "kind": "task", "to": "Alpha"})
	s.post("due_minutes on a note is refused", team.lead, fields{"body": "FYI", "to": "Alpha", "due_minutes": 10})
	s.post("due_minutes of zero fails validation", team.lead, fields{"body": "Do this", "kind": "task", "to": "Alpha", "due_minutes": 0})
	s.post("due_minutes above 1440 fails validation", team.lead, fields{"body": "Do this", "kind": "task", "to": "Alpha", "due_minutes": 1441})
	s.post("string due_minutes fails validation", team.lead, fields{"body": "Do this", "kind": "task", "to": "Alpha", "due_minutes": "30"})
	s.post("an unknown kind fails validation", team.lead, fields{"body": "Do this", "kind": "reassign", "to": "Alpha"})
	s.post("a task to an unknown member", team.lead, fields{"body": "Do this", "kind": "task", "to": "Ghost", "due_minutes": 10})
	s.post("lead assigns a task with the longest timeline", team.lead, fields{"submission_id": "limits", "body": "Review retry limits", "kind": "task", "to": "Alpha", "due_minutes": 1440})
	s.post("a retry keeps one task", team.lead, fields{"submission_id": "limits", "body": "Review retry limits", "kind": "task", "to": "Alpha", "due_minutes": 1440})
	s.post("a retry with a changed timeline is refused", team.lead, fields{"submission_id": "limits", "body": "Review retry limits", "kind": "task", "to": "Alpha", "due_minutes": 40})
	s.post("lead can assign a task to itself", team.lead, fields{"body": "Write the summary\n\n  with   details", "kind": "task", "to": "lead", "due_minutes": 1})
	s.member("status shows open tasks with due times", "status", fields{"token": team.lead})
	s.member("Alpha's status", "status", fields{"token": team.alpha})
}

// From test_tasks.py: block and resume, retries, repeated cycles and owner checks.
func TestTaskBlockAndResume(t *testing.T) {
	s := newScenario(t)
	team := newTaskTeam(s)
	first := team.task(s, "lead assigns the first task", "Review retry limits", "Alpha")
	second := team.task(s, "lead assigns the second task", "Review retry timing", "Alpha")
	s.post("resume without a reply target", team.alpha, fields{"body": "No target", "kind": "resume"})
	s.post("resume of a task that is not blocked", team.alpha, fields{"body": "Already open", "kind": "resume", "reply_to": first})
	blocker := s.post("Alpha blocks the first task", team.alpha, fields{"body": "I need a fixture", "kind": "blocker", "reply_to": first}).number("message_id")
	s.post("Alpha blocks the second task", team.alpha, fields{"body": "I need timing data", "kind": "blocker", "reply_to": second})
	s.post("Bravo answers the blocker", team.bravo, fields{"body": "Here is the fixture", "kind": "finding", "to": "Alpha", "reply_to": blocker})
	s.member("lead reads the blockers", "read", fields{"token": team.lead})
	s.post("resume replying to the blocker instead of the task", team.alpha, fields{"body": "Wrong reply target", "kind": "resume", "reply_to": blocker})
	s.post("Bravo cannot resume Alpha's task", team.bravo, fields{"body": "I resolved it for Alpha", "kind": "resume", "reply_to": first})
	s.post("Alpha resumes the first task", team.alpha, fields{"submission_id": "resume-first", "body": "The fixture lets me continue", "kind": "resume", "reply_to": first})
	s.post("a retry with the same submission ID", team.alpha, fields{"submission_id": "resume-first", "body": "The fixture lets me continue", "kind": "resume", "reply_to": first})
	s.post("a retry with a new submission ID", team.alpha, fields{"submission_id": "retry-first", "body": "The fixture lets me continue", "kind": "resume", "reply_to": first})
	s.member("status: one task open, one blocked", "status", fields{"token": team.lead})
	s.post("block the first task again with the same text", team.alpha, fields{"body": "I need a fixture", "kind": "blocker", "reply_to": first})
	s.post("resume again with the same text", team.alpha, fields{"body": "The fixture lets me continue", "kind": "resume", "reply_to": first})
	s.post("Alpha resumes the second task", team.alpha, fields{"body": "I recovered the timing data", "kind": "resume", "reply_to": second})
	s.post("Alpha finishes the first task", team.alpha, fields{"body": "Retry limits checked", "kind": "done", "reply_to": first})
	s.post("resume of a finished task", team.alpha, fields{"body": "Already closed", "kind": "resume", "reply_to": first})
	s.member("status after the cycles", "status", fields{"token": team.lead})
}

// From test_tasks.py: handoff offers, accept, decline, retries and stale offers.
func TestTaskHandoffs(t *testing.T) {
	s := newScenario(t)
	team := newTaskTeam(s)
	task := team.task(s, "lead assigns a task to Alpha", "Review retry limits", "Alpha")
	s.post("Alpha blocks", team.alpha, fields{"body": "I need a fixture", "kind": "blocker", "reply_to": task})
	s.post("the lead cannot hand off an active owner's task", team.lead, fields{"body": "Try a handoff", "kind": "handoff", "to": "Bravo", "reply_to": task})
	s.post("a handoff to yourself", team.alpha, fields{"body": "To me", "kind": "handoff", "to": "Alpha", "reply_to": task})
	s.post("a handoff without an addressee", team.alpha, fields{"body": "To anyone", "kind": "handoff", "reply_to": task})
	s.post("a handoff without a task", team.alpha, fields{"body": "No task", "kind": "handoff", "to": "Bravo"})
	s.post("a handoff to the human", team.alpha, fields{"body": "To the user", "kind": "handoff", "to": "Human", "reply_to": task})
	offer := s.post("Alpha offers a handoff to Bravo", team.alpha, fields{"submission_id": "offer", "body": "Please finish", "kind": "handoff", "to": "Bravo", "reply_to": task}).number("message_id")
	s.post("a second pending handoff is refused", team.alpha, fields{"body": "Another offer", "kind": "handoff", "to": "Bravo", "reply_to": task})
	s.post("the lead cannot accept an offer addressed to Bravo", team.lead, fields{"body": "I accept", "kind": "accept", "reply_to": offer})
	s.post("Alpha resumes while the offer is pending", team.alpha, fields{"body": "I found the fixture", "kind": "resume", "reply_to": task})
	s.member("status during the pending handoff", "status", fields{"token": team.lead})
	s.post("Bravo accepts", team.bravo, fields{"submission_id": "accept", "body": "I can finish", "kind": "accept", "reply_to": offer})
	s.post("the accept retry returns the same ID", team.bravo, fields{"submission_id": "accept", "body": "I can finish", "kind": "accept", "reply_to": offer})
	s.post("the handoff retry returns the same ID", team.alpha, fields{"submission_id": "offer", "body": "Please finish", "kind": "handoff", "to": "Bravo", "reply_to": task})
	s.post("Alpha's late done does not close Bravo's task", team.alpha, fields{"body": "I finished anyway", "kind": "done", "reply_to": task})
	s.member("status: Bravo owns the task", "status", fields{"token": team.lead})
	second := team.task(s, "lead assigns a second task to Alpha", "Review retry timing", "Alpha")
	declined := s.post("Alpha offers the second task", team.alpha, fields{"body": "Please review", "kind": "handoff", "to": "Bravo", "reply_to": second}).number("message_id")
	s.post("Bravo declines", team.bravo, fields{"body": "I cannot take it", "kind": "decline", "reply_to": declined})
	s.post("an accept after the decline is stale", team.bravo, fields{"body": "An old acceptance", "kind": "accept", "reply_to": declined})
	again := s.post("Alpha offers again", team.alpha, fields{"body": "Can you take over?", "kind": "handoff", "to": "Bravo", "reply_to": second}).number("message_id")
	s.post("Alpha finishes, which drops the offer", team.alpha, fields{"body": "Timing checked", "kind": "done", "reply_to": second})
	s.post("a late accept is stale", team.bravo, fields{"body": "A late acceptance", "kind": "accept", "reply_to": again})
	s.post("Bravo finishes the first task", team.bravo, fields{"body": "Retry limits checked", "kind": "done", "reply_to": task})
	s.member("status: no open tasks", "status", fields{"token": team.lead})
	s.operator("operator task list with timelines", "operator_tasks", fields{"conversation": team.conversation})
}

// From test_tasks.py and test_repository.py: done states, warnings and follow-up tasks.
func TestTaskDoneStates(t *testing.T) {
	s := newScenario(t)
	team := newTaskTeam(s)
	first := team.task(s, "lead assigns a task to Alpha", "Check retries", "Alpha")
	second := team.task(s, "lead assigns a task to Bravo", "Check timeouts", "Bravo")
	s.post("Bravo cannot finish Alpha's task", team.bravo, fields{"body": "Done for Alpha", "kind": "done", "reply_to": first})
	s.post("Alpha's done without a reply warns", team.alpha, fields{"body": "All done", "kind": "done"})
	s.member("status: Alpha replied with an open task", "status", fields{"token": team.lead})
	s.post("Alpha finishes the task", team.alpha, fields{"body": "Retries checked", "kind": "done", "reply_to": first})
	s.member("status: Alpha done", "status", fields{"token": team.lead})
	third := team.task(s, "a follow-up task clears done", "Check backoff", "Alpha")
	s.member("status: Alpha replied again", "status", fields{"token": team.lead})
	s.post("Alpha finishes the follow-up", team.alpha, fields{"body": "Backoff checked", "kind": "done", "reply_to": third})
	s.post("Alpha posts a note after done", team.alpha, fields{"body": "One more thing"})
	s.post("Bravo blocks without a task reference", team.bravo, fields{"body": "Waiting on access", "kind": "blocker"})
	s.member("status: a plain blocker marks Bravo blocked", "status", fields{"token": team.lead})
	s.post("Bravo resolves it with done on the task", team.bravo, fields{"body": "Timeouts checked", "kind": "done", "reply_to": second})
	s.member("status after every task finished", "status", fields{"token": team.lead})
}

// From test_tasks.py: retiring a member moves its open tasks to the least busy available
// worker (invited workers count), or to the lead, and cancels a pending handoff.
func TestRetireMovesTasks(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Review retries", "initiator": "Lead", "invitees": []any{"Alpha", "Bravo", "Charlie"}})
	lead := created.text("token")
	alpha := s.member("Alpha joins", "join", fields{"code": created.text("invitations", "Alpha")}).text("token")
	bravo := s.member("Bravo joins", "join", fields{"code": created.text("invitations", "Bravo")}).text("token")
	first := s.post("task 1 to Alpha", lead, fields{"body": "Audit retries", "kind": "task", "to": "Alpha", "due_minutes": 30}).number("message_id")
	s.post("task 2 to Alpha", lead, fields{"body": "Audit timeouts", "kind": "task", "to": "Alpha", "due_minutes": 30})
	s.post("task 3 to Alpha", lead, fields{"body": "Audit logs", "kind": "task", "to": "Alpha", "due_minutes": 30})
	finished := s.post("task 4 to Alpha", lead, fields{"body": "Audit alerts", "kind": "task", "to": "Alpha", "due_minutes": 30}).number("message_id")
	s.post("task 5 to Bravo", lead, fields{"body": "Audit metrics", "kind": "task", "to": "Bravo", "due_minutes": 30})
	s.post("Alpha finishes task 4", alpha, fields{"body": "Alerts audited", "kind": "done", "reply_to": finished})
	s.post("Alpha blocks task 1", alpha, fields{"body": "Stuck on access", "kind": "blocker", "reply_to": first})
	offer := s.post("Alpha offers task 1 to Bravo", alpha, fields{"body": "Please take over", "kind": "handoff", "to": "Bravo", "reply_to": first}).number("message_id")
	s.member("lead retires Alpha", "retire", fields{"token": lead, "name": "alpha"})
	s.post("Bravo's accept of the cancelled offer is stale", bravo, fields{"body": "Taking over", "kind": "accept", "reply_to": offer})
	s.post("a task to the retired member is refused", lead, fields{"body": "More", "kind": "task", "to": "Alpha", "due_minutes": 5})
	s.member("Bravo reads the reassign messages", "read", fields{"token": bravo})
	s.member("status after balancing", "status", fields{"token": lead})
	s.member("lead retires Bravo", "retire", fields{"token": lead, "name": "Bravo"})
	s.member("lead retires Charlie, the last worker", "retire", fields{"token": lead, "name": "Charlie"})
	s.member("status: the lead holds every open task", "status", fields{"token": lead})
	s.post("the lead finishes a moved task", lead, fields{"body": "Retries audited", "kind": "done", "reply_to": first})
	s.member("lead reads the transcript", "read", fields{"token": lead})
}

// From test_tasks.py: the lead cannot be retired until replaced; a retired co-lead leaves
// the other co-lead as lead.
func TestRetireLeadAndCoLead(t *testing.T) {
	s := newScenario(t)
	team := newTaskTeam(s)
	task := team.task(s, "lead assigns a task to Alpha", "Write the summary", "Alpha")
	s.member("the lead cannot be retired", "retire", fields{"token": team.lead, "name": "Lead"})
	s.member("a worker cannot retire anyone", "retire", fields{"token": team.bravo, "name": "Alpha"})
	s.member("retire an unknown name", "retire", fields{"token": team.lead, "name": "Ghost"})
	s.member("retire the human", "retire", fields{"token": team.lead, "name": "Human"})
	s.member("lead makes Alpha the lead", "set_role", fields{"token": team.lead, "name": "Alpha", "role": "lead"})
	s.member("the demoted lead cannot retire", "retire", fields{"token": team.lead, "name": "Bravo"})
	s.member("Alpha retires the former lead", "retire", fields{"token": team.alpha, "name": "Lead"})
	s.member("status: Alpha leads and keeps its task", "status", fields{"token": team.alpha})
	s.member("Alpha makes Bravo a co-lead", "set_role", fields{"token": team.alpha, "name": "Bravo", "role": "co-lead"})
	s.member("Bravo retires Alpha, the other co-lead", "retire", fields{"token": team.bravo, "name": "Alpha"})
	s.member("status: Bravo is the lead and holds the task", "status", fields{"token": team.bravo})
	s.post("Bravo finishes the moved task", team.bravo, fields{"body": "Summary written", "kind": "done", "reply_to": task})
	s.member("Bravo reads the events", "read", fields{"token": team.bravo})
}
