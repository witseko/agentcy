package main

import (
	"fmt"
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
	archive  ConversationList
	results  SearchResults
	lists    []ListRequest
	archives []ArchiveRequest
	searches []SearchRequest
	view     ConversationView
	showFunc func(ShowRequest) (ConversationView, error)
	showErr  error
	plugins  PluginList
	creates  []CreateRequest
	launches    []LaunchRequest
	retires     []RetireRequest
	roleChanges []SetRoleRequest
	posts       []PostRequest
	closes   []CloseRequest
	invites  []InviteRequest
	shows    []ShowRequest
}

func (f *fakeOperator) Plugins() (PluginList, error) { return f.plugins, nil }

func (f *fakeOperator) List(request ListRequest) (ConversationList, error) {
	f.lists = append(f.lists, request)
	if request.Archived {
		return f.archive, nil
	}
	return f.list, nil
}

func (f *fakeOperator) Archive(request ArchiveRequest) (ArchivedConversation, error) {
	f.archives = append(f.archives, request)
	return ArchivedConversation{Archived: request.Archived}, nil
}

func (f *fakeOperator) Search(request SearchRequest) (SearchResults, error) {
	f.searches = append(f.searches, request)
	return f.results, nil
}

func (f *fakeOperator) Show(request ShowRequest) (ConversationView, error) {
	f.shows = append(f.shows, request)
	if f.showFunc != nil {
		return f.showFunc(request)
	}
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

func (f *fakeOperator) Retire(request RetireRequest) (RetiredParticipant, error) {
	f.retires = append(f.retires, request)
	return RetiredParticipant{Participant: request.Name, Retired: true}, nil
}

func (f *fakeOperator) SetRole(request SetRoleRequest) (ParticipantRoleChanged, error) {
	f.roleChanges = append(f.roleChanges, request)
	return ParticipantRoleChanged{Participant: request.Name, Role: request.Role}, nil
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
	fake := &fakeOperator{view: longChat()}
	m := sized(newModel(fake, "/work"), 140, 30)
	next, command := m.openChat(testConversation)
	m = drive(t, next.(model), command)
	next, _ = m.Update(tea.MouseWheelMsg{X: 60, Y: 10, Button: tea.MouseWheelUp})
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

func TestArchiveKeyMovesAClosedChatAndVSwitchesTheList(t *testing.T) {
	fake := &fakeOperator{
		list: ConversationList{Conversations: []ConversationSummary{
			{Conversation: "open", Title: "Open", Open: true}, {Conversation: "done", Title: "Done"},
		}},
		archive: ConversationList{Conversations: []ConversationSummary{{Conversation: "old", Title: "Old"}}},
	}
	m := sized(newModel(fake, "/work"), 120, 30)
	m = drive(t, m, m.loadList())

	m, command := press(t, m, "a")
	m = drive(t, m, command)
	if len(fake.archives) != 0 || !strings.Contains(m.status, "Close the chat") {
		t.Fatalf("archived an open chat: %+v, status %q", fake.archives, m.status)
	}
	m, _ = press(t, m, "down")
	m, command = press(t, m, "a")
	m = drive(t, m, command)
	if len(fake.archives) != 1 || fake.archives[0] != (ArchiveRequest{Directory: "/work", Conversation: "done", Archived: true}) {
		t.Fatalf("archives = %+v", fake.archives)
	}

	m, command = press(t, m, "v")
	m = drive(t, m, command)
	if !m.list.archived || len(m.list.items) != 1 || m.list.items[0].Conversation != "old" {
		t.Fatalf("archived view %v items %+v", m.list.archived, m.list.items)
	}
	if !strings.Contains(m.View().Content, "Archived") {
		t.Fatal("the list does not show the view switch")
	}
	m, command = press(t, m, "a")
	drive(t, m, command)
	if last := fake.archives[len(fake.archives)-1]; last.Conversation != "old" || last.Archived {
		t.Fatalf("unarchive request = %+v", last)
	}
}

func TestLateListAnswerForTheOtherViewIsDropped(t *testing.T) {
	fake := &fakeOperator{list: ConversationList{Conversations: []ConversationSummary{{Conversation: "a", Title: "Active"}}}}
	m := sized(newModel(fake, "/work"), 120, 30)
	stale := m.loadList()
	m, _ = press(t, m, "v")
	m = drive(t, m, stale)
	if m.list.loaded || len(m.list.items) != 0 {
		t.Fatalf("archive shows the active list: %+v", m.list.items)
	}
}

func TestViewButtonsAndArchiveButtonAreClickable(t *testing.T) {
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
	archived := find(m, "view:archived")
	m, command := click(t, m, archived.x0, archived.y)
	m = drive(t, m, command)
	if !m.list.archived || fake.lists[len(fake.lists)-1] != (ListRequest{Directory: "/work", Archived: true}) {
		t.Fatalf("archived %v lists %+v", m.list.archived, fake.lists)
	}

	fake.view.Status.Open = false
	m, command = m.pollChat()
	m = drive(t, m, command)
	archive := find(m, "archive")
	m, command = click(t, m, archive.x0, archive.y)
	drive(t, m, command)
	if len(fake.archives) != 1 || fake.archives[0].Conversation != testConversation || !fake.archives[0].Archived {
		t.Fatalf("archives = %+v", fake.archives)
	}
}

// longChat returns a closed conversation with enough messages to scroll.
func longChat() ConversationView {
	var messages []TranscriptMessage
	for id := int64(1); id <= 40; id++ {
		messages = append(messages, TranscriptMessage{ID: id, Label: "Lead", Body: fmt.Sprintf("Message %d", id), Created: 1795000000})
	}
	return ConversationView{
		Conversation: testConversation,
		Brief:        "Old review",
		Archived:     true,
		Status:       ConversationStatus{Members: map[string]MemberStatus{"Lead": {Role: "lead", State: "done"}}},
		Messages:     messages,
		Cursor:       40,
	}
}

func TestSearchOpensTheChatAtTheMatchingMessage(t *testing.T) {
	target := int64(12)
	author := "Lead"
	fake := &fakeOperator{
		view: longChat(),
		results: SearchResults{Hits: []SearchHit{
			{Conversation: testConversation, Title: "Old review", Archived: true, MessageID: &target, Author: &author, Snippet: "Message 12"},
		}},
	}
	m := sized(newModel(fake, "/work"), 120, 30)

	m, command := press(t, m, "/")
	m = drive(t, m, command)
	if m.screen != screenSearch {
		t.Fatalf("screen %v, want search", m.screen)
	}
	m = typeText(t, m, "message 12")
	m, command = press(t, m, "enter")
	m = drive(t, m, command)
	if len(fake.searches) != 1 || fake.searches[0] != (SearchRequest{Directory: "/work", Query: "message 12"}) {
		t.Fatalf("searches = %+v", fake.searches)
	}
	if !strings.Contains(m.View().Content, "ARCHIVED") {
		t.Fatal("the result does not show that its chat is archived")
	}

	m, command = press(t, m, "enter")
	m = drive(t, m, command)
	if m.screen != screenChat || m.chat.id != testConversation || m.chat.highlight != target {
		t.Fatalf("screen %v chat %q highlight %d", m.screen, m.chat.id, m.chat.highlight)
	}
	if !m.list.archived {
		t.Fatal("the chat list did not switch to the archive for an archived result")
	}
	visible := visibleTranscriptText(m)
	if !strings.Contains(visible, "Message 12") || strings.Contains(visible, "Message 40") {
		t.Fatalf("transcript is not scrolled to message 12:\n%s", visible)
	}
}

// visibleTranscriptText returns the transcript rows on screen without styles.
func visibleTranscriptText(m model) string {
	_, center, _ := m.columnWidths()
	return ansi.Strip(strings.Join(m.visibleTranscript(center, m.transcriptHeight(center)), "\n"))
}

func TestSearchResultsAreClickableAndEscGoesBack(t *testing.T) {
	fake := &fakeOperator{results: SearchResults{Hits: []SearchHit{
		{Conversation: "other", Title: "Other", Snippet: "first"},
		{Conversation: testConversation, Title: "Old review", Snippet: "in the brief"},
	}}}
	m := sized(chatModel(t, fake), 140, 30)
	fake.view = longChat()
	m, _ = press(t, m, "tab")
	m, _ = press(t, m, "tab") // chat list focus
	m, command := press(t, m, "/")
	m = drive(t, m, command)
	m, _ = press(t, m, "esc")
	if m.screen != screenChat {
		t.Fatalf("esc went to screen %v, want the chat", m.screen)
	}

	search := zone{}
	for _, item := range m.zones() {
		if item.id == "search" {
			search = item
		}
	}
	m, command = click(t, m, search.x0, search.y)
	m = drive(t, m, command)
	m = typeText(t, m, "brief")
	m, command = press(t, m, "enter")
	m = drive(t, m, command)

	// The second result's title row: body rows start at 1, four header rows, four rows a result.
	m, command = click(t, m, sidebarWidth+5, 1+searchHeaderRows+searchHitRows)
	m = drive(t, m, command)
	if m.screen != screenChat || m.chat.id != testConversation || m.chat.highlight != 0 {
		t.Fatalf("screen %v chat %q highlight %d", m.screen, m.chat.id, m.chat.highlight)
	}
	if visible := visibleTranscriptText(m); !strings.Contains(visible, "Message 1\n") || strings.Contains(visible, "Message 40") {
		t.Fatalf("a brief match does not open at the start:\n%s", visible)
	}
}

func TestHighlightTermsMarksWordsThatStartWithATerm(t *testing.T) {
	got := highlightTerms("Retry the retrying job; skip jobs.", queryTerms("RETRY job"))
	if ansi.Strip(got) != "Retry the retrying job; skip jobs." {
		t.Fatalf("text changed: %q", ansi.Strip(got))
	}
	for _, word := range []string{"Retry", "retrying", "job", "jobs"} {
		if !strings.Contains(got, accentStyle.Render(word)) {
			t.Errorf("%q is not highlighted in %q", word, got)
		}
	}
	if strings.Contains(got, accentStyle.Render("skip")) {
		t.Error("skip is highlighted")
	}
}

func TestMemberStateShowsOpenTasksAsWorking(t *testing.T) {
	idle := int64(12)
	glyph, detail := memberState(MemberStatus{
		Role:        "worker",
		State:       "done",
		IdleSeconds: &idle,
		OpenTasks:   []OpenTask{{ID: 42}},
	})
	if ansi.Strip(glyph) != "●" || detail != "working on #42 · active 12s ago" {
		t.Fatalf("got (%q, %q), want active working on #42", ansi.Strip(glyph), detail)
	}

	glyph, detail = memberState(MemberStatus{
		Role:      "worker",
		State:     "replied",
		OpenTasks: []OpenTask{{ID: 42}, {ID: 45}},
	})
	if ansi.Strip(glyph) != "●" || detail != "working on 2 tasks" {
		t.Fatalf("got (%q, %q), want working on 2 tasks", ansi.Strip(glyph), detail)
	}

	glyph, detail = memberState(MemberStatus{
		Role:  "worker",
		State: "done",
	})
	if ansi.Strip(glyph) != "○" || detail != "done" {
		t.Fatalf("got (%q, %q), want done", ansi.Strip(glyph), detail)
	}

	glyph, detail = memberState(MemberStatus{
		Role:  "worker",
		State: "retired",
	})
	if ansi.Strip(glyph) != "✕" || detail != "archived" {
		t.Fatalf("got (%q, %q), want archived", ansi.Strip(glyph), detail)
	}
}

func TestRetireFromTeamPanelConfirmsAndCancels(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)
	m.invitations[testConversation] = map[string]JoinInvitation{
		foldName("Reviewer"): {Participant: "Reviewer", Code: "W", JoinPrompt: "join W"},
	}

	// Tab to team panel, move down to Reviewer, press d to start retiring, esc to cancel.
	m, _ = press(t, m, "tab")
	m, _ = press(t, m, "down")
	m, _ = press(t, m, "d")
	if m.chat.retiring != "Reviewer" {
		t.Fatalf("retiring = %q, want Reviewer", m.chat.retiring)
	}
	m, _ = press(t, m, "esc")
	if m.chat.retiring != "" || len(fake.retires) != 0 {
		t.Fatalf("esc did not cancel retire: retiring=%q retires=%+v", m.chat.retiring, fake.retires)
	}

	// Confirm retirement with enter.
	m, _ = press(t, m, "d")
	var command tea.Cmd
	m, command = press(t, m, "enter")
	m = drive(t, m, command)
	if len(fake.retires) != 1 || fake.retires[0].Name != "Reviewer" {
		t.Fatalf("retires = %+v, want Reviewer", fake.retires)
	}
	if _, found := m.knownInvitation("Reviewer"); found {
		t.Fatal("cached invitation was not cleared after retirement")
	}
	if !strings.Contains(m.status, "Retired Reviewer.") {
		t.Fatalf("unexpected status: %q", m.status)
	}
}

func TestRetireSlashCommandSendsRequest(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)
	m = typeText(t, m, "/retire Reviewer")
	var command tea.Cmd
	m, command = press(t, m, "enter")
	m = drive(t, m, command)
	if len(fake.retires) != 1 || fake.retires[0].Name != "Reviewer" {
		t.Fatalf("retires = %+v, want Reviewer", fake.retires)
	}
}

