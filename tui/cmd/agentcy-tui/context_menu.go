package main

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	minContextMenuWidth = 26
	maxContextMenuWidth = 46
)

// contextItem is one selectable operation in the right-click context menu.
type contextItem struct {
	id     string
	label  string
	key    string
	action func(model) (tea.Model, tea.Cmd)
}

// contextMenuState holds the active right-click context menu and its screen anchor.
type contextMenuState struct {
	open     bool
	title    string
	x        int
	y        int
	selected int
	items    []contextItem
}

// textCopiedMsg reports the clipboard result for a context-menu copy operation.
type textCopiedMsg struct {
	label string
	err   error
}

// openContextMenu opens a context menu anchored at screen cell (x, y) when items exist.
func (m model) openContextMenu(title string, x int, y int, items []contextItem) model {
	if len(items) == 0 {
		m.contextMenu = contextMenuState{}
		return m
	}
	m.contextMenu = contextMenuState{
		open:     true,
		title:    cleanLine(title),
		x:        x,
		y:        y,
		selected: 0,
		items:    items,
	}
	return m
}

// closeContextMenu dismisses any open context menu.
func (m model) closeContextMenu() model {
	m.contextMenu = contextMenuState{}
	return m
}

// copyText copies cleaned text to the clipboard and reports a status notice for label.
func (m model) copyText(label string, text string) (tea.Model, tea.Cmd) {
	return m, m.copyCmd(clean(text), func(err error) tea.Msg {
		return textCopiedMsg{label: label, err: err}
	})
}

// onTextCopied shows the result of a context-menu clipboard copy.
func (m model) onTextCopied(msg textCopiedMsg) model {
	if msg.err != nil {
		return m.showError("Not copied: " + msg.err.Error())
	}
	return m.showSuccess("Copied " + cleanLine(msg.label) + " to clipboard.")
}

// prefillComposer switches to the Chat tab input, fills text, and optionally shows a notice.
func (m model) prefillComposer(text string, notice string) (tea.Model, tea.Cmd) {
	m.chat.closing = false
	m.chat.deprecateOnClose = false
	m.chat.retiring = ""
	m.chat.tab = tabChat
	m.chat.input.SetValue(text)
	m.chat.input.CursorEnd()
	next, command := m.setChatFocus(focusInput)
	if notice != "" {
		return next.(model).inform(notice), command
	}
	return next, command
}

// contextMenuHelp returns the bottom-bar key hints while a context menu is open.
func (m model) contextMenuHelp() []hint {
	return []hint{{"↑↓", "select"}, {"⏎", "run"}, {"esc", "close"}}
}

// contextMenuKey handles keyboard navigation and shortcut activation inside an open context menu.
func (m model) contextMenuKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc", "q":
		return m.closeContextMenu(), nil
	case "up", "k":
		m.contextMenu.selected = max(m.contextMenu.selected-1, 0)
		return m, nil
	case "down", "j":
		m.contextMenu.selected = min(m.contextMenu.selected+1, max(len(m.contextMenu.items)-1, 0))
		return m, nil
	case "enter":
		return m.runContextItem(m.contextMenu.selected)
	}
	for index, item := range m.contextMenu.items {
		if item.key != "" && item.key == key {
			return m.runContextItem(index)
		}
	}
	return m, nil
}

// runContextItem closes the context menu and executes the chosen item's operation.
func (m model) runContextItem(index int) (tea.Model, tea.Cmd) {
	items := m.contextMenu.items
	m = m.closeContextMenu()
	if index < 0 || index >= len(items) || items[index].action == nil {
		return m, nil
	}
	return items[index].action(m)
}

// clickContextMenu runs a clicked menu item or dismisses the menu when clicking outside it.
func (m model) clickContextMenu(x int, y int) (tea.Model, tea.Cmd) {
	bodyHeight := max(m.height-2, 1)
	box, top, left, itemOf := m.contextMenuLayout(bodyHeight)
	row := y - 1
	width := m.contextMenuWidth()
	inside := row >= top && row < top+len(box) && x >= left && x < left+width
	if !inside {
		return m.closeContextMenu(), nil
	}
	itemIndex := itemOf[row-top]
	if itemIndex < 0 {
		return m, nil
	}
	return m.runContextItem(itemIndex)
}

// contextMenuHoverAt returns the hover id for a pointer position while the context menu is open.
func (m model) contextMenuHoverAt(x int, y int) string {
	if y == m.height-1 {
		return m.zoneAt(x, y)
	}
	bodyHeight := max(m.height-2, 1)
	if index, found := m.contextMenuItemAt(x, y, bodyHeight); found {
		return fmt.Sprintf("context:%d", index)
	}
	return ""
}

