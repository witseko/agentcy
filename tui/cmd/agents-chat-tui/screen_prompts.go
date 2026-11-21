package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const inviteBoxWidth = 76

// inviteState holds the invite window that lists the join prompts still to paste.
type inviteState struct {
	open     bool
	selected int
	// copied holds the folded names whose prompt was copied while this chat is open.
	copied map[string]bool
}

// openInvite shows the invite window and selects a slot by name when it is given.
func (m model) openInvite(name string) model {
	m.inviteWindow.open = true
	if m.inviteWindow.copied == nil {
		m.inviteWindow.copied = map[string]bool{}
	}
	slots := m.promptSlots()
	for index, slot := range slots {
		if name != "" && foldName(slot) == foldName(name) {
			m.inviteWindow.selected = index
		}
	}
	m.inviteWindow.selected = min(m.inviteWindow.selected, max(len(slots)-1, 0))
	return m
}

// promptSlots returns the manual slots that have not joined: invited members without a
// fresh-run launch, in team order.
func (m model) promptSlots() []string {
	var names []string
	for _, name := range m.slotNames() {
		member := m.chat.view.Status.Members[name]
		if member.State == "invited" && member.LaunchState == "" {
			names = append(names, name)
		}
	}
	return names
}

// knownInvitation returns the join prompt this session received for a slot.
func (m model) knownInvitation(name string) (JoinInvitation, bool) {
	invitation, found := m.invitations[m.chat.id][foldName(name)]
	return invitation, found
}

// inviteKey handles a key while the invite window shows.
func (m model) inviteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	slots := m.promptSlots()
	switch msg.String() {
	case "esc", "enter", "q":
		m.inviteWindow.open = false
		return m, nil
	case "up", "k":
		m.inviteWindow.selected = max(m.inviteWindow.selected-1, 0)
	case "down", "j":
		m.inviteWindow.selected = min(m.inviteWindow.selected+1, max(len(slots)-1, 0))
	case "c":
		if len(slots) == 0 {
			return m, nil
		}
		name := slots[min(m.inviteWindow.selected, len(slots)-1)]
		if _, found := m.knownInvitation(name); !found {
			return m.inform("No prompt for " + cleanLine(name) + " in this session. Press r for a new code."), nil
		}
		next, command := m.copyPrompt(name)
		updated := next.(model)
		updated.inviteWindow.selected = min(updated.inviteWindow.selected+1, len(slots)-1)
		return updated, command
	case "r":
		if len(slots) == 0 {
			return m, nil
		}
		name := slots[min(m.inviteWindow.selected, len(slots)-1)]
		return m.inform("Getting a new code for " + cleanLine(name) + "."), m.invite(name)
	}
	return m, nil
}

// copyPrompt copies a slot's join prompt to the clipboard and marks it as copied.
func (m model) copyPrompt(name string) (tea.Model, tea.Cmd) {
	invitation, _ := m.knownInvitation(name)
	if m.inviteWindow.copied == nil {
		m.inviteWindow.copied = map[string]bool{}
	}
	m.inviteWindow.copied[foldName(name)] = true
	copyText := m.clipboard
	prompt := clean(invitation.JoinPrompt)
	return m, func() tea.Msg { return copiedMsg{name: name, err: copyText(prompt)} }
}

// onCopied reports the clipboard result.
func (m model) onCopied(msg copiedMsg) model {
	if msg.err != nil {
		delete(m.inviteWindow.copied, foldName(msg.name))
		return m.inform("Not copied: " + msg.err.Error())
	}
	return m.inform("Copied the prompt for " + cleanLine(msg.name) + ". Paste it into its chat.")
}

// drawInvite draws the invite window over the main view.
func (m model) drawInvite(body []string) []string {
	box, row, col, _ := m.inviteLayout(len(body))
	return overlay(body, box, row, col, m.width)
}

// inviteLayout builds the invite window and places it on a body of the given height. slotOf
// gives the prompt slot index drawn on each box row, or -1.
func (m model) inviteLayout(bodyHeight int) (box []string, row int, col int, slotOf []int) {
	width := min(inviteBoxWidth, m.width-4)
	inner := width - 4
	slots := m.promptSlots()
	launched := 0
	for _, name := range m.slotNames() {
		member := m.chat.view.Status.Members[name]
		if member.State != "retired" && member.LaunchState != "" {
			launched++
		}
	}
	lineSlot := []int{-1, -1, -1, -1}
	lines := []string{
		brightStyle.Render("Paste these into your chats"),
		dimStyle.Render(fmt.Sprintf("%d slot(s) wait for a chat · %d run from a plugin", len(slots), launched)),
		dimStyle.Render("Paste each prompt into its chat. A code works once, for one hour."),
		"",
	}
	switch {
	case !m.chat.loaded:
		lines = append(lines, dimStyle.Render("Loading the slots."), "")
		lineSlot = append(lineSlot, -1, -1)
	case len(slots) == 0:
		lines = append(lines, okStyle.Render("✓")+textStyle.Render(" Every slot has joined or runs from a plugin."), "")
		lineSlot = append(lineSlot, -1, -1)
	}
	for index, name := range slots {
		member := m.chat.view.Status.Members[name]
		selected := index == min(m.inviteWindow.selected, len(slots)-1)
		action, _ := m.buttons(button{id: fmt.Sprintf("card:%d", index), label: "Copy"})
		mark := "  "
		if m.inviteWindow.copied[foldName(name)] {
			mark, action = okStyle.Render("✓ "), okStyle.Render("copied")
		}
		if selected && !m.inviteWindow.copied[foldName(name)] {
			mark = accentStyle.Render("› ")
		}
		title := mark + roleStyle(member.Role).Render(cleanLine(name)) + " " + dimStyle.Render(member.Role)
		gap := max(inner-ansi.StringWidth(title)-ansi.StringWidth(action), 1)
		prompt := dimStyle.Render("No prompt in this session. Press r for a new code.")
		if invitation, found := m.knownInvitation(name); found {
			prompt = textStyle.Render(cleanLine(invitation.JoinPrompt))
		}
		card := []string{title + strings.Repeat(" ", gap) + action, "  " + pad(prompt, inner-2)}
		for _, line := range card {
			switch {
			case selected:
				lines = append(lines, fill(line, inner, colorSelect))
			case m.hover == fmt.Sprintf("cardrow:%d", index):
				lines = append(lines, fill(line, inner, colorBand))
			default:
				lines = append(lines, line)
			}
			lineSlot = append(lineSlot, index)
		}
		lines = append(lines, "")
		lineSlot = append(lineSlot, -1)
	}
	done, _ := m.buttons(button{id: "done", label: "Done", primary: true})
	lines = append(lines, done+"   "+keyHints("c", "copy next", "r", "new code"))
	lineSlot = append(lineSlot, -1)

	box = []string{accentStyle.Render("┌" + strings.Repeat("─", width-2) + "┐")}
	slotOf = []int{-1}
	for index, line := range lines {
		box = append(box, fill(accentStyle.Render("│")+" "+pad(line, inner)+" "+accentStyle.Render("│"), width, colorBar))
		slotOf = append(slotOf, lineSlot[index])
	}
	box = append(box, accentStyle.Render("└"+strings.Repeat("─", width-2)+"┘"))
	slotOf = append(slotOf, -1)
	row = max((bodyHeight-len(box))/3, 0)
	col = max((m.width-width)/2, 0)
	return box, row, col, slotOf
}
