package shim

import (
	"fmt"
	"strings"

	"github.com/witseko/agentcy/internal/progress"
	"github.com/witseko/agentcy/internal/wire"
)

// Quiet-wait limits, from src/agentcy/mcp/server.py. Hosts stop a tool call after about
// one minute, and the first call may also start the core.
const (
	idleWaits           = 3
	workerIdleDoneWaits = 1
	// Some hosts (Codex) end the turn on "Stop listening" and wake only when the user pings
	// them, so a worker listens while a peer is active. The cap (about one hour) stops
	// abandoned rooms.
	workerActiveIdleWaits = 80
	leadCheckinWaits      = 2
	leadIdleWaits         = 8
	// A lead keeps listening while a teammate works, for the same reason and cap.
	leadActiveIdleWaits = workerActiveIdleWaits
	staleIdleSeconds    = 120
	roundSealed         = "sealed"
	roundCritique       = "critique"
)

// The guidance texts are prompt text: they are copied byte for byte from the Python shim.
var (
	listenText         = fmt.Sprintf("call read_messages again with your new cursor and wait_seconds=%d", wire.MaxWaitSeconds)
	nextAfterMessages  = "Handle these messages and post a reply if needed (skip empty acknowledgments). If 'more' is true, read remaining pages with wait_seconds=0. Then " + listenText + "."
	nextAfterClose     = "The conversation is closed. If 'more' is true, read the remaining pages with wait_seconds=0. Stop listening and report the outcome to your user."
	nextAfterRetire    = "You have been retired from this conversation. Stop listening and report to your user."
	blockerHelp        = "Ask a teammate who can clear the blocker. Post kind='question' with their name in 'to' and the blocker message ID in 'reply_to'. Ask them to address their answer to the blocked member."
	taskResumeHelp     = "Once you can continue a blocked task, reply to its original task ID with kind='resume'."
	overdueLeadHelp    = "For each overdue task, ask the owner for progress with the owner's name in 'to', or move the work to a free teammate."
	overdueWorkerHelp  = "Report progress on each overdue task to the lead, or reply to the task with kind='blocker'."
	leadCheckinHelp    = "Check in on quiet teammates or post a brief team update so waiting peers stay active."
	compactUsageFields = []string{"model", "conversation_tokens", "used_percent", "remaining_percent", "status"}
	// contextFields are left out of a waiting read: the chat holds them from create, join
	// or a one-time read.
	contextFields = []string{"brief", "role", "roster"}
)

// waitContext is everything that guidance reads about one read.
type waitContext struct {
	status          progress.Status
	me              string
	hasMessages     bool
	silentWaits     int
	blockerReceived bool
	actionHint      string
}

// pythonText formats a JSON value the way a Python f-string formats the decoded value.
func pythonText(value wire.Value) string {
	switch value.Kind {
	case wire.String:
		return value.Text
	case wire.Number:
		return value.Literal
	case wire.Bool:
		if value.Boolean {
			return "True"
		}
		return "False"
	case wire.Null:
		return "None"
	default:
		return value.Compact()
	}
}

// checkinNudge names a stale working peer when one has been quiet long enough, else it
// returns the general hint.
func checkinNudge(members []progress.Member, candidates []string, owners []progress.Member) string {
	stale, found := progress.QuietestWorkingPeer(members, candidates, owners)
	if !found || stale.IdleSeconds < staleIdleSeconds {
		return leadCheckinHelp
	}
	taskText := ""
	if len(stale.TaskIDs) > 0 {
		references := make([]string, len(stale.TaskIDs))
		for index, id := range stale.TaskIDs {
			references[index] = fmt.Sprintf("#%d", id)
		}
		taskText = " on " + strings.Join(references, ", ")
	}
	return fmt.Sprintf("%s has been quiet for %ds%s — check in with to='%s' or post a brief team update.",
		stale.Name, stale.IdleSeconds, taskText, stale.Name)
}

// incomingActionHint returns a targeted prompt for the newest task, handoff, reassign or
// question addressed to the caller.
func incomingActionHint(messages []wire.Value, me string) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		to, _ := message.Get("to")
		if to.Kind != wire.String || to.Text != me {
			continue
		}
		if hint := actionHintFor(message); hint != "" {
			return hint
		}
	}
	return ""
}

