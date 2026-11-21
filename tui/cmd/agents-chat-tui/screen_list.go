package main

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// listState holds the active or archived conversations of the checkout, newest first.
type listState struct {
	items    []ConversationSummary
	selected int
	loaded   bool
	archived bool
}

// homeKey handles a key while no conversation is open.
func (m model) homeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "n":
		return m.openForm()
	case "/", "a", "v":
		return m.listAction(msg.String())
	case "r":
		return m.inform("Refreshing."), m.loadList()
	case "q":
		return m, tea.Quit
	}
	return m.listNavigate(msg.String())
}

// listNavigate moves the chat selection or opens the selected chat.
func (m model) listNavigate(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.list.selected = max(m.list.selected-1, 0)
	case "down", "j":
		m.list.selected = min(m.list.selected+1, max(len(m.list.items)-1, 0))
	case "enter":
		if len(m.list.items) == 0 {
			return m, nil
		}
		return m.openChat(m.list.items[m.list.selected].Conversation)
	}
	return m, nil
}

// listAction runs the search, archive and view keys that work wherever the chat list has
// focus.
func (m model) listAction(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "/":
		return m.openSearch()
	case "a":
		return m.archiveSelected()
	case "v":
		return m.showArchive(!m.list.archived)
	}
	return m, nil
}

// showArchive switches the chat list between active and archived chats.
func (m model) showArchive(archived bool) (tea.Model, tea.Cmd) {
	if m.list.archived == archived {
		return m, nil
	}
	m.list = listState{archived: archived}
	return m, m.loadList()
}

// archiveSelected archives the selected closed chat, or returns it to the active list when
// the archive shows.
func (m model) archiveSelected() (tea.Model, tea.Cmd) {
	if len(m.list.items) == 0 {
		return m, nil
	}
	item := m.list.items[m.list.selected]
	if item.Open {
		return m.inform("Close the chat before archiving it."), nil
	}
	return m, m.archive(item.Conversation, !m.list.archived)
}

// archive moves a conversation to the archive or back to the active list.
func (m model) archive(conversation string, archived bool) tea.Cmd {
	request := ArchiveRequest{Directory: m.directory, Conversation: conversation, Archived: archived}
	client := m.client
	return func() tea.Msg {
		result, err := client.Archive(request)
		return archivedMsg{conversation: conversation, archived: result.Archived, err: err}
	}
}

// onArchived reports the move, reloads the list and refreshes the open chat's header.
func (m model) onArchived(msg archivedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.fail(msg.err), nil
	}
	notice := "Chat moved back to active chats."
	if msg.archived {
		notice = "Chat archived. Press v to see the archive."
	}
	m = m.inform(notice)
	commands := []tea.Cmd{m.loadList()}
	if msg.conversation == m.chat.id {
		var poll tea.Cmd
		m, poll = m.pollChat()
		commands = append(commands, poll)
	}
	return m, tea.Batch(commands...)
}

// onList replaces the list and keeps the selection in range. An answer for the other view
// arrives after a switch and is dropped. The first answer after a switch selects the open
// chat when it is in the list.
func (m model) onList(msg listMsg) model {
	if msg.err != nil {
		return m.fail(msg.err)
	}
	if msg.archived != m.list.archived {
		return m
	}
	firstLoad := !m.list.loaded
	m.list.items = msg.list.Conversations
	m.list.loaded = true
	m.list.selected = min(m.list.selected, max(len(m.list.items)-1, 0))
	for index, item := range m.list.items {
		if firstLoad && item.Conversation == m.chat.id {
			m.list.selected = index
		}
	}
	if m.status == "Refreshing." {
		m.status = ""
	}
	return m.succeed()
}

// listFocused reports whether keys go to the chat list.
func (m model) listFocused() bool {
	return m.screen == screenHome || m.chat.focus == focusList
}