func TestShortChatScrollUpDoesNotHideArrivingMessages(t *testing.T) {
	fake := &fakeOperator{}
	m := sized(chatModel(t, fake), 140, 30)

	// Scrolling up when everything already fits must keep scroll at 0 (following the tail).
	next, _ := m.Update(tea.MouseWheelMsg{X: 60, Y: 10, Button: tea.MouseWheelUp})
	m = next.(model)
	m, _ = press(t, m, "up")
	if m.chat.scroll != 0 {
		t.Fatalf("scroll = %d on a short chat, want 0", m.chat.scroll)
	}

	// Subsequent arriving messages must remain visible at the bottom.
	var incoming []TranscriptMessage
	for id := int64(1); id <= 25; id++ {
		incoming = append(incoming, TranscriptMessage{
			ID: id, Label: "Lead", Body: fmt.Sprintf("Fresh %d", id), Created: 1795000000,
		})
	}
	fake.view.Messages = incoming
	fake.view.Cursor = 25
	var cmd tea.Cmd
	m, cmd = m.pollChat()
	m = drive(t, m, cmd)

	if m.chat.scroll != 0 {
		t.Fatalf("scroll drifted to %d after new messages arrived", m.chat.scroll)
	}
	visible := visibleTranscriptText(m)
	if !strings.Contains(visible, "Fresh 25") {
		t.Fatalf("newest message disappeared from view:\n%s", visible)
	}
}