// actionHintFor returns the prompt for one message addressed to the caller, or "".
func actionHintFor(message wire.Value) string {
	kind, _ := message.Get("kind")
	idValue, _ := message.Get("id")
	id := pythonText(idValue)
	replyTo, _ := message.Get("reply_to")
	switch kind.StringOf() {
	case "task":
		due, _ := message.Get("due_minutes")
		dueText := ""
		if due.Truthy() {
			dueText = fmt.Sprintf(", due in %s minutes from its assignment", pythonText(due))
		}
		return fmt.Sprintf("New task #%s assigned to you%s. Start immediately and reply with reply_to=%s using kind='done' (with evidence) or kind='blocker'.", id, dueText, id)
	case "handoff":
		taskReference := ""
		if replyTo.Truthy() {
			taskReference = " for task #" + pythonText(replyTo)
		}
		return fmt.Sprintf("Handoff #%s offered to you%s. Reply with reply_to=%s using kind='accept' or kind='decline'.", id, taskReference, id)
	case "reassign":
		taskID := pythonText(replyTo)
		if !message.Has("reply_to") {
			taskID = "None"
		}
		return fmt.Sprintf("Task #%s was reassigned to you. Read its history, continue the work, and reply with reply_to=%s using kind='done' (with evidence) or kind='blocker'.", taskID, taskID)
	case "question":
		asker := "the sender"
		if label, found := message.Get("label"); found {
			asker = pythonText(label)
		}
		return fmt.Sprintf("Direct question #%s from %s. Investigate and reply with reply_to=%s and to='%s'.", id, asker, id, asker)
	}
	return ""
}

// tokenBalanceNote returns the lead balancing recommendation when active models are
// imbalanced.
func tokenBalanceNote(status progress.Status) string {
	balance := status.TokenBalance
	balanced, _ := balance.Get("balanced")
	if !balance.Truthy() || balanced.Truthy() {
		return ""
	}
	recommendation, _ := balance.Get("recommendation")
	if !recommendation.Truthy() {
		return ""
	}
	return " " + pythonText(recommendation)
}

// freeTeammatesNote mentions finished peers who hold no open tasks.
func freeTeammatesNote(members, owners []progress.Member, me string) string {
	available := progress.AvailableWorkers(members, owners, me)
	if len(available) == 0 {
		return ""
	}
	return fmt.Sprintf(" Free teammates waiting for next steps: %s (assign kind='task' or post a team update).", strings.Join(available, ", "))
}

// describeOpenTask shows the state preventing the owner from working on an open task.
func describeOpenTask(member progress.Member, task progress.Task) string {
	detail := fmt.Sprintf("#%d for %s", task.ID, member.Name)
	switch {
	case progress.IsRetired(member):
		detail += " (retired)"
	case progress.FreshRunEnded(member) || progress.AwaitingFreshJoin(member):
		detail += fmt.Sprintf(" (fresh run %s)", *member.LaunchState)
	case member.State == "invited":
		detail += " (invitation unused)"
	}
	if task.Blocked {
		detail += " (blocked)"
	}
	if task.Overdue {
		detail += " (overdue)"
	}
	return detail
}

// hasOverdueTask recognizes an open task list that holds a task past its due time.
func hasOverdueTask(tasks []progress.Task) bool {
	for _, task := range tasks {
		if task.Overdue {
			return true
		}
	}
	return false
}

// leadTaskGuidance names each open task and chooses whether to work, listen, or report.
func leadTaskGuidance(wait waitContext, owners []progress.Member) string {
	members := wait.status.Members
	var descriptions []string
	overdue := false
	for _, owner := range owners {
		for _, task := range owner.OpenTasks {
			descriptions = append(descriptions, describeOpenTask(owner, task))
		}
		overdue = overdue || hasOverdueTask(owner.OpenTasks)
	}
	summary := fmt.Sprintf("Open tasks are %s.", strings.Join(descriptions, ", "))
	if overdue {
		summary += " " + overdueLeadHelp
	}
	idleNote := freeTeammatesNote(members, owners, wait.me)
	if wait.hasMessages {
		return summary + idleNote + " " + nextAfterMessages
	}
	working, hasBlockedTask := progress.TaskAvailability(owners)
	if containsText(working, wait.me) {
		return summary + idleNote + " Continue your tasks. Reply to each task with kind='done' or 'blocker'."
	}
	otherWorking := progress.WorkingPeersWithoutTasks(members, owners, wait.me)
	if (len(working) > 0 || len(otherWorking) > 0) && wait.silentWaits < leadActiveIdleWaits {
		checkin := ""
		if wait.silentWaits >= leadCheckinWaits {
			candidates := append(withoutText(working, wait.me), otherWorking...)
			checkin = " " + checkinNudge(members, candidates, owners)
		}
		return fmt.Sprintf("%s%s To keep listening, %s.%s", summary, idleNote, listenText, checkin)
	}
	if hasBlockedTask && wait.silentWaits < leadIdleWaits {
		return fmt.Sprintf("%s%s If a teammate is checking a blocker, %s. If you need your user's help, stop listening and report the open tasks.", summary, idleNote, listenText)
	}
	return summary + idleNote + " Stop listening and tell your user which tasks are still open."
}

