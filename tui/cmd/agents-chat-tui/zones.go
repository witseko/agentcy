package main

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// zone is a clickable part of the screen in screen cells. x1 is exclusive.
type zone struct {
	id     string
	x0, x1 int
	y      int
}

// place turns spans relative to a line start into zones on screen row y.
func place(spans []span, x int, y int) []zone {
	zones := make([]zone, 0, len(spans))
	for _, item := range spans {
		zones = append(zones, zone{id: item.id, x0: x + item.x0, x1: x + item.x1, y: y})
	}
	return zones
}

// zones lists the buttons and key hints on screen. It follows the layout the views draw:
// row 0 is the top bar, the body starts at row 1, and the key bar is the last row.
func (m model) zones() []zone {
	bodyHeight := max(m.height-2, 1)
	help := m.mainHelp()
	if m.screen == screenNew {
		help = m.formHelp()
	}
	_, hintSpans := m.hintBar(help)
	zones := place(hintSpans, 1, m.height-1)
	if m.screen == screenNew {
		return append(zones, m.formZones(bodyHeight)...)
	}
	if m.inviteWindow.open {
		return append(zones, m.inviteZones(bodyHeight)...)
	}
	list, center, team := m.columnWidths()
	if list > 0 {
		_, spans := m.buttons(button{id: "new", label: "+ New chat"})
		zones = append(zones, place(spans, 1, bodyHeight)...)
	}
	if m.screen != screenChat {
		return zones
	}
	centerStart := 0
	if list > 0 {
		centerStart = list + 1
	}
	if _, spans := m.headerButtons(); m.headerLayout(center).actions {
		zones = append(zones, place(spans, centerStart+center-1-buttonsWidth(spans), 1)...)
	}
	if !m.chat.closing {
		_, spans := m.sendButton()
		zones = append(zones, place(spans, centerStart+center-1-buttonsWidth(spans), bodyHeight-1)...)
	}
	if team > 0 {
		teamStart := centerStart + center + 1
		for _, row := range m.teamRows() {
			if row.copyRow >= 0 {
				_, spans := m.buttons(button{id: "copy:" + row.name, label: "Copy join prompt"})
				zones = append(zones, place(spans, teamStart+3, 1+row.copyRow)...)
			}
		}
		if m.chat.view.Status.Open {
			_, spans := m.buttons(button{id: "invite-member", label: "+ Invite member"})
			zones = append(zones, place(spans, teamStart+1, bodyHeight)...)
		}
	}
	return zones
}

// buttonsWidth returns the width a row of buttons takes.
func buttonsWidth(spans []span) int {
	if len(spans) == 0 {
		return 0
	}
	return spans[len(spans)-1].x1
}

// inviteZones lists the Copy and Done buttons of the invite window.
func (m model) inviteZones(bodyHeight int) []zone {
	box, top, left, slotOf := m.inviteLayout(bodyHeight)
	inner := min(inviteBoxWidth, m.width-4) - 4
	var zones []zone
	seen := map[int]bool{}
	for index, slot := range slotOf {
		if slot < 0 || seen[slot] {
			continue
		}
		seen[slot] = true
		if m.inviteWindow.copied[foldName(m.promptSlots()[slot])] {
			continue
		}
		_, spans := m.buttons(button{id: fmt.Sprintf("card:%d", slot), label: "Copy"})
		zones = append(zones, place(spans, left+2+inner-buttonsWidth(spans), 1+top+index)...)
	}
	_, spans := m.buttons(button{id: "done", label: "Done", primary: true})
	return append(zones, place(spans, left+2, 1+top+len(box)-2)...)
}

// formZones lists the buttons of the new chat form on the rows they show on.
func (m model) formZones(bodyHeight int) []zone {
	_, fieldOf, focusLine := m.formLayout()
	first := scrollFirst(len(fieldOf), focusLine, bodyHeight)
	left := formLeft(m.width)
	width := formWidth(m.width)
	var zones []zone
	for line, field := range fieldOf {
		row := line - first
		if row < 0 || row >= bodyHeight {
			continue
		}
		var spans []span
		x := left + 2
		switch kind, workerRow := m.form.field(field); {
		case field == formLineAddWorker:
			_, spans = m.buttons(button{id: "add", label: "+ Add worker"})
		case field == m.form.fieldCount()-1:
			_, spans = m.formButtons()
		case field >= 0 && kind == fieldWorkerName:
			_, spans = m.removeButton(workerRow)
			x = left + width - removeButtonWidth
		}
		zones = append(zones, place(spans, x, 1+row)...)
	}
	return zones
}