func TestOpenChatLoadsTailAndAutoPaginatesOlderOnScroll(t *testing.T) {
	const total = 80
	const pageSize = 40
	totalCount := total
	all := make([]TranscriptMessage, 0, total)
	for id := int64(1); id <= total; id++ {
		all = append(all, TranscriptMessage{
			ID: id, Label: "Lead", Body: fmt.Sprintf("Msg %d", id), Created: 1795000000,
		})
	}
	baseView := func(messages []TranscriptMessage, more bool, cursor int64) ConversationView {
		return ConversationView{
			Conversation:  testConversation,
			Brief:         "Long chat",
			Status:        ConversationStatus{Open: true, Members: map[string]MemberStatus{"Lead": {Role: "lead", State: "joined"}}},
			Messages:      messages,
			More:          more,
			Cursor:        cursor,
			TotalMessages: &totalCount,
		}
	}
	fake := &fakeOperator{
		showFunc: func(req ShowRequest) (ConversationView, error) {
			if req.Tail {
				return baseView(all[total-pageSize:], true, total), nil
			}
			if req.Before != nil {
				end := min(int(*req.Before-1), len(all))
				start := max(end-pageSize, 0)
				return baseView(all[start:end], start > 0, all[end-1].ID), nil
			}
			return baseView(nil, false, req.After), nil
		},
	}

	m := sized(newModel(fake, "/work"), 140, 30)
	next, cmd := m.openChat(testConversation)
	m = drive(t, next.(model), cmd)

	if len(fake.shows) != 1 || !fake.shows[0].Tail {
		t.Fatalf("initial shows = %+v, want one tail request", fake.shows)
	}
	if len(m.chat.messages) != pageSize || m.chat.messages[0].ID != 41 || !m.chat.hasOlder {
		t.Fatalf("loaded %d messages starting at %d hasOlder=%v", len(m.chat.messages), m.chat.messages[0].ID, m.chat.hasOlder)
	}
	if !strings.Contains(m.View().Content, "80 messages") {
		t.Fatal("header does not show total_messages count")
	}

	// Scroll to the top of the loaded page to trigger auto-pagination of older messages.
	for range 20 {
		var scrollCmd tea.Cmd
		m, scrollCmd = m.scrollTranscript("pgup")
		m = drive(t, m, scrollCmd)
	}
	if len(fake.shows) < 2 || fake.shows[1].Before == nil || *fake.shows[1].Before != 41 {
		t.Fatalf("shows after scroll up = %+v, want before=41", fake.shows)
	}
	if len(m.chat.messages) != total || m.chat.messages[0].ID != 1 || m.chat.hasOlder {
		t.Fatalf("after auto-pagination: len=%d first=%d hasOlder=%v", len(m.chat.messages), m.chat.messages[0].ID, m.chat.hasOlder)
	}
}

