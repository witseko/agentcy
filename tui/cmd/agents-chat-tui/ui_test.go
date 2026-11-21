package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const testConversation = "0123456789abcdef01234567"

// fakeOperator records requests and returns canned results.
type fakeOperator struct {
	list     ConversationList
	view     ConversationView
	showErr  error
	plugins  PluginList
	creates  []CreateRequest
	launches []LaunchRequest
	posts    []PostRequest
	closes   []CloseRequest
	invites  []InviteRequest
	shows    []ShowRequest
}

func (f *fakeOperator) Plugins() (PluginList, error) { return f.plugins, nil }

func (f *fakeOperator) List(ListRequest) (ConversationList, error) { return f.list, nil }

func (f *fakeOperator) Show(request ShowRequest) (ConversationView, error) {
	f.shows = append(f.shows, request)
	return f.view, f.showErr
}

func (f *fakeOperator) Create(request CreateRequest) (CreatedConversation, error) {
	f.creates = append(f.creates, request)
	invitations := []JoinInvitation{{Participant: request.Lead, Role: "lead", Code: "LEAD-CODE", JoinPrompt: "join LEAD-CODE"}}
	for _, name := range request.Invitees {
		invitations = append(invitations, JoinInvitation{Participant: name, Role: "worker", Code: "W", JoinPrompt: "join W"})
	}
	return CreatedConversation{Conversation: testConversation, Invitations: invitations}, nil
}

func (f *fakeOperator) Invite(request InviteRequest) (JoinInvitation, error) {
	f.invites = append(f.invites, request)
	return JoinInvitation{Participant: request.Name, Role: "worker", Code: "NEW", JoinPrompt: "join NEW"}, nil
}

func (f *fakeOperator) Launch(request LaunchRequest) (LaunchQueued, error) {
	f.launches = append(f.launches, request)
	return LaunchQueued{AssignmentID: "a1", State: "queued"}, nil
}

func (f *fakeOperator) Post(request PostRequest) (PostedMessage, error) {
	f.posts = append(f.posts, request)
	return PostedMessage{MessageID: 1}, nil
}

func (f *fakeOperator) Close(request CloseRequest) (ClosedConversation, error) {
	f.closes = append(f.closes, request)
	return ClosedConversation{Closed: true}, nil
}

// press sends a named key, such as "enter", "tab" or "ctrl+x".
func press(t *testing.T, m model, key string) (model, tea.Cmd) {
	t.Helper()
	keys := map[string]tea.KeyPressMsg{
		"enter":     {Code: tea.KeyEnter},
		"tab":       {Code: tea.KeyTab},
		"shift+tab": {Code: tea.KeyTab, Mod: tea.ModShift},
		"esc":       {Code: tea.KeyEscape},
		"up":        {Code: tea.KeyUp},
		"down":      {Code: tea.KeyDown},
		"right":     {Code: tea.KeyRight},
		"ctrl+x":    {Code: 'x', Mod: tea.ModCtrl},
		"ctrl+p":    {Code: 'p', Mod: tea.ModCtrl},
		"ctrl+s":    {Code: 's', Mod: tea.ModCtrl},
		"ctrl+o":    {Code: 'o', Mod: tea.ModCtrl},
	}
	msg, named := keys[key]
	if !named {
		runes := []rune(key)
		msg = tea.KeyPressMsg{Code: runes[0], Text: key}
	}
	next, command := m.Update(msg)
	return next.(model), command
}

// typeText sends each character of text as a key press.
func typeText(t *testing.T, m model, text string) model {
	t.Helper()
	for _, character := range text {
		next, _ := m.Update(tea.KeyPressMsg{Code: character, Text: string(character)})
		m = next.(model)
	}
	return m
}

// drive executes a command and its batched commands and feeds every message that arrives
// promptly back into the model. Timers and cursor blinks do not arrive in time and are
// dropped, so the poll clock does not run during a test.
func drive(t *testing.T, m model, command tea.Cmd) model {
	t.Helper()
	for _, msg := range collect(command) {
		next, more := m.Update(msg)
		m = drive(t, next.(model), more)
	}
	return m
}