// sidebarView draws the chat list, two lines per chat, and scrolls to keep the selection
// visible.
func (m model) sidebarView(width int, height int) []string {
	views, _ := m.viewButtons()
	lines := []string{" " + views, ""}
	switch {
	case !m.list.loaded:
		lines = append(lines, " "+dimStyle.Render("Loading."))
	case len(m.list.items) == 0 && m.list.archived:
		lines = append(lines, " "+dimStyle.Render("No archived chats."))
	case len(m.list.items) == 0:
		lines = append(lines, " "+dimStyle.Render("No chats yet."))
	}
	rows := max((height-len(lines)-2)/3, 1)
	first := max(m.list.selected-rows+1, 0)
	now := m.now()
	for index := first; index < len(m.list.items) && index < first+rows; index++ {
		item := m.list.items[index]
		title := ansi.Truncate(cleanLine(item.Title), width-2, "…")
		detail := dimStyle.Render("closed · " + age(item.Created, now))
		if item.Open {
			glyph := okStyle.Render("●")
			if item.Joined < item.Members {
				glyph = dimStyle.Render("◌")
			}
			detail = glyph + dimStyle.Render(fmt.Sprintf(" %d/%d joined · %s", item.Joined, item.Members, age(item.Created, now)))
		}
		openHere := item.Conversation == m.chat.id && m.screen == screenChat
		switch {
		case index == m.list.selected && m.listFocused():
			lines = append(lines,
				fill(accentStyle.Render("▎")+brightStyle.Render(title), width, colorSelect),
				fill(accentStyle.Render("▎")+detail, width, colorSelect))
		case m.hover == fmt.Sprintf("chat:%d", index):
			lines = append(lines,
				fill(dimStyle.Render("▎")+brightStyle.Render(title), width, colorBand),
				fill(dimStyle.Render("▎")+detail, width, colorBand))
		case openHere:
			lines = append(lines, dimStyle.Render("▎")+brightStyle.Render(title), dimStyle.Render("▎")+detail)
		case item.Open:
			lines = append(lines, " "+textStyle.Render(title), " "+detail)
		default:
			lines = append(lines, " "+dimStyle.Render(title), " "+detail)
		}
		lines = append(lines, "")
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	actions, _ := m.listButtons()
	return append(lines[:max(height-1, 0)], " "+actions)
}

// viewButtons draws the switch between active and archived chats above the list.
func (m model) viewButtons() (string, []span) {
	return m.buttons(
		button{id: "view:active", label: "Active", primary: !m.list.archived},
		button{id: "view:archived", label: "Archived", primary: m.list.archived},
	)
}

// listButtons draws the buttons under the list.
func (m model) listButtons() (string, []span) {
	return m.buttons(button{id: "new", label: "+ New chat"}, button{id: "search", label: "Search"})
}

// homeView fills the center while no conversation is open.
func (m model) homeView(width int, height int) []string {
	lines := make([]string, max(height/3, 1))
	if m.list.loaded && len(m.list.items) == 0 && m.list.archived {
		return append(lines,
			"  "+brightStyle.Render("No archived chats."),
			"",
			"  "+dimStyle.Render("Select a closed chat and press ")+accentStyle.Render("a")+dimStyle.Render(" to archive it. ")+
				accentStyle.Render("v")+dimStyle.Render(" shows active chats."))
	}
	if m.list.loaded && len(m.list.items) == 0 {
		return append(lines,
			"  "+brightStyle.Render("No chats in this workspace yet."),
			"",
			"  "+dimStyle.Render("Press ")+accentStyle.Render("n")+dimStyle.Render(" to write a brief, name the team and start one."))
	}
	return append(lines,
		"  "+brightStyle.Render("No chat open."),
		"",
		"  "+accentStyle.Render("↑ ↓")+dimStyle.Render(" choose a chat   ")+accentStyle.Render("⏎")+dimStyle.Render(" open   ")+
			accentStyle.Render("n")+dimStyle.Render(" new chat   ")+accentStyle.Render("/")+dimStyle.Render(" search"))
}