// contextMenuItemAt reports which context menu item sits under screen cell (x, y).
func (m model) contextMenuItemAt(x int, y int, bodyHeight int) (int, bool) {
	if !m.contextMenu.open {
		return 0, false
	}
	box, top, left, itemOf := m.contextMenuLayout(bodyHeight)
	row := y - 1
	width := m.contextMenuWidth()
	if row < top || row >= top+len(box) || x < left || x >= left+width {
		return 0, false
	}
	index := itemOf[row-top]
	return index, index >= 0 && index < len(m.contextMenu.items)
}

// drawContextMenu overlays the open context menu onto the screen body.
func (m model) drawContextMenu(body []string) []string {
	box, row, col, _ := m.contextMenuLayout(len(body))
	return overlay(body, box, row, col, m.width)
}

// contextMenuWidth computes the outer box width needed for the current context menu.
func (m model) contextMenuWidth() int {
	inner := ansi.StringWidth(m.contextMenu.title) + 2
	for _, item := range m.contextMenu.items {
		rowWidth := 2 + ansi.StringWidth(cleanLine(item.label))
		if item.key != "" {
			rowWidth += 2 + ansi.StringWidth(item.key)
		}
		inner = max(inner, rowWidth)
	}
	limit := max(min(maxContextMenuWidth, m.width-2), 12)
	return min(max(inner+4, minContextMenuWidth), limit)
}

// contextMenuLayout builds the context menu box and its clamped (row, col) position on body.
func (m model) contextMenuLayout(bodyHeight int) (box []string, row int, col int, itemOf []int) {
	width := m.contextMenuWidth()
	inner := max(width-4, 1)
	box = []string{accentStyle.Render("┌" + strings.Repeat("─", max(width-2, 0)) + "┐")}
	itemOf = []int{-1}
	if m.contextMenu.title != "" {
		header := " " + headStyle.Render(ansi.Truncate(m.contextMenu.title, max(inner-1, 1), "…"))
		box = append(box, fill(accentStyle.Render("│")+" "+pad(header, inner)+" "+accentStyle.Render("│"), width, colorBar))
		itemOf = append(itemOf, -1)
	}
	for index, item := range m.contextMenu.items {
		line := m.contextMenuRow(index, item, inner)
		box = append(box, fill(accentStyle.Render("│")+" "+line+" "+accentStyle.Render("│"), width, colorBar))
		itemOf = append(itemOf, index)
	}
	box = append(box, accentStyle.Render("└"+strings.Repeat("─", max(width-2, 0))+"┘"))
	itemOf = append(itemOf, -1)
	row = min(max(m.contextMenu.y-1, 0), max(bodyHeight-len(box), 0))
	col = min(max(m.contextMenu.x, 0), max(m.width-width, 0))
	return box, row, col, itemOf
}

// contextMenuRow formats one item line inside the context menu box.
func (m model) contextMenuRow(index int, item contextItem, inner int) string {
	selected := index == m.contextMenu.selected
	hovered := m.hover == fmt.Sprintf("context:%d", index)
	marker := "  "
	if selected {
		marker = accentStyle.Render("› ")
	}
	keyText := ""
	keyWidth := 0
	if item.key != "" {
		keyWidth = ansi.StringWidth(item.key) + 2
		keyText = dimStyle.Render(item.key)
		if selected || hovered {
			keyText = keyStyle.Render(item.key)
		}
	}
	labelRoom := max(inner-2-keyWidth, 4)
	label := ansi.Truncate(cleanLine(item.label), labelRoom, "…")
	styledLabel := textStyle.Render(label)
	if selected || hovered {
		styledLabel = brightStyle.Render(label)
	}
	left := marker + styledLabel
	gap := max(inner-ansi.StringWidth(left)-ansi.StringWidth(keyText), 1)
	line := left
	if keyText != "" {
		line += strings.Repeat(" ", gap) + keyText
	}
	switch {
	case selected:
		return fill(line, inner, colorSelect)
	case hovered:
		return fill(line, inner, colorBand)
	default:
		return pad(line, inner)
	}
}

// onRightClick opens a context menu for the item or region under the pointer.
func (m model) onRightClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	row := mouse.Y - 1
	bodyHeight := max(m.height-2, 1)
	if row < 0 || row >= bodyHeight {
		return m.closeContextMenu(), nil
	}
	if m.inviteWindow.open {
		return m.rightClickInvite(mouse.X, mouse.Y, row, bodyHeight), nil
	}
	if m.screen == screenNew {
		return m.rightClickForm(mouse.X, mouse.Y, row, bodyHeight)
	}
	switch m.regionAt(mouse.X) {
	case regionList:
		return m.rightClickList(mouse.X, mouse.Y, row, bodyHeight), nil
	case regionTeam:
		return m.rightClickTeam(mouse.X, mouse.Y, row, bodyHeight)
	case regionCenter:
		return m.rightClickCenter(mouse.X, mouse.Y, row, bodyHeight), nil
	}
	return m.closeContextMenu(), nil
}