// collect runs a command tree and returns the messages that arrive within a short wait.
func collect(command tea.Cmd) []tea.Msg {
	if command == nil {
		return nil
	}
	arrived := make(chan tea.Msg, 1)
	go func() { arrived <- command() }()
	select {
	case msg := <-arrived:
		if batch, isBatch := msg.(tea.BatchMsg); isBatch {
			var messages []tea.Msg
			for _, inner := range batch {
				messages = append(messages, collect(inner)...)
			}
			return messages
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

// chatModel returns a model showing a conversation with a lead, a worker and the human.
func chatModel(t *testing.T, fake *fakeOperator) model {
	t.Helper()
	fake.view = ConversationView{
		Conversation: testConversation,
		Brief:        "Review the retry rule.",
		Status: ConversationStatus{Open: true, Members: map[string]MemberStatus{
			"Lead":     {Role: "lead", State: "joined"},
			"Reviewer": {Role: "worker", State: "invited"},
			"Human":    {Role: "human", State: "human"},
		}},
	}
	m := newModel(fake, "/work")
	next, command := m.openChat(testConversation)
	return drive(t, next.(model), command)
}

func TestResizeBeforeAnyScreenOpens(t *testing.T) {
	m := newModel(&fakeOperator{}, "/work")

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, command := press(t, next.(model), "n")
	m = drive(t, m, command)
	next, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	if lines := strings.Count(next.(model).View().Content, "\n") + 1; lines != 24 {
		t.Fatalf("view has %d lines, want 24", lines)
	}
}

func TestListNavigationOpensTheSelectedChat(t *testing.T) {
	fake := &fakeOperator{list: ConversationList{Conversations: []ConversationSummary{
		{Conversation: "a", Title: "first"}, {Conversation: "b", Title: "second"}, {Conversation: "c", Title: "third"},
	}}}
	m := drive(t, newModel(fake, "/work"), newModel(fake, "/work").loadList())

	m, _ = press(t, m, "down")
	m, _ = press(t, m, "j")
	m, _ = press(t, m, "up")
	m, command := press(t, m, "enter")
	m = drive(t, m, command)

	if m.screen != screenChat || m.chat.id != "b" {
		t.Fatalf("screen %v chat %q, want the chat view of b", m.screen, m.chat.id)
	}
	if len(fake.shows) != 1 || fake.shows[0].After != 0 {
		t.Fatalf("shows = %+v, want one read from cursor zero", fake.shows)
	}
}

func TestNewChatCreatesThenLaunchesPluginSlots(t *testing.T) {
	fake := &fakeOperator{plugins: PluginList{Plugins: []PluginSummary{{Name: "glm", Model: "glm-5"}}}}
	m := newModel(fake, "/work")
	m, command := press(t, m, "n")
	m = drive(t, m, command)
	m = typeText(t, m, "Check retries")
	for range 3 { // brief, lead name, worker name
		m, _ = press(t, m, "tab")
	}
	m, _ = press(t, m, "right") // worker source: manual -> glm
	m, _ = press(t, m, "tab")   // Create
	m, command = press(t, m, "enter")
	m = drive(t, m, command)

	if len(fake.creates) != 1 {
		t.Fatalf("creates = %+v", fake.creates)
	}
	created := fake.creates[0]
	if created.Brief != "Check retries" || created.Lead != "Lead" || len(created.Invitees) != 1 || created.Invitees[0] != "Worker" {
		t.Fatalf("create request = %+v", created)
	}
	if len(fake.launches) != 1 || fake.launches[0].Name != "Worker" || fake.launches[0].Plugin != "glm" || fake.launches[0].Conversation != testConversation {
		t.Fatalf("launches = %+v", fake.launches)
	}
	if m.screen != screenChat || m.chat.id != testConversation {
		t.Fatalf("screen %v chat %q, want the new chat", m.screen, m.chat.id)
	}
	codes := m.invitations[testConversation]
	if _, kept := codes["lead"]; !kept || len(codes) != 1 {
		t.Fatalf("kept codes for %v, want only the manual lead", codes)
	}
}

func TestPostAddressesAMemberAsAQuestion(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)

	m.chat.input.SetValue("@reviewer ?Which test fails?")
	m, command := press(t, m, "enter")
	m = drive(t, m, command)
	m.chat.input.SetValue("?@Lead Status?")
	m, command = press(t, m, "enter")
	m = drive(t, m, command)
	m.chat.input.SetValue("Plain note")
	m, command = press(t, m, "enter")
	drive(t, m, command)

	want := []PostRequest{
		{To: "Reviewer", Kind: "question", Body: "Which test fails?"},
		{To: "Lead", Kind: "question", Body: "Status?"},
		{Kind: "note", Body: "Plain note"},
	}
	if len(fake.posts) != len(want) {
		t.Fatalf("posts = %+v", fake.posts)
	}
	for index, post := range fake.posts {
		if post.To != want[index].To || post.Kind != want[index].Kind || post.Body != want[index].Body {
			t.Errorf("post %d = %+v, want %+v", index, post, want[index])
		}
		if !strings.HasPrefix(post.SubmissionID, "ui-") || post.Conversation != testConversation {
			t.Errorf("post %d has submission %q in %q", index, post.SubmissionID, post.Conversation)
		}
	}
}

func TestUnknownAddresseeIsRejected(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)

	m.chat.input.SetValue("@Nobody hello")
	m, command := press(t, m, "enter")
	m = drive(t, m, command)
	m.chat.input.SetValue("@Human hello")
	m, command = press(t, m, "enter")
	m = drive(t, m, command)

	if len(fake.posts) != 0 {
		t.Fatalf("posted %+v", fake.posts)
	}
	if !strings.Contains(m.status, "no member named @Human") {
		t.Fatalf("status = %q", m.status)
	}
	if m.chat.input.Value() != "@Human hello" {
		t.Fatalf("input lost: %q", m.chat.input.Value())
	}
}

func TestTranscriptDropsTerminalControls(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)
	hostile := ConversationView{
		Conversation: testConversation,
		Status:       fake.view.Status,
		Messages: []TranscriptMessage{{
			ID: 1, Label: "Le\x1b[2Jad", Created: 1795000000,
			Body: "\x1b]0;owned\x07hello\x00 \x9b31m world‮!",
		}},
		Cursor: 1,
	}

	next, _ := m.Update(showMsg{conversation: testConversation, after: 0, view: hostile})
	content := next.(model).View().Content

	for _, forbidden := range []string{"\x1b[2J", "\x1b]", "\x07", "\x00", "\x9b", "‮"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("view contains %q", forbidden)
		}
	}
	if !strings.Contains(content, "Lead") || !strings.Contains(content, "hello") {
		t.Errorf("view lost the message text:\n%s", content)
	}
}

