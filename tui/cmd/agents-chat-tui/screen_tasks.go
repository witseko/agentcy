package main

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// chatTab selects which center tab is active inside an open conversation.
type chatTab int

const (
	tabChat chatTab = iota
	tabTasks
)

// taskFilter selects which tasks are shown in the Tasks tab list.
type taskFilter int

const (
	filterOpen taskFilter = iota
	filterAll
	filterDone
)

// tasksState holds the conversation's tasks and the Tasks tab selection and scroll state.
type tasksState struct {
	items        []TaskDetail
	loaded       bool
	loading      bool
	filter       taskFilter
	selectedID   int64
	detailScroll int
}

// tasksMsg carries the result of an operator_tasks request.
type tasksMsg struct {
	conversation string
	list         TaskList
	err          error
}

// openTaskCount returns the number of unfinished tasks reported in the current conversation status.
func (m model) openTaskCount() int {
	if m.chat.tab == tabTasks && m.chat.tasks.loaded {
		count := 0
		for _, task := range m.chat.tasks.items {
			if !taskFinished(task.State) {
				count++
			}
		}
		return count
	}
	count := 0
	for _, member := range m.chat.view.Status.Members {
		count += len(member.OpenTasks)
	}
	return count
}

// chatTabButtons draws the Chat / Tasks tab switcher in the conversation header.
func (m model) chatTabButtons() (string, []span) {
	tasksLabel := "Tasks"
	if open := m.openTaskCount(); open > 0 {
		tasksLabel = fmt.Sprintf("Tasks (%d)", open)
	}
	return m.buttons(
		button{id: "tab:chat", label: "Chat", primary: m.chat.tab == tabChat},
		button{id: "tab:tasks", label: tasksLabel, primary: m.chat.tab == tabTasks},
	)
}

// setChatTab switches between the Chat and Tasks tabs of the open conversation.
func (m model) setChatTab(tab chatTab) (tea.Model, tea.Cmd) {
	if m.chat.id == "" {
		return m, nil
	}
	m.chat.tab = tab
	if tab == tabChat {
		if m.chat.focus == focusInput {
			return m, m.chat.input.Focus()
		}
		return m, nil
	}
	m.chat.input.Blur()
	m.chat.focus = focusInput
	return m.loadTasks()
}

// openTaskInTab switches to the Tasks tab and selects a specific task ID.
func (m model) openTaskInTab(taskID int64) (tea.Model, tea.Cmd) {
	m.chat.tasks.selectedID = taskID
	m.chat.tasks.detailScroll = 0
	for _, task := range m.chat.tasks.items {
		if task.ID == taskID {
			if taskFinished(task.State) && m.chat.tasks.filter == filterOpen {
				m.chat.tasks.filter = filterAll
			} else if !taskFinished(task.State) && m.chat.tasks.filter == filterDone {
				m.chat.tasks.filter = filterAll
			}
			break
		}
	}
	return m.setChatTab(tabTasks)
}

// loadTasks fetches the conversation's task list unless a request is already in flight.
func (m model) loadTasks() (model, tea.Cmd) {
	if m.chat.id == "" || m.chat.tasks.loading {
		return m, nil
	}
	m.chat.tasks.loading = true
	request := TasksRequest{Directory: m.directory, Conversation: m.chat.id}
	client := m.client
	return m, func() tea.Msg {
		list, err := client.Tasks(request)
		return tasksMsg{conversation: request.Conversation, list: list, err: err}
	}
}

// onTasks stores the loaded task list and keeps the selection valid.
func (m model) onTasks(msg tasksMsg) model {
	if msg.conversation != m.chat.id {
		return m
	}
	m.chat.tasks.loading = false
	if msg.err != nil {
		return m.fail(msg.err)
	}
	m.chat.tasks.items = msg.list.Tasks
	m.chat.tasks.loaded = true
	m.syncTaskSelection()
	return m.succeed()
}

// filteredTasks returns the tasks matching the current filter.
func (m model) filteredTasks() []TaskDetail {
	var out []TaskDetail
	for _, task := range m.chat.tasks.items {
		switch m.chat.tasks.filter {
		case filterOpen:
			if !taskFinished(task.State) {
				out = append(out, task)
			}
		case filterDone:
			if taskFinished(task.State) {
				out = append(out, task)
			}
		default:
			out = append(out, task)
		}
	}
	return out
}

// taskCounts returns (open, all, done) task counts.
func (m model) taskCounts() (int, int, int) {
	open, done := 0, 0
	for _, task := range m.chat.tasks.items {
		if taskFinished(task.State) {
			done++
		} else {
			open++
		}
	}
	return open, len(m.chat.tasks.items), done
}

