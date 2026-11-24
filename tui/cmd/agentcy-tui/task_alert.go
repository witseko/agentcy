package main

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// taskAlertState is the alert that stops a close or an archive while tasks are unfinished.
type taskAlertState struct {
	open bool
	// archive says the alert stops an archive. Otherwise it stops a close.
	archive      bool
	conversation string
	count        int
}

// archiveCheckMsg carries the unfinished task count that an archive waits for.
type archiveCheckMsg struct {
	conversation string
	unfinished   int
	err          error
}

// taskFinished reports whether a task state needs no more work.
func taskFinished(state string) bool {
	return state == "done" || state == "deprecated"
}

// requestArchive archives a chat after a check for unfinished tasks. A move back to the
// active list needs no check.
func (m model) requestArchive(conversation string, archived bool) (tea.Model, tea.Cmd) {
	if !archived {
		return m, m.archive(conversation, false, false)
	}
	request := TasksRequest{Directory: m.directory, Conversation: conversation}
	client := m.client
	return m, func() tea.Msg {
		list, err := client.Tasks(request)
		unfinished := 0
		for _, task := range list.Tasks {
			if !taskFinished(task.State) {
				unfinished++
			}
		}
		return archiveCheckMsg{conversation: conversation, unfinished: unfinished, err: err}
	}
}

// onArchiveCheck archives the chat, or opens the alert when tasks are unfinished.
func (m model) onArchiveCheck(msg archiveCheckMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.fail(msg.err), nil
	}
	if msg.unfinished == 0 {
		return m, m.archive(msg.conversation, true, false)
	}
	m.taskAlert = taskAlertState{open: true, archive: true, conversation: msg.conversation, count: msg.unfinished}
	return m, nil
}

// taskAlertKey handles a key while the open tasks alert shows. Every other key is ignored,
// so the chat cannot close or move to the archive with unfinished tasks.
func (m model) taskAlertKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	alert := m.taskAlert
	switch msg.String() {
	case "esc":
		m.taskAlert = taskAlertState{}
	case "t":
		m.taskAlert = taskAlertState{}
		return m.checkOpenTasks(alert.conversation)
	case "d":
		m.taskAlert = taskAlertState{}
		if alert.archive {
			return m, m.archive(alert.conversation, true, true)
		}
		m.chat.deprecateOnClose = true
		return m.askCloseSummary()
	}
	return m, nil
}

// checkOpenTasks shows the unfinished tasks of a chat in its Tasks tab.
func (m model) checkOpenTasks(conversation string) (tea.Model, tea.Cmd) {
	var open tea.Cmd
	if m.screen != screenChat || m.chat.id != conversation {
		next, command := m.openChat(conversation)
		m, open = next.(model), command
	}
	m.chat.tasks.filter = filterOpen
	next, load := m.setChatTab(tabTasks)
	return next, tea.Batch(open, load)
}

// taskAlertHelp returns the key hints of the open tasks alert.
func (m model) taskAlertHelp() []hint {
	return []hint{{"t", "check tasks"}, {"d", "deprecate tasks"}, {"esc", "cancel"}}
}

// drawTaskAlert places the open tasks alert in the middle of the body.
func (m model) drawTaskAlert(body []string) []string {
	width := popupWidth(m.width, popupMaxWidth)
	action, next := "close", "close the chat"
	if m.taskAlert.archive {
		action, next = "archive", "archive the chat"
	}
	count := fmt.Sprintf("%d tasks are", m.taskAlert.count)
	if m.taskAlert.count == 1 {
		count = "1 task is"
	}
	description := fmt.Sprintf(
		"%s still open. Check the task statuses, or deprecate the open tasks and %s.", count, next)
	var lines []string
	for _, line := range wrapLines(description, width-4, "") {
		lines = append(lines, textStyle.Render(line))
	}
	lines = append(lines, "", keyHints("t", "check tasks", "d", "deprecate and "+action, "esc", "cancel"))
	box := framedBox(" Open tasks ", warnStyle, lines, width)
	row := max((len(body)-len(box))/2, 0)
	return overlay(body, box, row, max((m.width-width)/2, 0), m.width)
}
