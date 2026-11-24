package main

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// maxTitleLength is the core's limit for a custom chat title.
const maxTitleLength = 80

// renameState is the rename dialog over the screen, if open.
type renameState struct {
	open         bool
	conversation string
	input        textinput.Model
}

// renamedMsg reports the result of a rename request with the title the core now shows.
type renamedMsg struct {
	conversation string
	title        string
	err          error
}

// startRename opens the rename dialog for a conversation with its current title filled in.
func (m model) startRename(conversation string, title string) (tea.Model, tea.Cmd) {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = "First line of the brief"
	input.CharLimit = maxTitleLength
	input.SetValue(cleanLine(title))
	input.CursorEnd()
	m.rename = renameState{open: true, conversation: conversation, input: input}
	m.resizeInputs()
	if m.chat.id != "" {
		m.chat.input.Blur()
	}
	return m, m.rename.input.Focus()
}

// renameSelected opens the rename dialog for the chat selected in the list.
func (m model) renameSelected() (tea.Model, tea.Cmd) {
	if len(m.list.items) == 0 {
		return m, nil
	}
	item := m.list.items[m.list.selected]
	return m.startRename(item.Conversation, item.Title)
}

// closeRename dismisses the rename dialog and returns keys to the screen under it.
func (m model) closeRename() (tea.Model, tea.Cmd) {
	m.rename = renameState{}
	if m.screen == screenChat {
		return m.setChatFocus(m.chat.focus)
	}
	return m, nil
}

// renameKey handles a key while the rename dialog shows. An empty title asks the core to
// use the first line of the brief again.
func (m model) renameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m.closeRename()
	case "enter":
		request := RenameRequest{Directory: m.directory, Conversation: m.rename.conversation}
		if title := strings.TrimSpace(m.rename.input.Value()); title != "" {
			request.Title = &title
		}
		client := m.client
		next, focus := m.closeRename()
		return next.(model).inform("Renaming."), tea.Batch(focus, func() tea.Msg {
			result, err := client.Rename(request)
			return renamedMsg{conversation: request.Conversation, title: result.Title, err: err}
		})
	}
	var command tea.Cmd
	m.rename.input, command = m.rename.input.Update(msg)
	return m, command
}

// onRenamed shows the new title in the list and the open chat, then reloads the list.
func (m model) onRenamed(msg renamedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.fail(msg.err), nil
	}
	for index := range m.list.items {
		if m.list.items[index].Conversation == msg.conversation {
			m.list.items[index].Title = msg.title
		}
	}
	if msg.conversation == m.chat.id {
		m.chat.view.Title = msg.title
	}
	return m.showSuccess("Chat renamed."), m.loadList()
}

// drawRename places the rename dialog in the middle of the body.
func (m model) drawRename(body []string) []string {
	width := popupWidth(m.width, popupMaxWidth)
	description := "The brief stays as it is. Leave the title empty to use the first line of the brief."
	var lines []string
	for _, line := range wrapLines(description, width-4, "") {
		lines = append(lines, textStyle.Render(line))
	}
	lines = append(lines, "", dimStyle.Render("Title ")+m.rename.input.View())
	lines = append(lines, "", keyHints("⏎", "rename", "esc", "cancel"))
	box := framedBox(" Rename this chat ", accentStyle, lines, width)
	row := max((len(body)-len(box))/2, 0)
	return overlay(body, box, row, max((m.width-width)/2, 0), m.width)
}