// endedFreshRuns names the members other than the caller, without open tasks, whose
// unfinished fresh run ended.
func endedFreshRuns(status progress.Status, owners []progress.Member, me string) []string {
	var ended []string
	for _, member := range status.Members {
		if member.Name != me && !ownsTask(owners, member.Name) && member.State != "done" &&
			!progress.IsRetired(member) && progress.FreshRunEnded(member) {
			ended = append(ended, fmt.Sprintf("%s (fresh run %s)", member.Name, *member.LaunchState))
		}
	}
	return ended
}

// leadGuidance uses task ownership when present and otherwise member activity.
func leadGuidance(wait waitContext) string {
	status := wait.status
	owners := progress.TaskOwners(status)
	members := status.Members
	ended := endedFreshRuns(status, owners, wait.me)
	summary := ""
	if len(ended) > 0 {
		summary = fmt.Sprintf("Fresh runs left unfinished work for %s. ", strings.Join(ended, ", "))
	}
	if len(owners) > 0 {
		return summary + leadTaskGuidance(wait, owners)
	}
	pending := progress.LeadWaitingOn(status, wait.me)
	idleNote := freeTeammatesNote(members, owners, wait.me)
	if wait.hasMessages {
		if len(pending) == 0 && len(ended) == 0 && idleNote != "" {
			return summary + "All other members are done. Handle these messages, assign any follow-up tasks with kind='task', or verify the evidence and report one conclusion to your user."
		}
		separator := ""
		if idleNote != "" {
			separator = " "
		}
		return summary + strings.TrimLeft(idleNote, " \t\n\r\v\f") + separator + nextAfterMessages
	}
	if len(pending) > 0 {
		return summary + leadPendingGuidance(members, pending, wait.silentWaits, idleNote)
	}
	if len(ended) > 0 {
		return summary + "Relaunch with launch_participant, reassign to a free teammate, or take over; Stop listening if you cannot recover it, verify the evidence, and report what remains open."
	}
	if status.OpenInvite && !hasOtherMembers(members, wait.me) {
		if wait.silentWaits >= leadIdleWaits {
			return "No teammates have joined yet via the open invite. Stop listening and tell your user."
		}
		return fmt.Sprintf("Waiting for teammates to join via the open invite. To keep listening, %s.", listenText)
	}
	return "All other members are done. Stop listening. Verify the evidence against the brief and report one conclusion to your user."
}

// hasOtherMembers reports another non-human member who is not retired.
func hasOtherMembers(members []progress.Member, me string) bool {
	for _, member := range members {
		if member.Name != me && member.Role != progress.RoleHuman && !progress.IsRetired(member) {
			return true
		}
	}
	return false
}

// leadPendingGuidance guides a lead from the priority group of outstanding peers.
func leadPendingGuidance(members []progress.Member, pending []string, silentWaits int, idleNote string) string {
	labels := make([]string, len(pending))
	for index, name := range pending {
		member, _ := findMember(members, name)
		labels[index] = name
		if progress.AwaitingFreshJoin(member) {
			labels[index] = fmt.Sprintf("%s (fresh run %s)", name, *member.LaunchState)
		}
	}
	names := strings.Join(labels, ", ")
	first, _ := findMember(members, pending[0])
	if first.State == "invited" && !progress.AwaitingFreshJoin(first) {
		return fmt.Sprintf("Stop listening. Tell your user which join codes are unused: %s.", names)
	}
	if first.State == "blocked" {
		return fmt.Sprintf("Stop listening and tell your user who is blocked: %s.", names)
	}
	if silentWaits >= leadActiveIdleWaits {
		return fmt.Sprintf("Stop listening and tell your user who is still working: %s.", names)
	}
	checkin := ""
	if silentWaits >= leadCheckinWaits {
		checkin = " " + checkinNudge(members, pending, nil)
	}
	return fmt.Sprintf("Still working: %s. To keep listening, %s.%s%s", names, listenText, idleNote, checkin)
}