// rightClickInvite opens a context menu for a prompt card or the invite window.
func (m model) rightClickInvite(x int, y int, row int, bodyHeight int) model {
	box, top, left, slotOf := m.inviteLayout(bodyHeight)
	width := min(inviteBoxWidth, m.width-4)
	inside := row >= top && row < top+len(box) && x >= left && x < left+width
	if !inside {
		return m.closeContextMenu()
	}
	slot := slotOf[row-top]
	slots := m.promptSlots()
	if slot >= 0 && slot < len(slots) {
		m.inviteWindow.selected = slot
		slotName := slots[slot]
		title := cleanLine(slotName)
		if slotName == openInviteCard {
			title = "Open invite"
		}
		return m.openContextMenu(title, x, y, m.inviteSlotMenuItems(slotName))
	}
	return m.openContextMenu("Join prompts", x, y, m.inviteWindowMenuItems())
}

// inviteSlotMenuItems builds context operations for one prompt card in the invite window.
func (m model) inviteSlotMenuItems(slotName string) []contextItem {
	if slotName == openInviteCard {
		return []contextItem{
			{
				id: "invite:copy-open", label: "Copy join prompt", key: "c",
				action: func(m model) (tea.Model, tea.Cmd) {
					if _, found := m.knownOpenInvite(); !found {
						return m.inform("Open invite is active from another session. Press r to rotate and get a prompt."), nil
					}
					return m.copyOpenPrompt()
				},
			},
			{
				id: "invite:rotate-open", label: "Rotate invite code", key: "r",
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.inform("Rotating the open invite code."), m.setOpenInvite(true)
				},
			},
			{
				id: "invite:disable-open", label: "Disable open invite", key: "o",
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.inform("Disabling open invite."), m.setOpenInvite(false)
				},
			},
		}
	}
	return []contextItem{
		{
			id: "invite:copy", label: "Copy join prompt", key: "c",
			action: func(m model) (tea.Model, tea.Cmd) {
				if _, found := m.knownInvitation(slotName); !found {
					return m.inform("Getting a new code for " + cleanLine(slotName) + "."), m.invite(slotName)
				}
				return m.copyPrompt(slotName)
			},
		},
		{
			id: "invite:reissue", label: "New join code", key: "r",
			action: func(m model) (tea.Model, tea.Cmd) {
				return m.inform("Getting a new code for " + cleanLine(slotName) + "."), m.invite(slotName)
			},
		},
	}
}

// inviteWindowMenuItems builds context operations for the invite window background.
func (m model) inviteWindowMenuItems() []contextItem {
	var items []contextItem
	if !m.chat.loaded || m.chat.view.Status.Open {
		label := "Enable open invite"
		enable := true
		if m.hasOpenInvite() {
			label = "Disable open invite"
			enable = false
		}
		items = append(items, contextItem{
			id: "invite:toggle-open", label: label, key: "o",
			action: func(m model) (tea.Model, tea.Cmd) {
				if enable {
					return m.inform("Enabling open invite."), m.setOpenInvite(true)
				}
				return m.inform("Disabling open invite."), m.setOpenInvite(false)
			},
		})
	}
	items = append(items, contextItem{
		id: "invite:done", label: "Close window",
		action: func(m model) (tea.Model, tea.Cmd) {
			m.inviteWindow.open = false
			return m, nil
		},
	})
	return items
}

// rightClickForm opens a context menu for a worker row or the new-chat form.
func (m model) rightClickForm(x int, y int, row int, bodyHeight int) (tea.Model, tea.Cmd) {
	if m.form.submitting {
		return m, nil
	}
	field, workerRow, ok := m.formWorkerAt(row, bodyHeight)
	if !ok {
		return m.openContextMenu("New chat", x, y, m.formGeneralMenuItems()), nil
	}
	cmd := m.form.setFocus(field)
	workerName := strings.TrimSpace(m.form.workers[workerRow].name.Value())
	if workerName == "" {
		workerName = fmt.Sprintf("Worker %d", workerRow+1)
	}
	return m.openContextMenu(workerName, x, y, m.formWorkerMenuItems(workerRow)), cmd
}

// formWorkerAt reports the field and worker row index at a body row on the new-chat form.
func (m model) formWorkerAt(row int, bodyHeight int) (int, int, bool) {
	_, fieldOf, focusLine := m.formLayout()
	line := scrollFirst(len(fieldOf), focusLine, bodyHeight) + row
	if line < 0 || line >= len(fieldOf) || fieldOf[line] < 0 {
		return 0, 0, false
	}
	field := fieldOf[line]
	kind, workerRow := m.form.field(field)
	isWorker := (kind == fieldWorkerName || kind == fieldWorkerSource) &&
		workerRow >= 0 && workerRow < len(m.form.workers)
	return field, workerRow, isWorker
}