// syncTaskSelection ensures selectedID points to a task in the filtered list when non-empty.
func (m *model) syncTaskSelection() {
	visible := m.filteredTasks()
	if len(visible) == 0 {
		m.chat.tasks.selectedID = 0
		m.chat.tasks.detailScroll = 0
		return
	}
	for _, task := range visible {
		if task.ID == m.chat.tasks.selectedID {
			return
		}
	}
	m.chat.tasks.selectedID = visible[0].ID
	m.chat.tasks.detailScroll = 0
}

// selectedTask returns the currently selected task and its index in filteredTasks, or (-1, false).
func (m model) selectedTask() (TaskDetail, int, bool) {
	visible := m.filteredTasks()
	if len(visible) == 0 {
		return TaskDetail{}, -1, false
	}
	for index, task := range visible {
		if task.ID == m.chat.tasks.selectedID {
			return task, index, true
		}
	}
	return visible[0], 0, true
}

// moveTaskSelection moves the task selection by delta rows.
func (m *model) moveTaskSelection(delta int) {
	visible := m.filteredTasks()
	if len(visible) == 0 {
		return
	}
	_, idx, _ := m.selectedTask()
	next := min(max(idx+delta, 0), len(visible)-1)
	if visible[next].ID != m.chat.tasks.selectedID {
		m.chat.tasks.selectedID = visible[next].ID
		m.chat.tasks.detailScroll = 0
	}
}

// setTaskFilter switches the Tasks tab filter and updates selection.
func (m *model) setTaskFilter(filter taskFilter) {
	m.chat.tasks.filter = filter
	m.syncTaskSelection()
}

// tasksKey handles a key while the Tasks tab center pane has focus.
func (m model) tasksKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.moveTaskSelection(-1)
	case "down", "j":
		m.moveTaskSelection(1)
	case "pgup", "K", "[":
		m.scrollTaskDetail(-scrollPageSize)
	case "pgdown", "J", "]":
		m.scrollTaskDetail(scrollPageSize)
	case "f":
		m.setTaskFilter((m.chat.tasks.filter + 1) % 3)
	case "r":
		m = m.inform("Refreshing tasks.")
		return m.loadTasks()
	case "enter":
		return m.jumpToSelectedTask()
	case "esc":
		return m.setChatTab(tabChat)
	}
	return m, nil
}

// jumpToSelectedTask switches to the Chat tab and highlights the selected task's message.
func (m model) jumpToSelectedTask() (tea.Model, tea.Cmd) {
	task, _, ok := m.selectedTask()
	if !ok {
		return m, nil
	}
	return m.openChatAt(m.chat.id, task.ID)
}

// taskFilterButtons draws the Open / All / Done filter buttons on the Tasks tab toolbar.
func (m model) taskFilterButtons() (string, []span) {
	open, all, done := m.taskCounts()
	return m.buttons(
		button{id: "task-filter:open", label: fmt.Sprintf("Open (%d)", open), primary: m.chat.tasks.filter == filterOpen},
		button{id: "task-filter:all", label: fmt.Sprintf("All (%d)", all), primary: m.chat.tasks.filter == filterAll},
		button{id: "task-filter:done", label: fmt.Sprintf("Done (%d)", done), primary: m.chat.tasks.filter == filterDone},
	)
}

// taskJumpButton draws the Jump to chat button on the right side of the Tasks toolbar.
func (m model) taskJumpButton() (string, []span) {
	if _, _, ok := m.selectedTask(); !ok {
		return "", nil
	}
	return m.buttons(button{id: "task-jump", label: "Jump to chat ⏎"})
}

// tasksLayout returns the row count of the list pane and the detail pane inside tasksView.
func tasksLayout(contentHeight int) (listHeight int, detailHeight int) {
	// Rows: 0 = toolbar, 1 = blank, 2..2+listHeight-1 = list, next = divider, rest = detail.
	avail := max(contentHeight-3, 2)
	listHeight = max(avail*38/100, 3)
	if listHeight > avail-3 {
		listHeight = max(avail/2, 1)
	}
	detailHeight = max(avail-listHeight, 1)
	return listHeight, detailHeight
}

// scrollTaskDetail shifts the detail pane scroll offset within valid bounds.
func (m *model) scrollTaskDetail(delta int) {
	task, _, ok := m.selectedTask()
	if !ok {
		m.chat.tasks.detailScroll = 0
		return
	}
	_, center, _ := m.columnWidths()
	bodyHeight := max(m.height-2, 1)
	contentHeight := max(bodyHeight-m.chatHeaderHeight(center), 1)
	_, detailHeight := tasksLayout(contentHeight)
	total := len(m.taskDetailLines(task, center))
	maxScroll := max(total-detailHeight, 0)
	m.chat.tasks.detailScroll = min(max(m.chat.tasks.detailScroll+delta, 0), maxScroll)
}

