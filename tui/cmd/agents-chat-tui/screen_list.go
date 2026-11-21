package main

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// listState holds the conversations of the checkout, newest first.
type listState struct {
	items    []ConversationSummary
	selected int
	loaded   bool
}

// homeKey handles a key while no conversation is open.
func (m model) homeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "n":
		return m.openForm()
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

// onList replaces the list and keeps the selection in range.
func (m model) onList(msg listMsg) model {
	if msg.err != nil {
		return m.fail(msg.err)
	}
	m.list.items = msg.list.Conversations
	m.list.loaded = true
	m.list.selected = min(m.list.selected, max(len(m.list.items)-1, 0))
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
	lines := []string{" " + headStyle.Render("CHATS"), ""}
	switch {
	case !m.list.loaded:
		lines = append(lines, " "+dimStyle.Render("Loading."))
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
	newChat, _ := m.buttons(button{id: "new", label: "+ New chat"})
	return append(lines[:max(height-1, 0)], " "+newChat)
}

// homeView fills the center while no conversation is open.
func (m model) homeView(width int, height int) []string {
	lines := make([]string, max(height/3, 1))
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
			accentStyle.Render("n")+dimStyle.Render(" new chat"))
}