// formWorkerMenuItems builds context operations for one worker slot on the new-chat form.
func (m model) formWorkerMenuItems(workerRow int) []contextItem {
	items := []contextItem{
		{
			id: "form:cycle-source", label: "Next join source",
			action: func(m model) (tea.Model, tea.Cmd) {
				if workerRow >= 0 && workerRow < len(m.form.workers) {
					m.form.changeSource(workerRow, "right", len(m.plugins))
				}
				return m, nil
			},
		},
		{
			id: "form:add-worker", label: "Add worker", key: "ctrl+n",
			action: func(m model) (tea.Model, tea.Cmd) { return m.addWorker() },
		},
	}
	if len(m.form.workers) > 1 || m.form.openInvite {
		items = append(items, contextItem{
			id: "form:remove-worker", label: "Remove worker", key: "ctrl+d",
			action: func(m model) (tea.Model, tea.Cmd) { return m.removeWorker(workerRow) },
		})
	}
	items = append(items, contextItem{
		id: "form:create", label: "Create chat", key: "ctrl+s",
		action: func(m model) (tea.Model, tea.Cmd) { return m.submitForm() },
	})
	return items
}

// formGeneralMenuItems builds context operations for the new-chat form.
func (m model) formGeneralMenuItems() []contextItem {
	toggleLabel := "Enable open invite"
	if m.form.openInvite {
		toggleLabel = "Disable open invite"
	}
	return []contextItem{
		{
			id: "form:add-worker", label: "Add worker", key: "ctrl+n",
			action: func(m model) (tea.Model, tea.Cmd) { return m.addWorker() },
		},
		{
			id: "form:toggle-open-invite", label: toggleLabel,
			action: func(m model) (tea.Model, tea.Cmd) {
				m.form.toggleOpenInvite(m.width, m.height)
				return m, nil
			},
		},
		{
			id: "form:create", label: "Create chat", key: "ctrl+s",
			action: func(m model) (tea.Model, tea.Cmd) { return m.submitForm() },
		},
		{
			id: "form:cancel", label: "Cancel",
			action: func(m model) (tea.Model, tea.Cmd) {
				return m.formKey(tea.KeyPressMsg{Code: tea.KeyEscape})
			},
		},
	}
}

// rightClickList opens a context menu for a conversation in the sidebar or the chat list itself.
func (m model) rightClickList(x int, y int, row int, bodyHeight int) model {
	if index, found := m.listItemAt(row, bodyHeight); found {
		m.list.selected = index
		item := m.list.items[index]
		return m.openContextMenu(item.Title, x, y, m.listItemMenuItems(item))
	}
	return m.openContextMenu("Chats", x, y, m.listEmptyMenuItems())
}

// listItemMenuItems builds context operations for one conversation in the sidebar list.
func (m model) listItemMenuItems(item ConversationSummary) []contextItem {
	items := []contextItem{
		{
			id: "list:open", label: "Open chat",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openChat(item.Conversation) },
		},
		{
			id: "list:copy-title", label: "Copy title",
			action: func(m model) (tea.Model, tea.Cmd) { return m.copyText("title", item.Title) },
		},
		{
			id: "list:rename", label: "Rename chat…", key: "e",
			action: func(m model) (tea.Model, tea.Cmd) { return m.startRename(item.Conversation, item.Title) },
		},
	}
	if !item.Open {
		items = append(items, contextItem{
			id: "list:reopen", label: "Reopen chat",
			action: func(m model) (tea.Model, tea.Cmd) { return m.reopenChat(item.Conversation, "") },
		})
		label := "Archive chat"
		targetArchived := true
		if m.list.archived {
			label = "Unarchive chat"
			targetArchived = false
		}
		items = append(items, contextItem{
			id: "list:archive", label: label, key: "a",
			action: func(m model) (tea.Model, tea.Cmd) { return m.requestArchive(item.Conversation, targetArchived) },
		})
	} else if m.screen == screenChat && m.chat.id == item.Conversation {
		items = append(items, contextItem{
			id: "list:close", label: "Close chat…", key: "ctrl+x",
			action: func(m model) (tea.Model, tea.Cmd) { return m.startClosing() },
		})
	}
	items = append(items,
		contextItem{
			id: "list:new", label: "New chat", key: "n",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openForm() },
		},
		contextItem{
			id: "list:search", label: "Search chats", key: "/",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openSearch() },
		},
	)
	return items
}

// listEmptyMenuItems builds context operations when right-clicking empty space in the sidebar.
func (m model) listEmptyMenuItems() []contextItem {
	viewLabel := "Show archived chats"
	targetArchived := true
	if m.list.archived {
		viewLabel = "Show active chats"
		targetArchived = false
	}
	return []contextItem{
		{
			id: "list:new", label: "New chat", key: "n",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openForm() },
		},
		{
			id: "list:search", label: "Search chats", key: "/",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openSearch() },
		},
		{
			id: "list:toggle-archive", label: viewLabel, key: "v",
			action: func(m model) (tea.Model, tea.Cmd) { return m.showArchive(targetArchived) },
		},
		{
			id: "list:refresh", label: "Refresh list", key: "r",
			action: func(m model) (tea.Model, tea.Cmd) { return m.inform("Refreshing."), m.loadList() },
		},
	}
}