// tasksView renders the Tasks tab content below chatHeader.
func (m model) tasksView(width int, height int) []string {
	filters, _ := m.taskFilterButtons()
	jump, _ := m.taskJumpButton()
	toolbar := " " + filters
	if jump != "" {
		gap := width - 1 - ansi.StringWidth(toolbar) - ansi.StringWidth(jump)
		if gap >= 2 {
			toolbar += strings.Repeat(" ", gap) + jump
		}
	}
	lines := []string{toolbar, ""}
	if !m.chat.tasks.loaded && m.chat.tasks.loading {
		return append(lines, " "+dimStyle.Render("Loading tasks…"))
	}
	if len(m.chat.tasks.items) == 0 {
		return append(lines,
			" "+brightStyle.Render("No tasks yet."),
			"",
			" "+dimStyle.Render("The lead assigns tasks with kind='task' and a teammate in 'to'."))
	}
	visible := m.filteredTasks()
	if len(visible) == 0 {
		empty := "No open tasks."
		if m.chat.tasks.filter == filterDone {
			empty = "No completed tasks yet."
		}
		return append(lines,
			" "+dimStyle.Render(empty+" Press f or click a filter above to see other tasks."))
	}
	listHeight, detailHeight := tasksLayout(height)
	selected, selectedIdx, _ := m.selectedTask()
	first := scrollFirst(len(visible), selectedIdx, listHeight)
	for offset := 0; offset < listHeight; offset++ {
		index := first + offset
		if index >= len(visible) {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, m.taskRowLine(visible[index], index == selectedIdx, width))
	}
	dividerTitle := fmt.Sprintf(" Task #%d · %s ", selected.ID, cleanLine(selected.Owner))
	fillWidth := max(width-ansi.StringWidth(dividerTitle)-3, 0)
	lines = append(lines,
		borderStyle.Render("───")+headStyle.Render(dividerTitle)+borderStyle.Render(strings.Repeat("─", fillWidth)))

	detail := m.taskDetailLines(selected, width)
	maxScroll := max(len(detail)-detailHeight, 0)
	start := min(max(m.chat.tasks.detailScroll, 0), maxScroll)
	end := min(start+detailHeight, len(detail))
	lines = append(lines, detail[start:end]...)
	return lines
}

// taskRowLine renders one compact row in the Tasks list pane.
func (m model) taskRowLine(task TaskDetail, selected bool, width int) string {
	idText := accentStyle.Render(fmt.Sprintf("#%-4d", task.ID))
	stateText := taskStatePill(task)
	ownerRole := m.chat.view.Status.Members[task.Owner].Role
	ownerName := ansi.Truncate(cleanLine(task.Owner), 16, "…")
	ownerText := roleStyle(ownerRole).Render(pad(ownerName, 16))
	prefix := idText + " " + stateText + " " + ownerText + " "
	titleRoom := max(width-2-ansi.StringWidth(prefix), 8)
	title := ansi.Truncate(cleanLine(task.Title), titleRoom, "…")
	row := prefix + textStyle.Render(title)
	switch {
	case selected && m.chat.focus == focusInput:
		return fill(accentStyle.Render("▎")+row, width, colorSelect)
	case selected:
		return fill(dimStyle.Render("▎")+row, width, colorBand)
	case m.hover == fmt.Sprintf("task:%d", task.ID):
		return fill(dimStyle.Render("▎")+row, width, colorBand)
	default:
		return " " + row
	}
}

// taskStatePill returns a fixed-width styled state label for the task list.
func taskStatePill(task TaskDetail) string {
	switch {
	case task.State == "deprecated":
		return dimStyle.Render("deprec.")
	case task.State == "done":
		return okStyle.Render("done   ")
	case task.State == "blocked":
		return warnStyle.Render("blocked")
	case task.Overdue:
		return warnStyle.Render("overdue")
	case task.PendingHandoffTo != "":
		return accentStyle.Render("handoff")
	default:
		return dimStyle.Render("open   ")
	}
}

