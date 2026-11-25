package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	inviteBoxWidth = 76
	openInviteCard = "\x00open-invite"
)

// inviteState holds the invite window that lists the join prompts still to paste.
type inviteState struct {
	open     bool
	selected int
	// copied holds the folded names whose prompt was copied while this chat is open.
	copied map[string]bool
	// viewing shows the selected prompt alone, so the terminal can select all of it.
	viewing bool
}

// openInvite shows the invite window and selects a slot by name when it is given.
func (m model) openInvite(name string) model {
	m.inviteWindow.open = true
	m.inviteWindow.viewing = false
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

// hasOpenInvite reports whether the open conversation currently has an active shared invite.
func (m model) hasOpenInvite() bool {
	if m.chat.loaded {
		return m.chat.view.Status.OpenInvite
	}
	_, found := m.knownOpenInvite()
	return found
}

// knownOpenInvite returns the shared open-invite prompt this session received for the open chat.
func (m model) knownOpenInvite() (OpenInviteInvitation, bool) {
	invitation, found := m.openInvites[m.chat.id]
	return invitation, found
}

// promptSlots returns the open-invite card (when enabled) followed by manual slots that have
// not joined: invited members without a fresh-run launch, in team order.
func (m model) promptSlots() []string {
	if m.chat.loaded && len(m.chat.view.Status.Members) > 0 && !m.chat.view.Status.Open {
		return nil
	}
	var names []string
	if m.hasOpenInvite() {
		names = append(names, openInviteCard)
	}
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

// selectedPrompt returns the join prompt of the selected slot when this session knows it.
func (m model) selectedPrompt() (string, bool) {
	slots := m.promptSlots()
	if len(slots) == 0 {
		return "", false
	}
	name := slots[min(m.inviteWindow.selected, len(slots)-1)]
	if name == openInviteCard {
		invitation, found := m.knownOpenInvite()
		return invitation.JoinPrompt, found
	}
	invitation, found := m.knownInvitation(name)
	return invitation.JoinPrompt, found
}

// promptViewKey handles a key while one prompt shows alone: c copies it, other keys go back.
func (m model) promptViewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "c":
		prompt, _ := m.selectedPrompt()
		return m, m.copyCmd(clean(prompt), func(err error) tea.Msg {
			return textCopiedMsg{label: "the prompt", err: err}
		})
	case "esc", "enter", "q", "v":
		m.inviteWindow.viewing = false
	}
	return m, nil
}

// promptView draws the selected prompt alone, wrapped at spaces and without borders or mouse
// capture, so a terminal selection copies the whole prompt and nothing else.
func (m model) promptView() tea.View {
	prompt, _ := m.selectedPrompt()
	width := max(m.width-2, 20)
	lines := []string{"", ansi.Wordwrap(clean(prompt), width, ""), ""}
	lines = append(lines, dimStyle.Render(fmt.Sprintf("Select the text with your mouse. %s copy · %s back",
		accentStyle.Render("c"), accentStyle.Render("esc"))))
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

// inviteKey handles a key while the invite window shows.
func (m model) inviteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.inviteWindow.viewing {
		return m.promptViewKey(msg)
	}
	slots := m.promptSlots()
	switch msg.String() {
	case "v":
		if _, found := m.selectedPrompt(); !found {
			return m.inform("No prompt to show. Press r for a new code."), nil
		}
		m.inviteWindow.viewing = true
		return m, nil
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
		if name == openInviteCard {
			if _, found := m.knownOpenInvite(); !found {
				return m.inform("Open invite is active from another session. Press r to rotate and get a prompt."), nil
			}
			next, command := m.copyOpenPrompt()
			updated := next.(model)
			updated.inviteWindow.selected = min(updated.inviteWindow.selected+1, len(slots)-1)
			return updated, command
		}
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
		if name == openInviteCard {
			return m.inform("Rotating the open invite code."), m.setOpenInvite(true)
		}
		return m.inform("Getting a new code for " + cleanLine(name) + "."), m.invite(name)
	case "o":
		if m.chat.loaded && !m.chat.view.Status.Open {
			return m.showError("This chat is closed."), nil
		}
		enable := !m.hasOpenInvite()
		if enable {
			return m.inform("Enabling open invite."), m.setOpenInvite(true)
		}
		return m.inform("Disabling open invite."), m.setOpenInvite(false)
	}
	return m, nil
}

// copyOpenPrompt copies the shared open-invite prompt to the clipboard.
func (m model) copyOpenPrompt() (tea.Model, tea.Cmd) {
	invitation, _ := m.knownOpenInvite()
	if m.inviteWindow.copied == nil {
		m.inviteWindow.copied = map[string]bool{}
	}
	m.inviteWindow.copied[openInviteCard] = true
	return m, m.copyCmd(clean(invitation.JoinPrompt), func(err error) tea.Msg {
		return copiedMsg{name: openInviteCard, err: err}
	})
}

// copyPrompt copies a slot's join prompt to the clipboard and marks it as copied.
func (m model) copyPrompt(name string) (tea.Model, tea.Cmd) {
	invitation, _ := m.knownInvitation(name)
	if m.inviteWindow.copied == nil {
		m.inviteWindow.copied = map[string]bool{}
	}
	m.inviteWindow.copied[foldName(name)] = true
	return m, m.copyCmd(clean(invitation.JoinPrompt), func(err error) tea.Msg {
		return copiedMsg{name: name, err: err}
	})
}

// onCopied reports the clipboard result.
func (m model) onCopied(msg copiedMsg) model {
	key := foldName(msg.name)
	if msg.name == openInviteCard {
		key = openInviteCard
	}
	if msg.err != nil {
		delete(m.inviteWindow.copied, key)
		return m.showError("Not copied: " + msg.err.Error())
	}
	if msg.name == openInviteCard {
		return m.showSuccess("Copied the shared open-invite prompt. Paste it into any chat to join.")
	}
	return m.showSuccess("Copied the prompt for " + cleanLine(msg.name) + ". Paste it into its chat.")
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
		selected := index == min(m.inviteWindow.selected, len(slots)-1)
		action, _ := m.buttons(button{id: fmt.Sprintf("card:%d", index), label: "Copy"})
		copyKey := foldName(name)
		if name == openInviteCard {
			copyKey = openInviteCard
		}
		mark := "  "
		if m.inviteWindow.copied[copyKey] {
			mark, action = okStyle.Render("✓ "), okStyle.Render("copied")
		}
		if selected && !m.inviteWindow.copied[copyKey] {
			mark = accentStyle.Render("› ")
		}
		var title, prompt string
		if name == openInviteCard {
			title = mark + accentStyle.Render("Open invite") + " " + dimStyle.Render("anyone (reusable)")
			prompt = dimStyle.Render("Active from another session. Press r to rotate and get a prompt.")
			if invitation, found := m.knownOpenInvite(); found {
				prompt = textStyle.Render(cleanLine(invitation.JoinPrompt))
			}
		} else {
			member := m.chat.view.Status.Members[name]
			title = mark + roleStyle(member.Role).Render(cleanLine(name)) + " " + dimStyle.Render(member.Role)
			prompt = dimStyle.Render("No prompt in this session. Press r for a new code.")
			if invitation, found := m.knownInvitation(name); found {
				prompt = textStyle.Render(cleanLine(invitation.JoinPrompt))
			}
		}
		gap := max(inner-ansi.StringWidth(title)-ansi.StringWidth(action), 1)
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
	lines = append(lines, done+"   "+keyHints("c", "copy next", "v", "view", "r", "new code", "o", "open invite"))
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
