package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	inputPlaceholder = "Message everyone. @Name addresses one member, ? asks a question."
	maxBriefLines    = 2
	scrollPageSize   = 10
	// sendButtonWidth is the width of the Send button and the gap before it.
	sendButtonWidth = 10
	// messageIndent lines a message body up under its author, after the time column.
	messageIndent = "       "
)

// chatFocus names the part of the conversation screen that receives keys.
type chatFocus int

const (
	focusInput chatFocus = iota
	focusTeam
	focusList
)

// roleOrder sorts members in the team panel: leads first, the human last.
var roleOrder = map[string]int{"lead": 0, "co-lead": 1, "worker": 2, "human": 3}

// chatState holds the open conversation and the transcript read so far.
type chatState struct {
	id       string
	view     ConversationView
	loaded   bool
	messages []TranscriptMessage
	cursor   int64
	polling  bool
	input    textinput.Model
	summary  textinput.Model
	closing  bool
	focus    chatFocus
	selected int
	// scroll counts transcript lines hidden below the view. Zero follows the tail.
	scroll int
	// jumping asks the view to scroll to jumpTarget once the whole transcript has loaded.
	// A jumpTarget of zero is the brief, at the start of the transcript.
	jumping    bool
	jumpTarget int64
	// highlight is the ID of the message a search opened the chat at, or zero.
	highlight int64
}

// humanPost is a parsed input line.
type humanPost struct {
	body string
	kind string
	to   string
}

// newChat returns the state for a conversation that has not been read yet.
func newChat(conversation string, width int) chatState {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = inputPlaceholder
	input.CharLimit = maxBriefLength
	summary := textinput.New()
	summary.Prompt = ""
	summary.Placeholder = "Summary for the team (optional)"
	summary.CharLimit = maxBriefLength
	chat := chatState{id: conversation, input: input, summary: summary}
	chat.resize(width)
	return chat
}

// resize fits the input fields to the center column.
func (c *chatState) resize(width int) {
	c.input.SetWidth(max(width-6-sendButtonWidth, 10))
	c.summary.SetWidth(max(width-24, 10))
}

// updateInput sends a message to the focused input field.
func (c chatState) updateInput(msg tea.Msg) (chatState, tea.Cmd) {
	var command tea.Cmd
	if c.closing {
		c.summary, command = c.summary.Update(msg)
	} else if c.focus == focusInput {
		c.input, command = c.input.Update(msg)
	}
	return c, command
}

// openChat shows a conversation and reads its transcript from the start.
func (m model) openChat(conversation string) (tea.Model, tea.Cmd) {
	m.screen = screenChat
	_, center, _ := m.columnWidths()
	m.chat = newChat(conversation, center)
	m.inviteWindow = inviteState{}
	m.status = ""
	for index, item := range m.list.items {
		if item.Conversation == conversation {
			m.list.selected = index
		}
	}
	focus := m.chat.input.Focus()
	m, poll := m.pollChat()
	return m, tea.Batch(focus, poll)
}

// pollChat requests the transcript after the cursor unless a request is in flight.
func (m model) pollChat() (model, tea.Cmd) {
	if m.chat.polling {
		return m, nil
	}
	m.chat.polling = true
	request := ShowRequest{Directory: m.directory, Conversation: m.chat.id, After: m.chat.cursor}
	client := m.client
	return m, func() tea.Msg {
		view, err := client.Show(request)
		return showMsg{conversation: request.Conversation, after: request.After, view: view, err: err}
	}
}

// onShow appends a new transcript page, keeps a scrolled view in place and reads on while
// the core reports more messages.
func (m model) onShow(msg showMsg) (tea.Model, tea.Cmd) {
	if msg.conversation != m.chat.id {
		return m, nil
	}
	m.chat.polling = false
	if msg.err != nil {
		return m.fail(msg.err), nil
	}
	if msg.after != m.chat.cursor {
		return m, nil
	}
	if m.chat.scroll > 0 {
		_, center, _ := m.columnWidths()
		m.chat.scroll += len(messageLines(msg.view.Messages, msg.view.Status.Members, center, 0))
	}
	m.chat.messages = append(m.chat.messages, msg.view.Messages...)
	m.chat.cursor = msg.view.Cursor
	m.chat.view = msg.view
	m.chat.loaded = true
	m = m.succeed()
	if msg.view.More {
		return m.pollChat()
	}
	if m.chat.jumping {
		m = m.scrollToTarget()
	}
	return m, nil
}

