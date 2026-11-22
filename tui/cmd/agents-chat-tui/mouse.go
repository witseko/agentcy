package main

import (
	tea "charm.land/bubbletea/v2"
)

// divider names a column divider that the user can drag.
type divider int

const (
	noDivider divider = iota
	leftDivider
	rightDivider
)

// wheelLines is how far one wheel step scrolls the transcript.
const wheelLines = 3

// toggleMouse turns mouse capture off, so the terminal selects and copies text, or back on.
func (m model) toggleMouse() model {
	m.mouseOff = !m.mouseOff
	m.dragging = noDivider
	if m.mouseOff {
		return m.inform("Mouse off: select text with your terminal. ctrl+o turns it back on.")
	}
	return m.inform("Mouse on: click, scroll and drag the dividers. ctrl+o turns it off.")
}

// region names the part of the main screen under a point.
type region int

const (
	regionNone region = iota
	regionList
	regionCenter
	regionTeam
	regionLeftDivider
	regionRightDivider
)

// regionAt finds the column under screen column x on the main screen.
func (m model) regionAt(x int) region {
	list, center, team := m.columnWidths()
	switch {
	case list > 0 && x < list:
		return regionList
	case list > 0 && x == list:
		return regionLeftDivider
	}
	start := 0
	if list > 0 {
		start = list + 1
	}
	switch {
	case x < start+center:
		return regionCenter
	case team > 0 && x == start+center:
		return regionRightDivider
	case team > 0:
		return regionTeam
	}
	return regionNone
}

// onClick routes a left click to the window, form or main screen part under it. Row 0 is
// the top bar, so body rows start at 1.
func (m model) onClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	if mouse.Button != tea.MouseLeft {
		return m, nil
	}
	if id := m.zoneAt(mouse.X, mouse.Y); id != "" {
		return m.clickZone(id)
	}
	row := mouse.Y - 1
	bodyHeight := max(m.height-2, 1)
	if row < 0 || row >= bodyHeight {
		return m, nil
	}
	switch {
	case m.screen == screenNew:
		return m.clickForm(mouse.X, row, bodyHeight)
	case m.inviteWindow.open:
		return m.clickInvite(mouse.X, row, bodyHeight)
	}
	switch m.regionAt(mouse.X) {
	case regionLeftDivider:
		m.dragging = leftDivider
	case regionRightDivider:
		m.dragging = rightDivider
	case regionList:
		return m.clickList(row, bodyHeight)
	case regionTeam:
		return m.clickTeam(row)
	case regionCenter:
		if m.screen == screenSearch {
			return m.clickSearch(row, bodyHeight)
		}
		if m.screen == screenChat && row >= bodyHeight-3 && !m.chat.closing && m.chat.retiring == "" {
			return m.setChatFocus(focusInput)
		}
	}
	return m, nil
}

// clickList opens the clicked chat. The buttons above and below the list are zones.
func (m model) clickList(row int, height int) (tea.Model, tea.Cmd) {
	index, found := m.listItemAt(row, height)
	if !found {
		return m, nil
	}
	m.list.selected = index
	if m.screen == screenChat && m.list.items[index].Conversation == m.chat.id {
		return m.setChatFocus(focusInput)
	}
	return m.openChat(m.list.items[index].Conversation)
}

// listItemAt returns the chat drawn at a body row. It follows the layout of sidebarView:
// two header rows, then three rows per chat.
func (m model) listItemAt(row int, height int) (int, bool) {
	if !m.list.loaded || len(m.list.items) == 0 || row < 2 {
		return 0, false
	}
	rows := max((height-4)/3, 1)
	first := max(m.list.selected-rows+1, 0)
	if (row-2)%3 == 2 {
		return 0, false
	}
	index := first + (row-2)/3
	if index >= len(m.list.items) || index >= first+rows {
		return 0, false
	}
	return index, true
}

// clickTeam selects the clicked member, and copies its join prompt when the click is on the
// copy line.
func (m model) clickTeam(row int) (tea.Model, tea.Cmd) {
	names := m.slotNames()
	for _, block := range m.teamRows() {
		if row < block.top || row >= block.top+block.size {
			continue
		}
		for index, name := range names {
			if name != block.name {
				continue
			}
			m.chat.selected = index
			next, command := m.setChatFocus(focusTeam)
			if row == block.copyRow {
				copied, copyCommand := next.(model).copyMemberPrompt()
				return copied, tea.Batch(command, copyCommand)
			}
			return next, command
		}
	}
	return m, nil
}

