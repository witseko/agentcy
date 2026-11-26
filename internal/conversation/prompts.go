package conversation

import "strings"

// The join instructions, copied from repository.py. CODE is replaced with a join code
// when the caller has one.
const (
	joinPrompt = "Use agentcy to join with CODE. It returns the brief, roster, and initial messages " +
		"(only call read_messages with wait_seconds=0 if 'more' is true). Say which role you joined as " +
		"and start your part. Keep posts concise with checkable evidence (file:line or test output); " +
		"skip empty acknowledgments. Send findings and questions directly to the peer who can act on " +
		"them, report brief progress updates to the lead periodically while working, reply to your " +
		"task ID with kind='done' or 'blocker', and check peer blockers before going idle. " +
		"Follow the bridge's 'next' guidance."
	openJoinPrompt = "Use agentcy to join with CODE (pass your preferred name if you want one). It returns " +
		"the brief, roster, and initial messages (only call read_messages with wait_seconds=0 if " +
		"'more' is true). Say which role you joined as and start your part. Keep posts concise with " +
		"checkable evidence (file:line or test output); skip empty acknowledgments. Send findings and " +
		"questions directly to the peer who can act on them, report brief progress updates to the " +
		"lead periodically while working, reply to your task ID with kind='done' or 'blocker', and " +
		"check peer blockers before going idle. Follow the bridge's 'next' guidance."
	leadJoinPrompt = "Use agentcy to join with CODE. You lead this conversation. Read the shared brief and " +
		"catch up from cursor zero. Assign tasks with kind='task', the worker's name in 'to', " +
		"and due_minutes (the minutes you expect the task to take): you own the task timeline. " +
		"You can add teammates with invite_participant, launch_participant (see list_plugins), or " +
		"open_invite, and retire inactive or unneeded teammates with retire_participant. " +
		"Track the team status in read_messages, assign follow-up work when workers finish, and post " +
		"brief team updates so waiting teammates stay active. Mind teammate token budgets in " +
		"team/token_balance and prefer assigning new tasks or handoffs to models with more remaining " +
		"budget. All members share one computer: give each member a limit of parallel processes for " +
		"heavy commands (test suites, builds) so the team total fits the computer, and keep message " +
		"traffic low. Messages from " + humanLabel + " come from " +
		"your user. Follow the bridge's guidance during this turn."
)

// joinPromptOptions are the optional parts of a join prompt heading and body.
type joinPromptOptions struct {
	participant string
	role        string
	template    string
	// code replaces CODE in the template when set.
	code    string
	hasCode bool
}

// roleJoinTemplate picks the join instructions for a slot: workers get the worker text,
// leaders the lead text.
func roleJoinTemplate(role string) string {
	if role == "worker" {
		return joinPrompt
	}
	return leadJoinPrompt
}

// formatJoinPrompt prefixes a join prompt with a "<shared job> - <role> (<name>)"
// heading and asks the agent to rename its session to the heading (join_prompt in
// repository.py). An empty template means joinPrompt.
func formatJoinPrompt(brief string, options joinPromptOptions) string {
	body := options.template
	if body == "" {
		body = joinPrompt
	}
	if options.hasCode {
		body = strings.ReplaceAll(body, "CODE", options.code)
	}
	heading := sessionTitle(brief)
	if options.role != "" {
		if heading != "" {
			heading = heading + " - " + options.role
		} else {
			heading = options.role
		}
	}
	if options.participant != "" {
		if heading != "" {
			heading = heading + " (" + options.participant + ")"
		} else {
			heading = options.participant
		}
	}
	if heading == "" {
		return body
	}
	return heading + ": " + body + " " + renameRequest(heading)
}

// renameRequest asks the joining agent to title its session with the prompt heading.
func renameRequest(heading string) string {
	return `If your app lets you rename this session, set its title to "` + heading + `".`
}

// formatOpenJoinPrompt is the shared open-invite join prompt for any agent joining the
// room, with the code filled in when hasCode is set.
func formatOpenJoinPrompt(brief, code string, hasCode bool) string {
	return formatJoinPrompt(brief, joinPromptOptions{role: "worker", template: openJoinPrompt, code: code, hasCode: hasCode})
}