// zoneAt returns the id of the zone at a screen cell, or "".
func (m model) zoneAt(x int, y int) string {
	for _, item := range m.zones() {
		if y == item.y && x >= item.x0 && x < item.x1 {
			return item.id
		}
	}
	return ""
}

// hoverAt names what is under the pointer: a button or key hint, a chat, a member, a prompt
// card or a divider.
func (m model) hoverAt(x int, y int) string {
	if id := m.zoneAt(x, y); id != "" {
		return id
	}
	row := y - 1
	bodyHeight := max(m.height-2, 1)
	if row < 0 || row >= bodyHeight || m.screen == screenNew {
		return ""
	}
	if m.inviteWindow.open {
		box, top, left, slotOf := m.inviteLayout(bodyHeight)
		inside := row >= top && row < top+len(box) && x >= left && x < left+min(inviteBoxWidth, m.width-4)
		if inside && slotOf[row-top] >= 0 {
			return fmt.Sprintf("cardrow:%d", slotOf[row-top])
		}
		return ""
	}
	switch m.regionAt(x) {
	case regionLeftDivider:
		return "divider:left"
	case regionRightDivider:
		return "divider:right"
	case regionList:
		if index, found := m.listItemAt(row, bodyHeight); found {
			return fmt.Sprintf("chat:%d", index)
		}
	case regionTeam:
		for _, block := range m.teamRows() {
			if row >= block.top && row < block.top+block.size && m.chat.view.Status.Members[block.name].Role != "human" {
				return "member:" + block.name
			}
		}
	}
	return ""
}

// clickZone runs the action of a clicked button or key hint.
func (m model) clickZone(id string) (tea.Model, tea.Cmd) {
	name, argument, _ := strings.Cut(id, ":")
	switch name {
	case "key":
		if key, found := hintKey(argument); found {
			return m.Update(key)
		}
	case "new":
		return m.openForm()
	case "prompts":
		return m.openInvite(""), nil
	case "close":
		return m.startClosing()
	case "send":
		next, focus := m.setChatFocus(focusInput)
		sent, command := next.(model).submitInput()
		return sent, tea.Batch(focus, command)
	case "invite-member":
		return m.teamKey(tea.KeyPressMsg{Code: 'i', Text: "i"})
	case "copy":
		for index, member := range m.slotNames() {
			if member == argument {
				m.chat.selected = index
			}
		}
		next, focus := m.setChatFocus(focusTeam)
		copied, command := next.(model).copyMemberPrompt()
		return copied, tea.Batch(focus, command)
	case "card":
		slot, err := strconv.Atoi(argument)
		slots := m.promptSlots()
		if err != nil || slot < 0 || slot >= len(slots) {
			return m, nil
		}
		m.inviteWindow.selected = slot
		return m.copyPrompt(slots[slot])
	case "done":
		m.inviteWindow.open = false
		return m, nil
	case "add":
		return m.addWorker()
	case "remove":
		row, err := strconv.Atoi(argument)
		if err != nil || row < 0 || row >= len(m.form.workers) {
			return m, nil
		}
		return m.removeWorker(row)
	case "create":
		return m.submitForm()
	case "cancel":
		return m.formKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	}
	return m, nil
}

// hintKey returns the key press a clickable key hint stands for. Arrow hints describe a
// motion, not one key, so they are not clickable.
func hintKey(label string) (tea.KeyPressMsg, bool) {
	switch label {
	case "⏎":
		return tea.KeyPressMsg{Code: tea.KeyEnter}, true
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}, true
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}, true
	}
	if letter, found := strings.CutPrefix(label, "ctrl+"); found && len(letter) == 1 {
		return tea.KeyPressMsg{Code: rune(letter[0]), Mod: tea.ModCtrl}, true
	}
	if len(label) == 1 {
		return tea.KeyPressMsg{Code: rune(label[0]), Text: label}, true
	}
	return tea.KeyPressMsg{}, false
}