// workerIdleLimit selects the quiet-wait ceiling for a worker from peer activity and its
// own state.
func workerIdleLimit(members []progress.Member, me, state string) int {
	if progress.HasActivePeers(members, me) {
		return workerActiveIdleWaits
	}
	if state == "done" {
		return workerIdleDoneWaits
	}
	return idleWaits
}

// workerMessageGuidance combines blocked-task resume help, incoming action hints, and
// the standard reply guidance.
func workerMessageGuidance(openTasks []progress.Task, actionHint string) string {
	var parts []string
	for _, task := range openTasks {
		if task.Blocked {
			parts = append(parts, taskResumeHelp)
			break
		}
	}
	if actionHint != "" {
		parts = append(parts, actionHint)
	}
	parts = append(parts, nextAfterMessages)
	return strings.Join(parts, " ")
}

// workerGuidance guides a worker from open tasks, incoming action items, and adaptive
// idle limits.
func workerGuidance(wait waitContext) string {
	members := wait.status.Members
	caller, _ := findMember(members, wait.me)
	if wait.hasMessages {
		return workerMessageGuidance(caller.OpenTasks, wait.actionHint)
	}
	for _, task := range caller.OpenTasks {
		if !task.Blocked {
			overdue := ""
			if hasOverdueTask(caller.OpenTasks) {
				overdue = " " + overdueWorkerHelp
			}
			return "Continue your open tasks. Reply to each task with kind='done' or 'blocker'." + overdue
		}
	}
	if caller.State == "joined" {
		return "You have not posted your part. Do the work and post your findings or blockers."
	}
	if wait.silentWaits >= workerIdleLimit(members, wait.me, caller.State) {
		return "Stop listening and tell your user that the conversation is quiet."
	}
	if blocked := progress.BlockedPeers(members, wait.me); len(blocked) > 0 {
		names := make([]string, len(blocked))
		for index, peer := range blocked {
			names[index] = peer.Name
		}
		return fmt.Sprintf("No new messages for you, but %s is blocked. Check if you can help with evidence, or %s.", strings.Join(names, ", "), listenText)
	}
	return fmt.Sprintf("No new messages. To keep listening, %s.", listenText)
}

// guidance renders the next action from the caller's role, progress, and recent waits.
func guidance(wait waitContext) string {
	status := wait.status
	if !status.Open {
		return nextAfterClose
	}
	caller, _ := findMember(status.Members, wait.me)
	if progress.IsRetired(caller) {
		return nextAfterRetire
	}
	if status.Round == roundSealed {
		return sealedGuidance(status, caller, wait.hasMessages)
	}
	if caller.Role != progress.RoleWorker {
		next := leadGuidance(wait) + tokenBalanceNote(status)
		if wait.actionHint != "" && wait.hasMessages {
			next = wait.actionHint + " " + next
		}
		if wait.blockerReceived {
			return blockerHelp + " " + next
		}
		return next
	}
	return workerGuidance(wait)
}

// sealedGuidance tells participants how to contribute or wait during the sealed round.
func sealedGuidance(status progress.Status, caller progress.Member, hasMessages bool) string {
	prefix := ""
	if hasMessages {
		prefix = "Handle the visible messages. "
	}
	if caller.Role != progress.RoleWorker {
		return fmt.Sprintf("%sDo your part while workers prepare their proposals. Wait for the proposals to be released, or use release_proposals to start critique early. To keep listening, %s.", prefix, listenText)
	}
	if status.ProposalSubmitted {
		return fmt.Sprintf("%sYour proposal is sealed. To wait for release, %s.", prefix, listenText)
	}
	return prefix + "Prepare your independent proposal and send it with post_message using kind='proposal'. Then wait for release."
}

// findMember returns the member with the given name.
func findMember(members []progress.Member, name string) (progress.Member, bool) {
	for _, member := range members {
		if member.Name == name {
			return member, true
		}
	}
	return progress.Member{}, false
}

// ownsTask reports whether name is among the task owners.
func ownsTask(owners []progress.Member, name string) bool {
	_, found := findMember(owners, name)
	return found
}

// containsText reports whether texts holds text.
func containsText(texts []string, text string) bool {
	for _, candidate := range texts {
		if candidate == text {
			return true
		}
	}
	return false
}

// withoutText returns a copy of texts without text.
func withoutText(texts []string, text string) []string {
	var kept []string
	for _, candidate := range texts {
		if candidate != text {
			kept = append(kept, candidate)
		}
	}
	return kept
}