func TestSearchStartAutoPaginatesNewerOnScrollDown(t *testing.T) {
	const total = 60
	const pageSize = 30
	totalCount := total
	all := make([]TranscriptMessage, 0, total)
	for id := int64(1); id <= total; id++ {
		all = append(all, TranscriptMessage{
			ID: id, Label: "Lead", Body: fmt.Sprintf("Entry %d", id), Created: 1795000000,
		})
	}
	fake := &fakeOperator{
		showFunc: func(req ShowRequest) (ConversationView, error) {
			start := int(req.After)
			end := min(start+pageSize, len(all))
			return ConversationView{
				Conversation:  testConversation,
				Brief:         "Long chat",
				Status:        ConversationStatus{Open: true, Members: map[string]MemberStatus{"Lead": {Role: "lead", State: "joined"}}},
				Messages:      all[start:end],
				More:          end < len(all),
				Cursor:        int64(end),
				TotalMessages: &totalCount,
			}, nil
		},
	}

	m := sized(newModel(fake, "/work"), 140, 30)
	next, cmd := m.openChatAt(testConversation, 0)
	m = drive(t, next.(model), cmd)

	// Only the first page should be loaded, not eagerly drained.
	if len(fake.shows) != 1 || len(m.chat.messages) != pageSize || !m.chat.hasNewer {
		t.Fatalf("shows=%d len=%d hasNewer=%v, want 1 page with hasNewer=true", len(fake.shows), len(m.chat.messages), m.chat.hasNewer)
	}

	// Scroll down near the bottom to auto-load the next page.
	for range 15 {
		var scrollCmd tea.Cmd
		m, scrollCmd = m.scrollTranscript("pgdown")
		m = drive(t, m, scrollCmd)
	}
	if len(fake.shows) != 2 || fake.shows[1].After != pageSize {
		t.Fatalf("shows after scroll down = %+v, want second call with after=%d", fake.shows, pageSize)
	}
	if len(m.chat.messages) != total || m.chat.hasNewer {
		t.Fatalf("len=%d hasNewer=%v, want %d and false", len(m.chat.messages), m.chat.hasNewer, total)
	}
}