func TestCloseNeedsConfirmation(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)

	m, _ = press(t, m, "ctrl+x")
	m, _ = press(t, m, "esc")
	if len(fake.closes) != 0 || m.chat.closing {
		t.Fatalf("cancel closed the chat: %+v", fake.closes)
	}
	m, _ = press(t, m, "ctrl+x")
	m = typeText(t, m, "All done")
	m, command := press(t, m, "enter")
	drive(t, m, command)

	if len(fake.closes) != 1 || fake.closes[0].Summary != "All done" {
		t.Fatalf("closes = %+v", fake.closes)
	}
}

func TestPromptsCopyTheKnownJoinPrompt(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)
	var copied []string
	m.clipboard = func(text string) error {
		copied = append(copied, text)
		return nil
	}

	m, _ = press(t, m, "ctrl+p")
	m, command := press(t, m, "c")
	m = drive(t, m, command)
	if len(copied) != 0 {
		t.Fatalf("copied %v without a known code", copied)
	}
	m, command = press(t, m, "r")
	m = drive(t, m, command)
	m, command = press(t, m, "c")
	drive(t, m, command)

	if len(fake.invites) != 1 || fake.invites[0].Name != "Reviewer" {
		t.Fatalf("invites = %+v", fake.invites)
	}
	if len(copied) != 1 || copied[0] != "join NEW" {
		t.Fatalf("copied = %v", copied)
	}
}

func TestVanishedCoreShowsAStatusAndRetries(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)
	fake.showErr = errCoreUnreachable

	next, command := m.Update(tickMsg(time.Now()))
	m = drive(t, next.(model), command)
	if m.status != offlineStatus || m.chat.polling {
		t.Fatalf("status %q polling %v", m.status, m.chat.polling)
	}

	fake.showErr = nil
	next, command = m.Update(tickMsg(time.Now()))
	m = drive(t, next.(model), command)
	if m.status != "" || len(fake.shows) != 3 {
		t.Fatalf("status %q after %d reads", m.status, len(fake.shows))
	}
}

func TestCreateOpensTheInviteWindowForManualSlots(t *testing.T) {
	fake := &fakeOperator{}
	m := newModel(fake, "/work")
	m, command := press(t, m, "n")
	m = drive(t, m, command)
	m = typeText(t, m, "Check retries")
	m, command = press(t, m, "ctrl+s")
	m = drive(t, m, command)

	if len(fake.creates) != 1 || !m.inviteWindow.open || m.screen != screenChat {
		t.Fatalf("creates %d, invite window %v, screen %v", len(fake.creates), m.inviteWindow.open, m.screen)
	}
	m, _ = press(t, m, "esc")
	if m.inviteWindow.open {
		t.Fatal("esc left the invite window open")
	}
}