// scrollToTarget scrolls the transcript so the target message starts at the top of the
// view and marks it. A target that is not in the transcript, such as the brief, scrolls to
// the start.
func (m model) scrollToTarget() model {
	m.chat.jumping = false
	_, center, _ := m.columnWidths()
	members := m.chat.view.Status.Members
	total := len(messageLines(m.chat.messages, members, center, 0))
	top := 0
	for index, message := range m.chat.messages {
		if message.ID == m.chat.jumpTarget {
			top = len(messageLines(m.chat.messages[:index], members, center, 0))
			m.chat.highlight = message.ID
		}
	}
	height := m.transcriptHeight(center)
	m.chat.scroll = max(min(total-top-height, total-height), 0)
	return m
}

// transcriptHeight returns the rows the transcript gets between the header and the
// message line.
func (m model) transcriptHeight(width int) int {
	bodyHeight := max(m.height-2, 1)
	return max(bodyHeight-len(m.chatHeader(width))-len(m.composer(width)), 1)
}

// chatKey handles a key in the conversation view. Keys that work everywhere come first,
// then the keys of the focused part.
func (m model) chatKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.chat.closing {
		return m.closingKey(msg)
	}
	switch msg.String() {
	case "tab":
		return m.setChatFocus(m.chat.focus + 1)
	case "shift+tab":
		return m.setChatFocus(m.chat.focus + 2)
	case "ctrl+x":
		return m.startClosing()
	case "ctrl+p":
		return m.openInvite(""), nil
	case "ctrl+l":
		return m.prefillLaunch()
	case "ctrl+r":
		return m.reissueSelected()
	case "pgup", "pgdown":
		m.chat.scrollBy(msg.String())
		return m, nil
	}
	switch m.chat.focus {
	case focusTeam:
		return m.teamKey(msg)
	case focusList:
		return m.chatListKey(msg)
	}
	return m.inputKey(msg)
}

// setChatFocus moves keys to the input, the team panel or the chat list, in that cycle.
func (m model) setChatFocus(focus chatFocus) (tea.Model, tea.Cmd) {
	m.chat.focus = focus % 3
	if m.chat.focus == focusInput {
		return m, m.chat.input.Focus()
	}
	m.chat.input.Blur()
	return m, nil
}

// inputKey handles a key while the message line has focus.
func (m model) inputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return m.submitInput()
	case "esc":
		return m.setChatFocus(focusList)
	case "up", "down":
		m.chat.scrollBy(msg.String())
		return m, nil
	}
	var command tea.Cmd
	m.chat, command = m.chat.updateInput(msg)
	return m, command
}

// teamKey handles a key while the team panel has focus.
func (m model) teamKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.chat.selected = max(m.chat.selected-1, 0)
	case "down", "j":
		m.chat.selected = min(m.chat.selected+1, max(len(m.slotNames())-1, 0))
	case "c":
		return m.copyMemberPrompt()
	case "r":
		return m.reissueSelected()
	case "l":
		return m.prefillLaunch()
	case "i":
		m.chat.input.SetValue("/invite ")
		m.chat.input.CursorEnd()
		next, command := m.setChatFocus(focusInput)
		return next.(model).inform("Type the new member's name and press enter."), command
	case "x":
		return m.startClosing()
	case "esc":
		return m.setChatFocus(focusInput)
	}
	return m, nil
}

// chatListKey handles a key while the chat list has focus inside a conversation.
func (m model) chatListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "n":
		return m.openForm()
	case "/", "a", "v":
		return m.listAction(msg.String())
	case "esc":
		return m.setChatFocus(focusInput)
	case "q":
		return m, tea.Quit
	}
	return m.listNavigate(msg.String())
}