func TestTeamPanelShowsArchivedSectionAndPreservesOldParticipants(t *testing.T) {
	fake := &fakeOperator{
		view: ConversationView{
			Conversation: testConversation,
			Brief:        "Team lifecycle",
			Status: ConversationStatus{
				Open: true,
				Members: map[string]MemberStatus{
					"Lead":      {Role: "lead", State: "replied"},
					"Reviewer":  {Role: "worker", State: "replied"},
					"OldWorker": {Role: "worker", State: "retired"},
					"Human":     {Role: "human", State: "human"},
				},
			},
		},
	}
	m := sized(newModel(fake, "/work"), 140, 30)
	next, cmd := m.openChat(testConversation)
	m = drive(t, next.(model), cmd)

	panel := ansi.Strip(strings.Join(m.teamView(teamWidth, 28), "\n"))
	teamIndex := strings.Index(panel, "TEAM")
	archivedIndex := strings.Index(panel, "ARCHIVED")
	oldIndex := strings.Index(panel, "OldWorker")
	if teamIndex < 0 || archivedIndex < 0 || oldIndex < 0 {
		t.Fatalf("missing TEAM/ARCHIVED/OldWorker in panel:\n%s", panel)
	}
	if !(teamIndex < archivedIndex && archivedIndex < oldIndex) {
		t.Fatalf("OldWorker should appear under ARCHIVED section:\n%s", panel)
	}
	if !strings.Contains(panel, "archived") {
		t.Fatalf("archived detail missing:\n%s", panel)
	}

	var archivedRow teamRow
	for _, row := range m.teamRows() {
		if row.name == "OldWorker" {
			archivedRow = row
		}
	}
	if archivedRow.name == "" {
		t.Fatal("OldWorker missing from teamRows")
	}
	listW, centerW, _ := m.columnWidths()
	teamX := listW + 1 + centerW + 2
	m, _ = click(t, m, teamX, 1+archivedRow.top)
	if m.selectedMember() != "OldWorker" {
		t.Fatalf("selectedMember = %q, want OldWorker", m.selectedMember())
	}

	m, _ = press(t, m, "m")
	if len(fake.roleChanges) != 0 || !strings.Contains(m.status, "archived") {
		t.Fatalf("unexpected make-lead on archived: changes=%+v status=%q", fake.roleChanges, m.status)
	}
	m, _ = press(t, m, "d")
	if m.chat.retiring != "" || !strings.Contains(m.status, "already archived") {
		t.Fatalf("unexpected retire on archived: retiring=%q status=%q", m.chat.retiring, m.status)
	}
}