func TestTeamPanelCopiesAMemberPrompt(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)
	var copied []string
	m.clipboard = func(text string) error {
		copied = append(copied, text)
		return nil
	}

	m, _ = press(t, m, "tab") // team panel
	m, _ = press(t, m, "down")
	if m.selectedMember() != "Reviewer" {
		t.Fatalf("selected %q, want Reviewer", m.selectedMember())
	}
	m, command := press(t, m, "c") // no code yet: asks for one and opens the invite window
	m = drive(t, m, command)
	if len(fake.invites) != 1 || !m.inviteWindow.open {
		t.Fatalf("invites %+v, invite window %v", fake.invites, m.inviteWindow.open)
	}
	m, command = press(t, m, "c")
	drive(t, m, command)
	if len(copied) != 1 || copied[0] != "join NEW" {
		t.Fatalf("copied = %v", copied)
	}
}

func TestViewLinesFitTheTerminal(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)
	for _, size := range [][2]int{{60, 20}, {90, 30}, {140, 40}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(next.(model).View().Content, "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for index, line := range lines {
			if width := ansi.StringWidth(line); width > size[0] {
				t.Errorf("%dx%d: line %d is %d columns", size[0], size[1], index, width)
			}
		}
	}
}

// click sends a left click at a screen cell.
func click(t *testing.T, m model, x int, y int) (model, tea.Cmd) {
	t.Helper()
	next, command := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return next.(model), command
}

// sized returns the model after a window resize.
func sized(m model, width int, height int) model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return next.(model)
}

func TestClickingAChatInTheListOpensIt(t *testing.T) {
	fake := &fakeOperator{list: ConversationList{Conversations: []ConversationSummary{
		{Conversation: "first", Title: "First"}, {Conversation: testConversation, Title: "Second"},
	}}}
	m := sized(newModel(fake, "/work"), 120, 30)
	m = drive(t, m, m.loadList())

	// Body rows start under the top bar; chats start two rows into the list, three rows each.
	m, command := click(t, m, 4, 1+2+3)
	m = drive(t, m, command)
	if m.screen != screenChat || m.chat.id != testConversation {
		t.Fatalf("screen %v chat %q, want the second chat", m.screen, m.chat.id)
	}
}

func TestClickingTheTeamCopyLineCopiesThePrompt(t *testing.T) {
	fake := &fakeOperator{}
	m := sized(chatModel(t, fake), 140, 30)
	var copied []string
	m.clipboard = func(text string) error {
		copied = append(copied, text)
		return nil
	}
	m.invitations[testConversation] = map[string]JoinInvitation{"reviewer": {Participant: "Reviewer", JoinPrompt: "join R"}}

	var reviewer teamRow
	for _, row := range m.teamRows() {
		if row.name == "Reviewer" {
			reviewer = row
		}
	}
	m, command := click(t, m, 140-5, 1+reviewer.copyRow)
	drive(t, m, command)
	if m.chat.focus != focusTeam || m.selectedMember() != "Reviewer" || len(copied) != 1 || copied[0] != "join R" {
		t.Fatalf("focus %v selected %q copied %v", m.chat.focus, m.selectedMember(), copied)
	}
}

func TestDraggingADividerResizesTheColumn(t *testing.T) {
	m := sized(chatModel(t, &fakeOperator{}), 140, 30)
	m, _ = click(t, m, sidebarWidth, 5)
	next, _ := m.Update(tea.MouseMotionMsg{X: sidebarWidth + 10, Y: 5, Button: tea.MouseLeft})
	m = next.(model)
	next, _ = m.Update(tea.MouseReleaseMsg{X: sidebarWidth + 10, Y: 5})
	m = next.(model)
	next, _ = m.Update(tea.MouseMotionMsg{X: sidebarWidth + 20, Y: 5})
	m = next.(model)
	if m.sidebarCols != sidebarWidth+10 {
		t.Fatalf("sidebar width %d, want %d", m.sidebarCols, sidebarWidth+10)
	}
}