// rightClickTeam opens a context menu for a clicked member, open task, or the team panel.
func (m model) rightClickTeam(x int, y int, row int, bodyHeight int) (tea.Model, tea.Cmd) {
	if m.screen != screenChat {
		return m.closeContextMenu(), nil
	}
	if name, found := m.teamMemberAt(row); found {
		member := m.chat.view.Status.Members[name]
		if member.Role == "human" {
			return m.openContextMenu("You", x, y, m.teamHumanMenuItems()), nil
		}
		var cmd tea.Cmd
		m, cmd = m.selectTeamMember(name)
		title := fmt.Sprintf("%s (%s)", cleanLine(name), cleanLine(member.Role))
		return m.openContextMenu(title, x, y, m.teamMemberMenuItems(name, member)), cmd
	}
	for _, item := range m.teamTaskRows(bodyHeight) {
		if row == item.row {
			title := fmt.Sprintf("Task #%d", item.id)
			return m.openContextMenu(title, x, y, m.teamTaskMenuItems(item.id)), nil
		}
	}
	return m.openContextMenu("Team", x, y, m.teamEmptyMenuItems()), nil
}

// teamMemberAt returns the member name drawn at a body row in the team panel.
func (m model) teamMemberAt(row int) (string, bool) {
	for _, block := range m.teamRows() {
		if row >= block.top && row < block.top+block.size {
			return block.name, true
		}
	}
	return "", false
}

// selectTeamMember selects name in the team panel and moves focus to focusTeam.
func (m model) selectTeamMember(name string) (model, tea.Cmd) {
	for index, slot := range m.slotNames() {
		if slot == name {
			m.chat.selected = index
			next, cmd := m.setChatFocus(focusTeam)
			return next.(model), cmd
		}
	}
	return m, nil
}

// teamMemberMenuItems builds context operations for a non-human member in the team panel.
func (m model) teamMemberMenuItems(name string, member MemberStatus) []contextItem {
	var items []contextItem
	isOpen := !m.chat.loaded || m.chat.view.Status.Open
	displayName := cleanLine(name)
	if isOpen {
		items = append(items,
			contextItem{
				id: "member:message", label: "Message @" + displayName,
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.prefillComposer("@"+name+" ", "Type your message to "+displayName+" and press enter.")
				},
			},
			contextItem{
				id: "member:ask", label: "Ask ?@" + displayName,
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.prefillComposer("?@"+name+" ", "Type your question for "+displayName+" and press enter.")
				},
			},
		)
	}
	if member.State == "invited" && member.LaunchState == "" {
		items = append(items, contextItem{
			id: "member:copy-prompt", label: "Copy join prompt", key: "c",
			action: func(m model) (tea.Model, tea.Cmd) { return m.copyMemberPrompt() },
		})
	}
	if isOpen && member.State == "invited" {
		items = append(items, contextItem{
			id: "member:reissue", label: "New join code", key: "r",
			action: func(m model) (tea.Model, tea.Cmd) {
				return m.inform("Getting a new code for " + displayName + "."), m.invite(name)
			},
		})
	}
	if isOpen && member.State != "invited" {
		items = append(items, contextItem{
			id: "member:reinvite", label: "Reinvite (new code, old chat loses access)", key: "r",
			action: func(m model) (tea.Model, tea.Cmd) {
				return m.inform("Reinviting " + displayName + "."), m.reinvite(name)
			},
		})
	}
	if isOpen && member.State != "retired" {
		launchLabel := "Launch from plugin…"
		if needsRelaunch(member) {
			launchLabel = "Relaunch from plugin…"
		}
		items = append(items,
			contextItem{
				id: "member:ping", label: "Ping member", key: "p",
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.inform("Pinging " + displayName + "."), m.pingMember(name)
				},
			},
			contextItem{
				id: "member:launch", label: launchLabel, key: "l",
				action: func(m model) (tea.Model, tea.Cmd) { return m.prefillLaunchFor(name) },
			},
		)
		items = append(items, m.teamMemberRoleItems(name, member)...)
		items = append(items,
			contextItem{
				id: "member:budget", label: "Set token budget…",
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.prefillComposer("/budget "+name+" ", "Type a token budget (e.g. 200k) and press enter.")
				},
			},
			contextItem{
				id: "member:retire", label: "Retire member…", key: "d",
				action: func(m model) (tea.Model, tea.Cmd) { return m.startRetiring(name) },
			},
		)
	}
	items = append(items, contextItem{
		id: "member:copy-name", label: "Copy name",
		action: func(m model) (tea.Model, tea.Cmd) { return m.copyText("name", name) },
	})
	return items
}