// copyMemberPrompt copies the selected member's join prompt, or asks for a new code when this
// session has none for that member.
func (m model) copyMemberPrompt() (tea.Model, tea.Cmd) {
	name := m.selectedMember()
	if name == "" {
		return m, nil
	}
	member := m.chat.view.Status.Members[name]
	if member.State != "invited" || member.LaunchState != "" {
		return m.inform(cleanLine(name) + " has no join prompt to copy."), nil
	}
	if _, found := m.knownInvitation(name); !found {
		return m.inform("Getting a new code for " + cleanLine(name) + "."), m.invite(name)
	}
	return m.copyPrompt(name)
}

// scrollBy moves the transcript view. The view clamps the offset when it draws.
func (c *chatState) scrollBy(key string) {
	switch key {
	case "up":
		c.scroll++
	case "down":
		c.scroll--
	case "pgup":
		c.scroll += scrollPageSize
	case "pgdown":
		c.scroll -= scrollPageSize
	}
	c.scroll = max(c.scroll, 0)
}

// startClosing asks for an optional summary before the conversation closes.
func (m model) startClosing() (tea.Model, tea.Cmd) {
	m.chat.closing = true
	m.chat.input.Blur()
	return m, m.chat.summary.Focus()
}

// closingKey handles a key while the close confirmation shows.
func (m model) closingKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.chat.closing = false
		m.chat.summary.Blur()
		m.chat.focus = focusInput
		return m, m.chat.input.Focus()
	case "enter":
		request := CloseRequest{
			Directory: m.directory, Conversation: m.chat.id,
			Summary: strings.TrimSpace(m.chat.summary.Value()),
		}
		m.chat.closing = false
		m.chat.summary.Reset()
		m.chat.summary.Blur()
		m.chat.focus = focusInput
		client := m.client
		return m.inform("Closing."), tea.Batch(m.chat.input.Focus(), func() tea.Msg {
			_, err := client.Close(request)
			return closedMsg{err: err}
		})
	}
	var command tea.Cmd
	m.chat, command = m.chat.updateInput(msg)
	return m, command
}

// onClosed reports the result of a close request and reads the summary message.
func (m model) onClosed(msg closedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.fail(msg.err), nil
	}
	m = m.inform("Chat closed.")
	m, poll := m.pollChat()
	return m, tea.Batch(poll, m.loadList())
}

// submitInput runs a slash command or posts the input line as the human.
func (m model) submitInput() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.chat.input.Value())
	if text == "" {
		return m, nil
	}
	if strings.HasPrefix(text, "/") {
		return m.runCommand(text)
	}
	post, err := parsePost(text, m.chat.view.Status.Members)
	if err != nil {
		return m.inform(err.Error()), nil
	}
	m.chat.input.Reset()
	request := PostRequest{
		Directory: m.directory, Conversation: m.chat.id, SubmissionID: submissionID(),
		Body: post.body, Kind: post.kind, To: post.to,
	}
	client := m.client
	return m, func() tea.Msg {
		_, err := client.Post(request)
		return postedMsg{err: err}
	}
}

// onPosted shows a failed post and otherwise reads the new message at once.
func (m model) onPosted(msg postedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.fail(msg.err), nil
	}
	m.chat.scroll = 0
	return m.pollChat()
}

// parsePost reads an optional leading ? (a question) and an optional @Name addressee that
// must name a member other than the human. Both orders work: "?@Lead text" and "@Lead ?text".
func parsePost(text string, members map[string]MemberStatus) (humanPost, error) {
	post := humanPost{kind: "note"}
	text = post.takeQuestion(text)
	if strings.HasPrefix(text, "@") {
		name, rest, err := addressee(text[1:], members)
		if err != nil {
			return humanPost{}, err
		}
		post.to = name
		text = post.takeQuestion(rest)
	}
	post.body = strings.TrimSpace(text)
	if post.body == "" {
		return humanPost{}, errors.New("type a message after the name or ?")
	}
	return post, nil
}