func TestMakeLeadAndRoleSlashCommandsUpdateRunningTUI(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)
	showsBefore := len(fake.shows)
	listsBefore := len(fake.lists)

	m, _ = press(t, m, "tab")
	m, _ = press(t, m, "down")
	var cmd tea.Cmd
	m, cmd = press(t, m, "m")
	m = drive(t, m, cmd)

	if len(fake.roleChanges) != 1 || fake.roleChanges[0] != (SetRoleRequest{
		Directory: "/work", Conversation: testConversation, Name: "Reviewer", Role: "lead",
	}) {
		t.Fatalf("roleChanges = %+v, want Reviewer -> lead", fake.roleChanges)
	}
	if len(fake.shows) <= showsBefore || len(fake.lists) <= listsBefore {
		t.Fatalf("expected immediate show and list refresh after role change: shows=%d lists=%d", len(fake.shows), len(fake.lists))
	}

	m, _ = press(t, m, "esc")
	m = typeText(t, m, "/role Reviewer co-lead")
	m, cmd = press(t, m, "enter")
	m = drive(t, m, cmd)
	if len(fake.roleChanges) != 2 || fake.roleChanges[1].Role != "co-lead" {
		t.Fatalf("roleChanges = %+v, want co-lead", fake.roleChanges)
	}

	m = typeText(t, m, "/lead Lead")
	m, cmd = press(t, m, "enter")
	m = drive(t, m, cmd)
	if len(fake.roleChanges) != 3 || fake.roleChanges[2].Name != "Lead" || fake.roleChanges[2].Role != "lead" {
		t.Fatalf("roleChanges = %+v, want Lead -> lead", fake.roleChanges)
	}

	m = typeText(t, m, "/role Lead boss")
	m, _ = press(t, m, "enter")
	if len(fake.roleChanges) != 3 || !strings.Contains(m.status, "Usage: /role") {
		t.Fatalf("invalid role was not rejected: changes=%+v status=%q", fake.roleChanges, m.status)
	}
}