// teamMemberRoleItems returns role-change operations for an active member.
func (m model) teamMemberRoleItems(name string, member MemberStatus) []contextItem {
	displayName := cleanLine(name)
	var items []contextItem
	if member.Role != "lead" {
		items = append(items, contextItem{
			id: "member:role-lead", label: "Make lead", key: "m",
			action: func(m model) (tea.Model, tea.Cmd) {
				return m.inform("Making " + displayName + " lead."), m.setRole(name, "lead")
			},
		})
	}
	if member.Role != "co-lead" {
		items = append(items, contextItem{
			id: "member:role-co-lead", label: "Make co-lead",
			action: func(m model) (tea.Model, tea.Cmd) {
				return m.inform("Setting " + displayName + " as co-lead."), m.setRole(name, "co-lead")
			},
		})
	}
	if member.Role != "worker" {
		items = append(items, contextItem{
			id: "member:role-worker", label: "Make worker",
			action: func(m model) (tea.Model, tea.Cmd) {
				return m.inform("Setting " + displayName + " as worker."), m.setRole(name, "worker")
			},
		})
	}
	return items
}

// prefillLaunchFor puts a launch or relaunch command for name into the composer.
func (m model) prefillLaunchFor(name string) (tea.Model, tea.Cmd) {
	plugin := "plugin"
	if len(m.plugins) > 0 {
		plugin = m.plugins[0]
	}
	command := "/launch "
	if member, found := m.chat.view.Status.Members[name]; found && needsRelaunch(member) {
		command = "/relaunch "
	}
	return m.prefillComposer(command+name+" "+plugin, "Edit the plugin name if needed and press enter.")
}

// teamHumanMenuItems builds context operations for the human's own card in the team panel.
func (m model) teamHumanMenuItems() []contextItem {
	return []contextItem{
		{
			id: "human:focus-input", label: "Focus message input",
			action: func(m model) (tea.Model, tea.Cmd) { return m.setChatFocus(focusInput) },
		},
		{
			id: "human:prompts", label: "Join prompts", key: "ctrl+p",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openInvite(""), nil },
		},
	}
}

// teamTaskMenuItems builds context operations for an open task row in the team panel.
func (m model) teamTaskMenuItems(taskID int64) []contextItem {
	return []contextItem{
		{
			id: "team-task:open", label: "Open in Tasks tab",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openTaskInTab(taskID) },
		},
		{
			id: "team-task:jump", label: "Jump to message in Chat",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openChatAt(m.chat.id, taskID) },
		},
	}
}

// teamEmptyMenuItems builds context operations for empty space in the team panel.
func (m model) teamEmptyMenuItems() []contextItem {
	var items []contextItem
	if !m.chat.loaded || m.chat.view.Status.Open {
		openLabel := "Enable open invite"
		enable := true
		if m.hasOpenInvite() {
			openLabel = "Disable open invite"
			enable = false
		}
		items = append(items,
			contextItem{
				id: "team:invite", label: "Invite member…", key: "i",
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.prefillComposer("/invite ", "Type the new member's name and press enter.")
				},
			},
			contextItem{
				id: "team:prompts", label: "Join prompts", key: "ctrl+p",
				action: func(m model) (tea.Model, tea.Cmd) { return m.openInvite(""), nil },
			},
			contextItem{
				id: "team:open-invite", label: openLabel,
				action: func(m model) (tea.Model, tea.Cmd) {
					if enable {
						return m.inform("Enabling open invite."), m.setOpenInvite(true)
					}
					return m.inform("Disabling open invite."), m.setOpenInvite(false)
				},
			},
			contextItem{
				id: "team:close", label: "Close chat…", key: "x",
				action: func(m model) (tea.Model, tea.Cmd) { return m.startClosing() },
			},
		)
	} else {
		items = append(items, contextItem{
			id: "team:prompts", label: "Join prompts", key: "ctrl+p",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openInvite(""), nil },
		})
	}
	return items
}

// rightClickCenter opens a context menu for search results, tasks, transcript messages, or the conversation.
func (m model) rightClickCenter(x int, y int, row int, bodyHeight int) model {
	switch m.screen {
	case screenSearch:
		return m.rightClickSearch(x, y, row, bodyHeight)
	case screenChat:
		if m.chat.tab == tabTasks {
			return m.rightClickTasks(x, y, row, bodyHeight)
		}
		return m.rightClickChat(x, y, row, bodyHeight)
	default:
		return m.openContextMenu("Workspace", x, y, m.listEmptyMenuItems())
	}
}

// rightClickSearch opens a context menu for a clicked search result hit.
func (m model) rightClickSearch(x int, y int, row int, bodyHeight int) model {
	index, found := m.searchHitAt(row, bodyHeight)
	if !found {
		return m.closeContextMenu()
	}
	m.search.selected = index
	hit := m.search.hits[index]
	return m.openContextMenu(hit.Title, x, y, m.searchHitMenuItems(hit))
}