// takeQuestion removes a leading ? and marks the post as a question.
func (p *humanPost) takeQuestion(text string) string {
	text = strings.TrimSpace(text)
	if rest, found := strings.CutPrefix(text, "?"); found {
		p.kind = "question"
		return rest
	}
	return text
}

// addressee matches the longest member name at the start of text, so names with spaces work.
func addressee(text string, members map[string]MemberStatus) (string, string, error) {
	best := ""
	for name, member := range members {
		if member.Role == "human" || len(name) > len(text) || len(name) <= len(best) {
			continue
		}
		if !strings.EqualFold(text[:len(name)], name) {
			continue
		}
		if len(text) == len(name) || text[len(name)] == ' ' {
			best = name
		}
	}
	if best == "" {
		word, _, _ := strings.Cut(text, " ")
		return "", "", fmt.Errorf("no member named @%s in this conversation", cleanLine(word))
	}
	return best, text[len(best):], nil
}

// runCommand handles /invite Name and /launch Name plugin.
func (m model) runCommand(text string) (tea.Model, tea.Cmd) {
	command, argument, _ := strings.Cut(text, " ")
	argument = strings.TrimSpace(argument)
	switch {
	case command == "/invite" && argument != "":
		m.chat.input.Reset()
		return m, m.invite(argument)
	case command == "/launch" && argument != "":
		cut := strings.LastIndex(argument, " ")
		if cut < 0 {
			return m.inform("Usage: /launch Name plugin"), nil
		}
		m.chat.input.Reset()
		return m, m.launch(strings.TrimSpace(argument[:cut]), argument[cut+1:])
	}
	return m.inform("Commands: /invite Name, /launch Name plugin"), nil
}

// invite adds or reissues a slot and then shows its join prompt.
func (m model) invite(name string) tea.Cmd {
	request := InviteRequest{Directory: m.directory, Conversation: m.chat.id, Name: name}
	client := m.client
	return func() tea.Msg {
		invitation, err := client.Invite(request)
		return invitedMsg{conversation: request.Conversation, invitation: invitation, err: err}
	}
}

// launch fills a slot from a plugin.
func (m model) launch(name string, plugin string) tea.Cmd {
	request := LaunchRequest{
		Directory: m.directory, Conversation: m.chat.id, Name: name, Plugin: plugin,
	}
	client := m.client
	return func() tea.Msg {
		queued, err := client.Launch(request)
		return launchedMsg{name: name, queued: queued, err: err}
	}
}

// onLaunched reports the launch state and drops the slot's old join code.
func (m model) onLaunched(msg launchedMsg) model {
	if msg.err != nil {
		return m.fail(msg.err)
	}
	delete(m.invitations[m.chat.id], foldName(msg.name))
	return m.inform(fmt.Sprintf("Launch for %s is %s.", cleanLine(msg.name), cleanLine(msg.queued.State)))
}

// prefillLaunch puts a launch command for the selected member into the message line.
func (m model) prefillLaunch() (tea.Model, tea.Cmd) {
	name := m.selectedMember()
	if name == "" {
		return m.inform("Select a member in the team panel first."), nil
	}
	plugin := "plugin"
	if len(m.plugins) > 0 {
		plugin = m.plugins[0]
	}
	m.chat.input.SetValue("/launch " + name + " " + plugin)
	m.chat.input.CursorEnd()
	next, command := m.setChatFocus(focusInput)
	return next.(model).inform("Edit the plugin name if needed and press enter."), command
}

// reissueSelected asks for a new join code for the selected member.
func (m model) reissueSelected() (tea.Model, tea.Cmd) {
	name := m.selectedMember()
	if name == "" {
		return m.inform("Select a member in the team panel first."), nil
	}
	return m.inform("Getting a new code for " + cleanLine(name) + "."), m.invite(name)
}