// clickInvite selects and copies the clicked prompt. A click outside the window closes it.
func (m model) clickInvite(x int, row int, height int) (tea.Model, tea.Cmd) {
	box, top, left, slotOf := m.inviteLayout(height)
	width := min(inviteBoxWidth, m.width-4)
	inside := row >= top && row < top+len(box) && x >= left && x < left+width
	if !inside {
		m.inviteWindow.open = false
		return m, nil
	}
	slot := slotOf[row-top]
	slots := m.promptSlots()
	if slot < 0 || slot >= len(slots) {
		return m, nil
	}
	m.inviteWindow.selected = slot
	if slots[slot] == openInviteCard {
		if _, found := m.knownOpenInvite(); !found {
			return m.inform("Open invite is active from another session. Press r to rotate and get a prompt."), nil
		}
		return m.copyOpenPrompt()
	}
	if _, found := m.knownInvitation(slots[slot]); !found {
		return m.inform("No prompt for " + cleanLine(slots[slot]) + " in this session. Press r for a new code."), nil
	}
	return m.copyPrompt(slots[slot])
}

// clickForm focuses the clicked field. A click on a worker's source changes it, and a click
// on the Create line creates the chat.
func (m model) clickForm(x int, row int, height int) (tea.Model, tea.Cmd) {
	if m.form.submitting {
		return m, nil
	}
	_, fieldOf, focusLine := m.formLayout()
	line := scrollFirst(len(fieldOf), focusLine, height) + row
	if line >= len(fieldOf) {
		return m, nil
	}
	field := fieldOf[line]
	switch {
	case field == formLineAddWorker:
		return m.addWorker()
	case field == formLineNone:
		return m, nil
	case field == m.form.fieldCount()-1:
		return m.submitForm()
	}
	kind, workerRow := m.form.field(field)
	if kind == fieldOpenInvite {
		command := m.form.setFocus(field)
		m.form.toggleOpenInvite(m.width, m.height)
		return m, command
	}
	if kind == fieldWorkerName && x-formLeft(m.width) >= sourceColumn {
		command := m.form.setFocus(field + 1)
		m.form.changeSource(workerRow, "right", len(m.plugins))
		return m, command
	}
	return m, m.form.setFocus(field)
}

// onDrag resizes a side column while its divider is held.
func (m model) onDrag(mouse tea.Mouse) model {
	switch m.dragging {
	case leftDivider:
		m.sidebarCols = m.clampSide(mouse.X, m.teamCols)
	case rightDivider:
		m.teamCols = m.clampSide(m.width-mouse.X-1, m.sidebarCols)
	default:
		return m
	}
	m.resizeInputs()
	m.clampChatScroll()
	return m
}

// clampSide keeps a side column width in range and leaves the center its minimum width.
func (m model) clampSide(width int, other int) int {
	room := m.width - other - 2 - minCenterWidth
	return max(min(width, maxSideWidth, room), minSideWidth)
}

// onWheel scrolls the chat list, the transcript or the team selection under the pointer.
func (m model) onWheel(mouse tea.Mouse) (model, tea.Cmd) {
	step := 0
	switch mouse.Button {
	case tea.MouseWheelUp:
		step = -1
	case tea.MouseWheelDown:
		step = 1
	default:
		return m, nil
	}
	if m.screen == screenNew || m.inviteWindow.open {
		return m, nil
	}
	switch m.regionAt(mouse.X) {
	case regionList:
		m.list.selected = min(max(m.list.selected+step, 0), max(len(m.list.items)-1, 0))
	case regionCenter:
		switch m.screen {
		case screenChat:
			return m.scrollTranscriptBy(-step * wheelLines)
		case screenSearch:
			m.search.selected = min(max(m.search.selected+step, 0), max(len(m.search.hits)-1, 0))
		}
	case regionTeam:
		m.chat.selected = min(max(m.chat.selected+step, 0), max(len(m.slotNames())-1, 0))
	}
	return m, nil
}