// searchHitMenuItems builds context operations for one search result hit.
func (m model) searchHitMenuItems(hit SearchHit) []contextItem {
	return []contextItem{
		{
			id: "search:open-hit", label: "Open at matching message",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openHit(hit) },
		},
		{
			id: "search:open-latest", label: "Open chat at latest",
			action: func(m model) (tea.Model, tea.Cmd) {
				var listCmd tea.Cmd
				if m.list.archived != hit.Archived {
					m.list = listState{archived: hit.Archived}
					listCmd = m.loadList()
				}
				m.search.input.Blur()
				next, cmd := m.openChat(hit.Conversation)
				return next, tea.Batch(cmd, listCmd)
			},
		},
		{
			id: "search:copy-snippet", label: "Copy snippet",
			action: func(m model) (tea.Model, tea.Cmd) { return m.copyText("snippet", hit.Snippet) },
		},
	}
}

// rightClickTasks opens a context menu for a task in the Tasks tab.
func (m model) rightClickTasks(x int, y int, row int, bodyHeight int) model {
	if taskID, found := m.taskRowAt(row, bodyHeight); found {
		m.chat.focus = focusInput
		if m.chat.tasks.selectedID != taskID {
			m.chat.tasks.selectedID = taskID
			m.chat.tasks.detailScroll = 0
		}
	}
	if task, _, ok := m.selectedTask(); ok {
		title := fmt.Sprintf("Task #%d · %s", task.ID, cleanLine(task.Owner))
		return m.openContextMenu(title, x, y, m.taskMenuItems(task))
	}
	return m.openContextMenu("Tasks", x, y, m.chatConversationMenuItems())
}

// taskMenuItems builds context operations for a task in the Tasks tab.
func (m model) taskMenuItems(task TaskDetail) []contextItem {
	items := []contextItem{
		{
			id: "task:jump", label: "Jump to message in Chat",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openChatAt(m.chat.id, task.ID) },
		},
	}
	isOpen := !m.chat.loaded || m.chat.view.Status.Open
	owner := task.Owner
	if isOpen && owner != "" && m.chat.view.Status.Members[owner].Role != "human" {
		displayOwner := cleanLine(owner)
		items = append(items,
			contextItem{
				id: "task:message-owner", label: "Message @" + displayOwner,
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.prefillComposer("@"+owner+" ", "Type your message to "+displayOwner+" and press enter.")
				},
			},
			contextItem{
				id: "task:ask-owner", label: "Ask ?@" + displayOwner,
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.prefillComposer("?@"+owner+" ", "Type your question for "+displayOwner+" and press enter.")
				},
			},
		)
	}
	items = append(items,
		contextItem{
			id: "task:copy-body", label: "Copy task description",
			action: func(m model) (tea.Model, tea.Cmd) { return m.copyText("task description", task.Body) },
		},
		contextItem{
			id: "task:cycle-filter", label: "Cycle filter", key: "f",
			action: func(m model) (tea.Model, tea.Cmd) {
				m.setTaskFilter((m.chat.tasks.filter + 1) % 3)
				return m, nil
			},
		},
		contextItem{
			id: "task:refresh", label: "Refresh tasks", key: "r",
			action: func(m model) (tea.Model, tea.Cmd) {
				m = m.inform("Refreshing tasks.")
				return m.loadTasks()
			},
		},
	)
	return items
}

// rightClickChat opens a context menu for a transcript message or the conversation itself.
func (m model) rightClickChat(x int, y int, row int, bodyHeight int) model {
	if message, found := m.transcriptMessageAt(row, bodyHeight); found {
		m.chat.highlight = message.ID
		author := cleanLine(message.Label)
		if m.chat.view.Status.Members[message.Label].Role == "human" {
			author = "You"
		}
		title := fmt.Sprintf("Message #%d · %s", message.ID, author)
		return m.openContextMenu(title, x, y, m.transcriptMessageMenuItems(message))
	}
	return m.openContextMenu("Conversation", x, y, m.chatConversationMenuItems())
}

// transcriptMessageAt returns the transcript message drawn at a center-column body row.
func (m model) transcriptMessageAt(row int, bodyHeight int) (TranscriptMessage, bool) {
	if !m.chat.loaded || len(m.chat.messages) == 0 {
		return TranscriptMessage{}, false
	}
	_, center, _ := m.columnWidths()
	headerLen := m.chatHeaderHeight(center)
	transcriptHeight := max(bodyHeight-headerLen-composerLines, 1)
	visibleRow := row - headerLen
	if visibleRow < 0 || visibleRow >= transcriptHeight {
		return TranscriptMessage{}, false
	}
	lines := m.transcriptLines(center)
	hidden := min(m.chat.scroll, max(len(lines)-transcriptHeight, 0))
	end := len(lines) - hidden
	start := max(end-transcriptHeight, 0)
	lineIndex := start + visibleRow
	if lineIndex < 0 || lineIndex >= end {
		return TranscriptMessage{}, false
	}
	return m.messageAtTranscriptLine(lineIndex, center)
}