// onInvited keeps the new code in memory and shows its join prompt.
func (m model) onInvited(msg invitedMsg) model {
	if msg.err != nil {
		return m.fail(msg.err)
	}
	if m.invitations[msg.conversation] == nil {
		m.invitations[msg.conversation] = map[string]JoinInvitation{}
	}
	m.invitations[msg.conversation][foldName(msg.invitation.Participant)] = msg.invitation
	if msg.conversation != m.chat.id {
		return m
	}
	m = m.openInvite(msg.invitation.Participant)
	delete(m.inviteWindow.copied, foldName(msg.invitation.Participant))
	m.status = "New code for " + cleanLine(msg.invitation.Participant) + ". It works once and expires in one hour."
	return m
}

// slotNames returns the member names other than the human in team order.
func (m model) slotNames() []string {
	var names []string
	for _, name := range sortedMembers(m.chat.view.Status.Members) {
		if m.chat.view.Status.Members[name].Role != "human" {
			names = append(names, name)
		}
	}
	return names
}

// selectedMember returns the member selected in the team panel, or "" when there is none.
func (m model) selectedMember() string {
	names := m.slotNames()
	if len(names) == 0 {
		return ""
	}
	return names[min(max(m.chat.selected, 0), len(names)-1)]
}

// sortedMembers orders member names by role, then by name.
func sortedMembers(members map[string]MemberStatus) []string {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	slices.SortFunc(names, func(left, right string) int {
		if order := roleOrder[members[left].Role] - roleOrder[members[right].Role]; order != 0 {
			return order
		}
		return strings.Compare(foldName(left), foldName(right))
	})
	return names
}

// mainHelp returns the key hints for the bottom bar of the main screen.
func (m model) mainHelp() []hint {
	mouse := hint{"ctrl+o", m.mouseHint()}
	switch {
	case m.screen == screenHome:
		return append([]hint{{"↑↓", "choose"}, {"⏎", "open"}, {"n", "new chat"}}, append(m.listHints(), mouse, hint{"q", "quit"})...)
	case m.inviteWindow.open:
		return []hint{{"c", "copy and go to next"}, {"↑↓", "select"}, {"r", "new code"}, {"esc", "done"}}
	case m.chat.closing:
		return []hint{{"⏎", "close the chat"}, {"esc", "cancel"}}
	case m.chat.focus == focusTeam:
		return []hint{{"↑↓", "member"}, {"c", "copy prompt"}, {"r", "new code"}, {"i", "invite"},
			{"l", "launch"}, {"x", "close chat"}, {"tab", "chat list"}}
	case m.chat.focus == focusList:
		return append([]hint{{"↑↓", "choose"}, {"⏎", "open"}, {"n", "new chat"}}, append(m.listHints(), hint{"tab", "message"}, mouse)...)
	}
	return []hint{{"⏎", "send"}, {"tab", "team"}, {"↑↓", "scroll"}, {"ctrl+p", "join prompts"},
		{"ctrl+x", "close chat"}, mouse}
}

// listHints returns the search, archive and view hints of the chat list.
func (m model) listHints() []hint {
	if m.list.archived {
		return []hint{{"/", "search"}, {"a", "unarchive"}, {"v", "active chats"}}
	}
	return []hint{{"/", "search"}, {"a", "archive"}, {"v", "archived chats"}}
}

// mouseHint names what ctrl+o does now.
func (m model) mouseHint() string {
	if m.mouseOff {
		return "mouse on"
	}
	return "mouse off"
}

// chatView draws the header, the transcript and the message line in the center column.
func (m model) chatView(width int, height int) []string {
	header := m.chatHeader(width)
	footer := m.composer(width)
	transcriptHeight := max(height-len(header)-len(footer), 1)
	lines := append(header, m.visibleTranscript(width, transcriptHeight)...)
	for len(lines) < height-len(footer) {
		lines = append(lines, "")
	}
	return append(lines, footer...)
}