func TestWheelScrollsTheTranscriptAndCtrlOReleasesTheMouse(t *testing.T) {
	m := sized(chatModel(t, &fakeOperator{}), 140, 30)
	next, _ := m.Update(tea.MouseWheelMsg{X: 60, Y: 10, Button: tea.MouseWheelUp})
	m = next.(model)
	if m.chat.scroll != wheelLines {
		t.Fatalf("scroll %d, want %d", m.chat.scroll, wheelLines)
	}
	if m.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatal("mouse capture is off by default")
	}
	m, _ = press(t, m, "ctrl+o")
	if m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("ctrl+o left mouse capture on")
	}
}

func TestClickingAWorkerSourceChangesIt(t *testing.T) {
	fake := &fakeOperator{plugins: PluginList{Plugins: []PluginSummary{{Name: "glm"}}}}
	m := sized(newModel(fake, "/work"), 120, 40)
	m, command := press(t, m, "n")
	m = drive(t, m, command)
	_, fieldOf, focusLine := m.formLayout()
	workerLine := -1
	for line, field := range fieldOf {
		if field == leadFields {
			workerLine = line
		}
	}
	row := workerLine - scrollFirst(len(fieldOf), focusLine, 38)
	m, _ = click(t, m, formLeft(120)+sourceColumn+2, 1+row)
	if m.form.workers[0].source != 1 || m.form.focus != leadFields+1 {
		t.Fatalf("source %d focus %d", m.form.workers[0].source, m.form.focus)
	}
}

func TestButtonsAndKeyHintsAreClickable(t *testing.T) {
	fake := &fakeOperator{}
	m := sized(chatModel(t, fake), 140, 30)
	find := func(m model, id string) zone {
		t.Helper()
		for _, item := range m.zones() {
			if item.id == id {
				return item
			}
		}
		t.Fatalf("no zone %q in %+v", id, m.zones())
		return zone{}
	}

	join := find(m, "prompts")
	m, _ = click(t, m, join.x0, join.y)
	if !m.inviteWindow.open {
		t.Fatal("Join prompts button did not open the invite window")
	}
	done := find(m, "done")
	m, _ = click(t, m, done.x0, done.y)
	if m.inviteWindow.open {
		t.Fatal("Done button left the invite window open")
	}

	m.chat.input.SetValue("hello")
	send := find(m, "send")
	m, command := click(t, m, send.x0, send.y)
	m = drive(t, m, command)
	if len(fake.posts) != 1 || fake.posts[0].Body != "hello" {
		t.Fatalf("posts = %+v", fake.posts)
	}

	closeHint := find(m, "key:ctrl+x")
	m, _ = click(t, m, closeHint.x0, closeHint.y)
	if !m.chat.closing {
		t.Fatal("clicking the ctrl+x hint did not start closing")
	}
}

func TestHoverLightsTheDividerUnderThePointer(t *testing.T) {
	m := sized(chatModel(t, &fakeOperator{}), 140, 30)
	next, _ := m.Update(tea.MouseMotionMsg{X: sidebarWidth, Y: 5})
	m = next.(model)
	if m.hover != "divider:left" || !strings.Contains(m.View().Content, "┃") {
		t.Fatalf("hover %q", m.hover)
	}
}

func TestArrowsMoveBetweenFormRows(t *testing.T) {
	fake := &fakeOperator{plugins: PluginList{Plugins: []PluginSummary{{Name: "glm"}}}}
	m := sized(newModel(fake, "/work"), 120, 40)
	m, command := press(t, m, "n")
	m = drive(t, m, command)
	m, _ = press(t, m, "ctrl+n") // second worker, focused on its name
	m, _ = press(t, m, "up")
	m, _ = press(t, m, "up")
	m, _ = press(t, m, "up") // worker 2 -> worker 1 -> lead -> brief
	if kind, _ := m.form.field(m.form.focus); kind != fieldBrief {
		t.Fatalf("focus %d, want the brief", m.form.focus)
	}
	m = typeText(t, m, "Check retries")
	steps := []struct {
		key  string
		want int
	}{
		{"down", int(fieldLeadName)},
		{"down", leadFields},     // worker 1 name
		{"tab", leadFields + 1},  // worker 1 source
		{"down", leadFields + 3}, // worker 2 source keeps the column
		{"down", leadFields + 4}, // Create
		{"up", leadFields + 2},   // worker 2 name
	}
	for _, step := range steps {
		m, _ = press(t, m, step.key)
		if m.form.focus != step.want {
			t.Fatalf("after %s focus %d, want %d", step.key, m.form.focus, step.want)
		}
	}
	if m.form.brief.Value() != "Check retries" {
		t.Fatalf("brief = %q", m.form.brief.Value())
	}
}