// messageAtTranscriptLine maps a line index inside transcriptLines(width) to its TranscriptMessage.
func (m model) messageAtTranscriptLine(lineIndex int, width int) (TranscriptMessage, bool) {
	cursor := 0
	if m.chat.hasOlder {
		cursor = 1
	}
	cache := m.chat.linesCache
	for _, message := range m.chat.messages {
		var msgLines int
		if cache != nil && cache.width == width {
			if cached, ok := cache.perMessage[message.ID]; ok && len(cached.lines) > 0 {
				msgLines = len(cached.lines) - 1
			}
		}
		if msgLines == 0 {
			msgLines = 1 + len(wrapLines(message.Body, width-1, messageIndent))
		}
		if lineIndex >= cursor && lineIndex < cursor+msgLines {
			return message, true
		}
		cursor += msgLines + 1
	}
	return TranscriptMessage{}, false
}

// transcriptMessageMenuItems builds context operations for a message in the chat transcript.
func (m model) transcriptMessageMenuItems(message TranscriptMessage) []contextItem {
	items := []contextItem{
		{
			id: "msg:copy", label: "Copy message",
			action: func(m model) (tea.Model, tea.Cmd) { return m.copyText("message", message.Body) },
		},
	}
	isOpen := !m.chat.loaded || m.chat.view.Status.Open
	author := message.Label
	if isOpen && author != "" && m.chat.view.Status.Members[author].Role != "human" {
		displayAuthor := cleanLine(author)
		items = append(items,
			contextItem{
				id: "msg:reply", label: "Message @" + displayAuthor,
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.prefillComposer("@"+author+" ", "Type your message to "+displayAuthor+" and press enter.")
				},
			},
			contextItem{
				id: "msg:ask", label: "Ask ?@" + displayAuthor,
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.prefillComposer("?@"+author+" ", "Type your question for "+displayAuthor+" and press enter.")
				},
			},
		)
	}
	if message.Kind == "task" {
		taskID := message.ID
		items = append(items, contextItem{
			id:    "msg:open-task",
			label: "Open Task #" + strconv.FormatInt(taskID, 10) + " in Tasks",
			action: func(m model) (tea.Model, tea.Cmd) {
				return m.openTaskInTab(taskID)
			},
		})
	} else if message.ReplyTo != nil {
		targetID := *message.ReplyTo
		items = append(items,
			contextItem{
				id:    "msg:open-task",
				label: "Open Task #" + strconv.FormatInt(targetID, 10) + " in Tasks",
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.openTaskInTab(targetID)
				},
			},
			contextItem{
				id:    "msg:jump-parent",
				label: "Jump to #" + strconv.FormatInt(targetID, 10),
				action: func(m model) (tea.Model, tea.Cmd) {
					return m.openChatAt(m.chat.id, targetID)
				},
			},
		)
	}
	items = append(items, contextItem{
		id: "msg:copy-brief", label: "Copy brief",
		action: func(m model) (tea.Model, tea.Cmd) { return m.copyText("brief", m.chat.view.Brief) },
	})
	return items
}

// chatConversationMenuItems builds context operations for the open conversation header or empty area.
func (m model) chatConversationMenuItems() []contextItem {
	items := []contextItem{
		{
			id: "chat:copy-brief", label: "Copy brief",
			action: func(m model) (tea.Model, tea.Cmd) { return m.copyText("brief", m.chat.view.Brief) },
		},
	}
	if m.chat.loaded {
		items = append(items, contextItem{
			id: "chat:rename", label: "Rename chat…",
			action: func(m model) (tea.Model, tea.Cmd) { return m.startRename(m.chat.id, chatTitle(m.chat.view)) },
		})
	}
	if m.chat.tab == tabTasks {
		items = append(items, contextItem{
			id: "chat:tab-chat", label: "Switch to Chat tab", key: "ctrl+t",
			action: func(m model) (tea.Model, tea.Cmd) { return m.setChatTab(tabChat) },
		})
	} else {
		items = append(items, contextItem{
			id: "chat:tab-tasks", label: "Switch to Tasks tab", key: "ctrl+t",
			action: func(m model) (tea.Model, tea.Cmd) { return m.setChatTab(tabTasks) },
		})
	}
	items = append(items, contextItem{
		id: "chat:prompts", label: "Join prompts", key: "ctrl+p",
		action: func(m model) (tea.Model, tea.Cmd) { return m.openInvite(""), nil },
	})
	if !m.chat.loaded || m.chat.view.Status.Open {
		items = append(items, contextItem{
			id: "chat:close", label: "Close chat…", key: "ctrl+x",
			action: func(m model) (tea.Model, tea.Cmd) { return m.startClosing() },
		})
	} else {
		items = append(items, contextItem{
			id: "chat:reopen", label: "Reopen chat",
			action: func(m model) (tea.Model, tea.Cmd) { return m.reopenChat(m.chat.id, "") },
		})
		label := "Archive chat"
		targetArchived := true
		if m.chat.view.Archived {
			label = "Unarchive chat"
			targetArchived = false
		}
		items = append(items, contextItem{
			id: "chat:archive", label: label,
			action: func(m model) (tea.Model, tea.Cmd) { return m.requestArchive(m.chat.id, targetArchived) },
		})
	}
	return items
}