// chatHeader draws the title, a state badge and the start of the brief.
func (m model) chatHeader(width int) []string {
	view := m.chat.view
	header := m.headerLayout(width)
	left := " " + brightStyle.Render(header.title) + "  " + header.state
	if header.detail != "" {
		left += "  " + dimStyle.Render(header.detail)
	}
	actions := ""
	if header.actions {
		actions, _ = m.headerButtons()
	}
	gap := max(width-1-ansi.StringWidth(left)-ansi.StringWidth(actions), 1)
	lines := []string{left + strings.Repeat(" ", gap) + actions}
	shown := header.title
	// Under the title, show the part of the brief the title line does not already hold.
	rest := view.Brief
	if first, after, found := strings.Cut(view.Brief, "\n"); found && shown == cleanLine(first) {
		rest = after
	}
	if shown != strings.TrimSpace(clean(view.Brief)) && strings.TrimSpace(rest) != "" {
		brief := wrapLines(rest, width-2, " ")
		if len(brief) > maxBriefLines {
			brief = brief[:maxBriefLines]
			last := brief[maxBriefLines-1]
			brief[maxBriefLines-1] = ansi.Truncate(last, width-3, "") + "…"
		}
		for _, line := range brief {
			lines = append(lines, dimStyle.Render(line))
		}
	}
	return append(lines, rule(width))
}

// headerParts is what fits on the header line: the title as shown, the state badge, the
// detail text and whether the action buttons show.
type headerParts struct {
	title   string
	state   string
	detail  string
	actions bool
}

// minTitleWidth is the narrowest title the header keeps before it drops other parts.
const minTitleWidth = 20

// headerLayout decides what fits on the header line. The detail gives way first, then the
// action buttons, so the title stays readable.
func (m model) headerLayout(width int) headerParts {
	view := m.chat.view
	parts := headerParts{title: cleanLine(strings.SplitN(view.Brief, "\n", 2)[0]), state: badge("OPEN", colorOK)}
	switch {
	case !m.chat.loaded:
		parts.state, parts.title = badge("LOADING", colorDim), "Loading the chat"
	case view.Archived:
		parts.state = badge("ARCHIVED", colorDim)
	case !view.Status.Open:
		parts.state = badge("CLOSED", colorDim)
	}
	detail := fmt.Sprintf("%d messages", len(m.chat.messages))
	if view.Created != nil {
		detail = "started " + time.Unix(*view.Created, 0).Format("15:04") + " · " + detail
	}
	actions, _ := m.headerButtons()
	base := width - 6 - ansi.StringWidth(parts.state)
	actionsWidth := 0
	if actions != "" {
		actionsWidth = ansi.StringWidth(actions) + 2
	}
	room := base - actionsWidth - ansi.StringWidth(detail) - 2
	switch {
	case room >= minTitleWidth || (actions == "" && room >= 10):
		parts.detail, parts.actions = detail, actions != ""
	case base-actionsWidth >= minTitleWidth:
		room, parts.actions = base-actionsWidth, actions != ""
	default:
		room = base
	}
	parts.title = ansi.Truncate(parts.title, max(room, 10), "…")
	return parts
}

// headerButtons draws the chat actions at the right end of the header. A closed chat can
// move to the archive or back.
func (m model) headerButtons() (string, []span) {
	switch {
	case !m.chat.loaded:
		return "", nil
	case m.chat.view.Archived:
		return m.buttons(button{id: "archive", label: "Unarchive"})
	case !m.chat.view.Status.Open:
		return m.buttons(button{id: "archive", label: "Archive"})
	}
	return m.buttons(button{id: "prompts", label: "Join prompts"}, button{id: "close", label: "Close chat"})
}

// sendButton draws the button at the right end of the message line.
func (m model) sendButton() (string, []span) {
	return m.buttons(button{id: "send", label: "Send ⏎", primary: strings.TrimSpace(m.chat.input.Value()) != ""})
}

// composer draws the message line with a hint that says what enter will send.
func (m model) composer(width int) []string {
	if m.chat.closing {
		return []string{
			rule(width),
			" " + warnStyle.Render("Close this chat?") + " " + m.chat.summary.View(),
			" " + dimStyle.Render("Participants can still read it. Posting stops and unused codes expire."),
		}
	}
	marker := dimStyle.Render("›")
	if m.chat.focus == focusInput {
		marker = accentStyle.Render("›")
	}
	send, _ := m.sendButton()
	return []string{
		rule(width),
		pad(" "+marker+" "+m.chat.input.View(), width-1-ansi.StringWidth(send)) + send,
		"   " + dimStyle.Render(m.inputPreview()),
	}
}