func TestLivePollReflectsAddedArchivedAndLeadChangedParticipants(t *testing.T) {
	initial := ConversationView{
		Conversation: testConversation,
		Brief:        "Live roster updates",
		Status: ConversationStatus{
			Open: true,
			Members: map[string]MemberStatus{
				"Lead":     {Role: "lead", State: "replied"},
				"Reviewer": {Role: "worker", State: "replied"},
				"Human":    {Role: "human", State: "human"},
			},
		},
		Messages: []TranscriptMessage{{ID: 1, Label: "Lead", Body: "Kickoff", Created: 1795000000}},
		Cursor:   1,
	}
	updated := ConversationView{
		Conversation: testConversation,
		Brief:        "Live roster updates",
		Status: ConversationStatus{
			Open: true,
			Members: map[string]MemberStatus{
				"Lead":     {Role: "worker", State: "replied"},
				"Reviewer": {Role: "worker", State: "retired"},
				"Scout":    {Role: "lead", State: "joined"},
				"Human":    {Role: "human", State: "human"},
			},
		},
		Cursor: 1,
	}
	call := 0
	fake := &fakeOperator{
		showFunc: func(ShowRequest) (ConversationView, error) {
			call++
			if call == 1 {
				return initial, nil
			}
			return updated, nil
		},
	}

	m := sized(newModel(fake, "/work"), 140, 30)
	next, cmd := m.openChat(testConversation)
	m = drive(t, next.(model), cmd)

	m, _ = press(t, m, "tab")
	if m.selectedMember() != "Lead" {
		t.Fatalf("initial selectedMember = %q, want Lead", m.selectedMember())
	}
	versionBefore := m.chat.version
	listsBefore := len(fake.lists)

	next, cmd = m.Update(tickMsg(time.Now()))
	m = drive(t, next.(model), cmd)

	if m.chat.version == versionBefore {
		t.Fatal("transcript version did not bump when roles changed")
	}
	if len(fake.lists) <= listsBefore {
		t.Fatal("sidebar list did not refresh immediately on roster change")
	}
	if m.selectedMember() != "Lead" {
		t.Fatalf("selection drifted after reorder: got %q, want Lead", m.selectedMember())
	}
	panel := ansi.Strip(strings.Join(m.teamView(teamWidth, 28), "\n"))
	scoutPos := strings.Index(panel, "Scout lead")
	leadPos := strings.Index(panel, "Lead worker")
	archivedPos := strings.Index(panel, "ARCHIVED")
	reviewerPos := strings.Index(panel, "Reviewer worker")
	if scoutPos < 0 || leadPos < 0 || archivedPos < 0 || reviewerPos < 0 {
		t.Fatalf("panel missing expected live roster updates:\n%s", panel)
	}
	if !(scoutPos < leadPos && leadPos < archivedPos && archivedPos < reviewerPos) {
		t.Fatalf("unexpected team order after live update:\n%s", panel)
	}
}

func TestInviteImmediatelyRefreshesTeamAndShowsNewSlot(t *testing.T) {
	fake := &fakeOperator{}
	m := chatModel(t, fake)
	showsBefore := len(fake.shows)
	listsBefore := len(fake.lists)

	m = typeText(t, m, "/invite Scout")
	var cmd tea.Cmd
	m, cmd = press(t, m, "enter")
	m = drive(t, m, cmd)

	if !m.inviteWindow.open {
		t.Fatal("invite window did not open after /invite")
	}
	if len(fake.shows) <= showsBefore || len(fake.lists) <= listsBefore {
		t.Fatalf("expected immediate show and list refresh after invite: shows=%d lists=%d", len(fake.shows), len(fake.lists))
	}
}
