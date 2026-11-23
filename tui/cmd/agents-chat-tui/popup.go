package main

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// popupKind says what a notice popup reports.
type popupKind int

const (
	popupNone popupKind = iota
	// popupError stays until the user dismisses it.
	popupError
	// popupSuccess confirms a finished action and closes by itself.
	popupSuccess
)

const (
	popupMaxWidth = 64
	popupMinWidth = 24
	// popupSuccessTicks keeps a success popup on screen for this many poll ticks.
	popupSuccessTicks = 4
)

var colorError = lipgloss.Color("#f7768e")

// popupState is the notice popup over the screen, if any.
type popupState struct {
	kind popupKind
	text string
	// ticks counts down to an automatic close. Zero means the popup stays until dismissed.
	ticks int
}

// showError opens a red popup that stays until a key press or click dismisses it.
func (m model) showError(text string) model {
	m.popup = popupState{kind: popupError, text: cleanLine(text)}
	return m
}

// showSuccess opens a green popup that confirms a finished action and closes by itself.
func (m model) showSuccess(text string) model {
	m.popup = popupState{kind: popupSuccess, text: cleanLine(text), ticks: popupSuccessTicks}
	return m
}

// expirePopup counts down a self-closing popup on each tick.
func (m model) expirePopup() model {
	if m.popup.ticks > 0 {
		m.popup.ticks--
		if m.popup.ticks == 0 {
			m.popup = popupState{}
		}
	}
	return m
}

// popupKey dismisses the popup. Esc and enter only dismiss an error; every other key, and
// every key under a success notice, also reaches the screen, so typing loses no key.
func (m model) popupKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	wasError := m.popup.kind == popupError
	m.popup = popupState{}
	switch msg.String() {
	case "esc", "enter":
		if wasError {
			return m, nil
		}
	}
	return m.handleKey(msg)
}

// drawPopup places the notice popup near the top of the body, centered.
func (m model) drawPopup(body []string) []string {
	title, style := " Done ", lipgloss.NewStyle().Foreground(colorOK)
	footer := ""
	if m.popup.kind == popupError {
		title, style = " Error ", lipgloss.NewStyle().Foreground(colorError)
		footer = "Press esc or click to dismiss."
	}
	width := popupWidth(m.width, ansi.StringWidth(m.popup.text)+6)
	lines := wrapLines(m.popup.text, width-4, "")
	for index, line := range lines {
		lines[index] = textStyle.Render(line)
	}
	if footer != "" {
		lines = append(lines, "", dimStyle.Render(footer))
	}
	box := framedBox(title, style, lines, width)
	return overlay(body, box, min(2, max(len(body)-len(box), 0)), max((m.width-width)/2, 0), m.width)
}

// drawConfirm places the close or retire confirmation dialog in the middle of the body.
func (m model) drawConfirm(body []string) []string {
	width := popupWidth(m.width, popupMaxWidth)
	title := " Retire " + cleanLine(m.chat.retiring) + "? "
	description := "Posting stops, unused codes expire, and any running plugin is cancelled."
	if m.chat.closing {
		title = " Close this chat? "
		description = "Participants can still read it. Posting stops and unused codes expire."
	}
	var lines []string
	for _, line := range wrapLines(description, width-4, "") {
		lines = append(lines, textStyle.Render(line))
	}
	if m.chat.closing {
		lines = append(lines, "", dimStyle.Render("Summary ")+m.chat.summary.View())
	}
	lines = append(lines, "", keyHints("⏎", "confirm", "esc", "cancel"))
	box := framedBox(title, warnStyle, lines, width)
	row := max((len(body)-len(box))/2, 0)
	return overlay(body, box, row, max((m.width-width)/2, 0), m.width)
}

// popupWidth returns a box width that fits the wanted width and the terminal.
func popupWidth(screenWidth int, wanted int) int {
	limit := max(min(popupMaxWidth, screenWidth-4), 12)
	return min(max(wanted, popupMinWidth), limit)
}

// framedBox draws lines inside a colored border with title in the top edge, on the bar color.
func framedBox(title string, style lipgloss.Style, lines []string, width int) []string {
	inner := max(width-4, 1)
	title = ansi.Truncate(title, max(width-4, 1), "…")
	top := "┌─" + title + strings.Repeat("─", max(width-3-ansi.StringWidth(title), 0)) + "┐"
	box := []string{fill(style.Bold(true).Render(top), width, colorBar)}
	edge := style.Render("│")
	box = append(box, fill(edge+strings.Repeat(" ", width-2)+edge, width, colorBar))
	for _, line := range lines {
		box = append(box, fill(edge+" "+pad(line, inner)+" "+edge, width, colorBar))
	}
	box = append(box, fill(edge+strings.Repeat(" ", width-2)+edge, width, colorBar))
	box = append(box, fill(style.Render("└"+strings.Repeat("─", max(width-2, 0))+"┘"), width, colorBar))
	return box
}