// inputPreview describes what the current message line would do.
func (m model) inputPreview() string {
	text := strings.TrimSpace(m.chat.input.Value())
	if !m.chat.view.Status.Open && m.chat.loaded {
		return "This chat is closed."
	}
	switch {
	case text == "":
		return "⏎ send · @ address one member · ? ask a question"
	case strings.HasPrefix(text, "/"):
		return "command · /invite Name · /launch Name plugin"
	}
	post, err := parsePost(text, m.chat.view.Status.Members)
	if err != nil {
		return err.Error()
	}
	target := "everyone"
	if post.to != "" {
		target = cleanLine(post.to)
	}
	return post.kind + " to " + target + " · ⏎ send"
}

// visibleTranscript returns the transcript lines that fit, honoring the scroll offset.
func (m model) visibleTranscript(width int, height int) []string {
	lines := messageLines(m.chat.messages, m.chat.view.Status.Members, width, m.chat.highlight)
	if len(lines) == 0 && m.chat.loaded {
		lines = []string{"", " " + dimStyle.Render("No messages yet. The lead posts tasks here once it joins.")}
	}
	hidden := min(m.chat.scroll, max(len(lines)-height, 0))
	end := len(lines) - hidden
	start := max(end-height, 0)
	return lines[start:end]
}

// messageLines draws each message as a header line and its wrapped body, with a blank line
// between messages. The human's messages sit on a band, and the highlighted message on the
// selection color with a marker. Highlighting never changes the number of lines.
func messageLines(messages []TranscriptMessage, members map[string]MemberStatus, width int, highlight int64) []string {
	var lines []string
	for _, message := range messages {
		role := members[message.Label].Role
		author := cleanLine(message.Label)
		if role == "human" {
			author = "You"
		}
		header := " " + dimStyle.Render(time.Unix(message.Created, 0).Format("15:04")) + "  " +
			roleStyle(role).Render(author)
		switch {
		case message.ReplyTo != nil:
			header += dimStyle.Render(fmt.Sprintf(" ↳ re #%d", *message.ReplyTo))
		case message.To != "":
			header += dimStyle.Render(" → " + cleanLine(message.To))
		case role == "human":
			header += dimStyle.Render(" to everyone")
		}
		if label := kindLabel(message); label != "" {
			header += "  " + label
		}
		body := wrapLines(message.Body, width-1, messageIndent)
		switch {
		case message.ID == highlight:
			lines = append(lines, fill(accentStyle.Render("▎")+header[1:], width, colorSelect))
			for _, line := range body {
				lines = append(lines, fill(textStyle.Render(line), width, colorSelect))
			}
		case role == "human":
			lines = append(lines, fill(header, width, colorBand))
			for _, line := range body {
				lines = append(lines, fill(textStyle.Render(line), width, colorBand))
			}
		default:
			lines = append(lines, header)
			for _, line := range body {
				lines = append(lines, textStyle.Render(line))
			}
		}
		lines = append(lines, "")
	}
	return lines
}

// kindLabel draws the tag for a message kind. Plain notes have none.
func kindLabel(message TranscriptMessage) string {
	kind := cleanLine(message.Kind)
	switch kind {
	case "", "note":
		return ""
	case "task", "handoff":
		return tag(fmt.Sprintf("%s #%d", strings.ToUpper(kind), message.ID), colorAccent)
	case "blocker":
		return badge("BLOCKED", colorWarn)
	case "done", "accept":
		return badge(strings.ToUpper(kind), colorOK)
	}
	return tag(strings.ToUpper(kind), colorText)
}