// taskDetailLines builds the wrapped detail lines for one task: metadata, description, and activity.
func (m model) taskDetailLines(task TaskDetail, width int) []string {
	var stateBadge string
	switch task.State {
	case "done":
		stateBadge = badge("DONE", colorOK)
	case "deprecated":
		stateBadge = badge("DEPRECATED", colorDim)
	case "blocked":
		stateBadge = badge("BLOCKED", colorWarn)
	default:
		stateBadge = badge("OPEN", colorAccent)
	}
	header := fmt.Sprintf(" %s  %s", brightStyle.Render(fmt.Sprintf("Task #%d", task.ID)), stateBadge)
	if task.PendingHandoffTo != "" {
		header += "  " + tag("HANDOFF → "+cleanLine(task.PendingHandoffTo), colorAccent)
	}
	if task.Overdue {
		header += "  " + tag("OVERDUE", colorWarn)
	}
	ownerLabel := cleanLine(task.Owner)
	if member, ok := m.chat.view.Status.Members[task.Owner]; ok && member.State == "retired" {
		ownerLabel += " (archived)"
	}
	meta := fmt.Sprintf(
		"Owner: %s  ·  From: %s  ·  Created %s  ·  Updated %s",
		ownerLabel,
		cleanLine(task.Author),
		time.Unix(task.Created, 0).Format("15:04"),
		time.Unix(task.Updated, 0).Format("15:04"),
	)
	if task.Due != 0 {
		meta += "  ·  Due " + time.Unix(task.Due, 0).Format("15:04")
	}
	lines := []string{header}
	for _, line := range wrapLines(meta, max(width-2, 10), " ") {
		lines = append(lines, dimStyle.Render(line))
	}
	lines = append(lines, "", " "+headStyle.Render("DESCRIPTION"))
	for _, line := range wrapLines(task.Body, max(width-2, 10), " ") {
		lines = append(lines, textStyle.Render(line))
	}
	if len(task.Events) > 1 {
		lines = append(lines, "", " "+headStyle.Render(fmt.Sprintf("ACTIVITY (%d)", len(task.Events)-1)))
		for _, event := range task.Events[1:] {
			lines = append(lines, m.taskEventLines(event, width)...)
		}
	}
	return lines
}

// taskEventLines renders one follow-up event in a task's activity timeline.
func (m model) taskEventLines(event TaskEvent, width int) []string {
	role := m.chat.view.Status.Members[event.Author].Role
	author := cleanLine(event.Author)
	if role == "human" {
		author = "You"
	}
	header := " " + dimStyle.Render(time.Unix(event.Created, 0).Format("15:04")) + "  " +
		roleStyle(role).Render(author)
	if event.To != "" {
		header += dimStyle.Render(" → " + cleanLine(event.To))
	}
	if label := kindLabel(TranscriptMessage{ID: event.ID, Kind: event.Kind}); label != "" {
		header += "  " + label
	}
	lines := []string{header}
	for _, line := range wrapLines(event.Body, max(width-2, 10), messageIndent) {
		lines = append(lines, textStyle.Render(line))
	}
	return append(lines, "")
}

// taskRowAt maps a body row in the center column to a task ID when on the Tasks tab.
func (m model) taskRowAt(row int, bodyHeight int) (int64, bool) {
	_, center, _ := m.columnWidths()
	headerLen := m.chatHeaderHeight(center)
	contentRow := row - headerLen
	contentHeight := max(bodyHeight-headerLen, 1)
	listHeight, _ := tasksLayout(contentHeight)
	// Rows 0..1 inside tasksView are toolbar + blank; list rows start at 2.
	if contentRow < 2 || contentRow >= 2+listHeight {
		return 0, false
	}
	visible := m.filteredTasks()
	if len(visible) == 0 {
		return 0, false
	}
	_, selectedIdx, _ := m.selectedTask()
	first := scrollFirst(len(visible), selectedIdx, listHeight)
	index := first + (contentRow - 2)
	if index < 0 || index >= len(visible) {
		return 0, false
	}
	return visible[index].ID, true
}

// isTaskDetailRow reports whether a body row in the center column is over the task detail pane.
func (m model) isTaskDetailRow(row int, bodyHeight int) bool {
	_, center, _ := m.columnWidths()
	headerLen := m.chatHeaderHeight(center)
	contentRow := row - headerLen
	contentHeight := max(bodyHeight-headerLen, 1)
	listHeight, _ := tasksLayout(contentHeight)
	return contentRow >= 2+listHeight+1
}

// clickTasks selects a clicked task row in the Tasks tab.
func (m model) clickTasks(row int, bodyHeight int) (tea.Model, tea.Cmd) {
	if taskID, found := m.taskRowAt(row, bodyHeight); found {
		m.chat.focus = focusInput
		if m.chat.tasks.selectedID != taskID {
			m.chat.tasks.selectedID = taskID
			m.chat.tasks.detailScroll = 0
		}
	}
	return m, nil
}