// teamView draws each member with a state glyph and detail, then the open tasks.
func (m model) teamView(width int, height int) []string {
	members := m.chat.view.Status.Members
	lines := []string{" " + headStyle.Render("TEAM"), ""}
	selected := m.selectedMember()
	var tasks []string
	for _, name := range sortedMembers(members) {
		member := members[name]
		glyph, detail := memberState(member)
		label := cleanLine(name)
		if member.Role == "human" {
			label = "You"
		}
		block := []string{
			glyph + " " + roleStyle(member.Role).Render(label) + " " + dimStyle.Render(member.Role),
			"  " + dimStyle.Render(detail),
		}
		if member.State == "invited" && member.LaunchState == "" {
			copyButton, _ := m.buttons(button{id: "copy:" + name, label: "Copy join prompt"})
			block = append(block, "  "+copyButton)
		}
		switch {
		case name == selected && m.chat.focus == focusTeam:
			for index, line := range block {
				block[index] = fill(accentStyle.Render("▎")+line, width, colorSelect)
			}
		case m.hover == "member:"+name:
			for index, line := range block {
				block[index] = fill(dimStyle.Render("▎")+line, width, colorBand)
			}
		default:
			for index, line := range block {
				block[index] = " " + line
			}
		}
		lines = append(lines, block...)
		lines = append(lines, "")
		for _, task := range member.OpenTasks {
			state := dimStyle.Render("open")
			if task.Blocked != nil && *task.Blocked {
				state = warnStyle.Render("blocked")
			} else if member.State == "invited" {
				state = dimStyle.Render("waiting to join")
			}
			tasks = append(tasks, " "+accentStyle.Render(fmt.Sprintf("#%d", task.ID))+" "+label+" "+state)
		}
	}
	var footer []string
	if len(tasks) > 0 {
		footer = append([]string{" " + headStyle.Render("OPEN TASKS")}, tasks...)
	}
	if m.chat.view.Status.Open {
		invite, _ := m.buttons(button{id: "invite-member", label: "+ Invite member"})
		footer = append(footer, "", " "+invite)
	}
	for len(lines)+len(footer) < height {
		lines = append(lines, "")
	}
	return append(lines, footer...)
}

// teamRow places one member block in the team panel.
type teamRow struct {
	name string
	top  int
	size int
	// copyRow is the row of the "copy join prompt" line, or -1.
	copyRow int
}

// teamRows returns where teamView draws each member, so a click can find the member.
func (m model) teamRows() []teamRow {
	members := m.chat.view.Status.Members
	var rows []teamRow
	top := 2
	for _, name := range sortedMembers(members) {
		member := members[name]
		row := teamRow{name: name, top: top, size: 2, copyRow: -1}
		if member.State == "invited" && member.LaunchState == "" {
			row.copyRow = top + 2
			row.size = 3
		}
		rows = append(rows, row)
		top += row.size + 1
	}
	return rows
}

// memberState returns a glyph that reads without color and a one-line detail.
func memberState(member MemberStatus) (string, string) {
	blockedTask := int64(0)
	for _, task := range member.OpenTasks {
		if task.Blocked != nil && *task.Blocked {
			blockedTask = task.ID
		}
	}
	switch {
	case member.Role == "human":
		return roleStyle("human").Render("◆"), "you, through this window"
	case blockedTask != 0:
		return warnStyle.Render("▲"), fmt.Sprintf("blocked on task #%d", blockedTask)
	case member.State == "blocked":
		return warnStyle.Render("▲"), "blocked"
	case member.LaunchState == "failed" || member.LaunchState == "timed_out" || member.LaunchState == "cancelled":
		return dimStyle.Render("✕"), "launch " + strings.ReplaceAll(member.LaunchState, "_", " ")
	case member.State == "invited" && member.LaunchState != "":
		return accentStyle.Render("◐"), "plugin " + member.LaunchState
	case member.State == "invited":
		return dimStyle.Render("◌"), "not joined yet"
	case member.State == "done":
		return dimStyle.Render("○"), "done"
	}
	detail := member.State
	if member.IdleSeconds != nil {
		detail += " · active " + idle(*member.IdleSeconds) + " ago"
	}
	return okStyle.Render("●"), detail
}
