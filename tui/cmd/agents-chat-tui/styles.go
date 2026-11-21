package main

import (
	"image/color"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Palette from the design canvas "agents-chat TUI redesign" (Tokens and legend board).
// The UI leaves the main background to the terminal and paints only bars and bands.
var (
	colorBackground = lipgloss.Color("#0f1115")
	colorBar        = lipgloss.Color("#151821")
	colorSelect     = lipgloss.Color("#1f2633")
	colorBand       = lipgloss.Color("#1a1d24")
	colorBorder     = lipgloss.Color("#2a2f3a")
	colorText       = lipgloss.Color("#d7dae0")
	colorBright     = lipgloss.Color("#ffffff")
	colorDim        = lipgloss.Color("#8a91a0")
	colorAccent     = lipgloss.Color("#7aa2f7")
	colorWarn       = lipgloss.Color("#e0a458")
	colorOK         = lipgloss.Color("#8bd5a0")
	colorLead       = lipgloss.Color("#c099ff")
	colorWorker     = lipgloss.Color("#7dcfff")
	colorHuman      = lipgloss.Color("#e6c07b")
)

var (
	textStyle   = lipgloss.NewStyle().Foreground(colorText)
	brightStyle = lipgloss.NewStyle().Foreground(colorBright).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(colorDim)
	headStyle   = lipgloss.NewStyle().Foreground(colorDim).Bold(true)
	accentStyle = lipgloss.NewStyle().Foreground(colorAccent)
	warnStyle   = lipgloss.NewStyle().Foreground(colorWarn)
	okStyle     = lipgloss.NewStyle().Foreground(colorOK)
	borderStyle = lipgloss.NewStyle().Foreground(colorBorder)
	keyStyle    = lipgloss.NewStyle().Foreground(colorText)
	buttonStyle = lipgloss.NewStyle().Background(colorAccent).Foreground(colorBackground).Bold(true)
)

// roleStyle colors a member name by role.
func roleStyle(role string) lipgloss.Style {
	switch role {
	case "lead", "co-lead":
		return lipgloss.NewStyle().Foreground(colorLead).Bold(true)
	case "human":
		return lipgloss.NewStyle().Foreground(colorHuman).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(colorWorker).Bold(true)
	}
}

// badge draws a short label on a solid background, for states that need attention.
func badge(text string, background color.Color) string {
	return lipgloss.NewStyle().Background(background).Foreground(colorBackground).Bold(true).
		Render(" " + text + " ")
}

// tag draws a quieter label for message kinds that are not alarms.
func tag(text string, foreground color.Color) string {
	return lipgloss.NewStyle().Background(colorSelect).Foreground(foreground).Render(" " + text + " ")
}

// keyHints draws "key action" pairs for the bottom bar.
func keyHints(pairs ...string) string {
	var parts []string
	for index := 0; index+1 < len(pairs); index += 2 {
		parts = append(parts, keyStyle.Render(pairs[index])+" "+dimStyle.Render(pairs[index+1]))
	}
	return strings.Join(parts, "   ")
}

// button is a clickable label. Its id names what a click does and whether the pointer is
// over it.
type button struct {
	id      string
	label   string
	primary bool
}

// span is the column range a clickable part covers, relative to the start of its line.
type span struct {
	id     string
	x0, x1 int
}

// buttons draws buttons two columns apart and returns where each one is.
func (m model) buttons(items ...button) (string, []span) {
	var builder strings.Builder
	var spans []span
	x := 0
	for index, item := range items {
		if index > 0 {
			builder.WriteString("  ")
			x += 2
		}
		text := " " + item.label + " "
		style := lipgloss.NewStyle().Background(colorSelect).Foreground(colorAccent)
		switch {
		case item.primary && m.hover == item.id:
			style = lipgloss.NewStyle().Background(colorBright).Foreground(colorBackground).Bold(true)
		case item.primary, m.hover == item.id:
			style = buttonStyle
		}
		builder.WriteString(style.Render(text))
		width := ansi.StringWidth(text)
		spans = append(spans, span{id: item.id, x0: x, x1: x + width})
		x += width
	}
	return builder.String(), spans
}

// hint is one "key action" pair in the bottom bar.
type hint struct {
	key    string
	action string
}

// hintBar draws the bottom bar hints and returns where each clickable key is. A hovered
// hint is highlighted.
func (m model) hintBar(hints []hint) (string, []span) {
	var builder strings.Builder
	var spans []span
	x := 0
	for index, item := range hints {
		if index > 0 {
			builder.WriteString("   ")
			x += 3
		}
		text := keyStyle.Render(item.key) + " " + dimStyle.Render(item.action)
		width := ansi.StringWidth(item.key) + 1 + ansi.StringWidth(item.action)
		id := "key:" + item.key
		if _, clickable := hintKey(item.key); clickable {
			if m.hover == id {
				text = lipgloss.NewStyle().Background(colorSelect).Foreground(colorBright).
					Render(item.key + " " + item.action)
			}
			spans = append(spans, span{id: id, x0: x, x1: x + width})
		}
		builder.WriteString(text)
		x += width
	}
	return builder.String(), spans
}

// pad cuts or fills a styled line to exactly width columns.
func pad(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = ansi.Truncate(line, width, "")
	if gap := width - ansi.StringWidth(line); gap > 0 {
		line += strings.Repeat(" ", gap)
	}
	return line
}

// fill pads a styled line to width and paints background behind all of it. Styled parts
// end with a reset, so the background is set again after each one.
func fill(line string, width int, background color.Color) string {
	set := ansi.NewStyle().BackgroundColor(background).String()
	line = pad(line, width)
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+set)
	line = strings.ReplaceAll(line, ansi.ResetStyle, ansi.ResetStyle+set)
	return set + line + ansi.ResetStyle
}

// column returns exactly height lines of width columns, cutting or padding the input.
func column(lines []string, width int, height int) []string {
	out := make([]string, height)
	for index := range out {
		line := ""
		if index < len(lines) {
			line = lines[index]
		}
		out[index] = pad(line, width)
	}
	return out
}

// joinColumns places columns of equal height side by side. dividers[i] is drawn between
// column i and column i+1.
func joinColumns(dividers []string, columns ...[]string) []string {
	if len(columns) == 0 {
		return nil
	}
	out := make([]string, len(columns[0]))
	for row := range out {
		var builder strings.Builder
		for index, lines := range columns {
			if index > 0 {
				builder.WriteString(dividers[index-1])
			}
			builder.WriteString(lines[row])
		}
		out[row] = builder.String()
	}
	return out
}

// rule draws a horizontal divider.
func rule(width int) string {
	return borderStyle.Render(strings.Repeat("─", max(width, 0)))
}

// overlay draws box over base with its top left corner at row and col.
func overlay(base []string, box []string, row int, col int, width int) []string {
	out := slices.Clone(base)
	for index, line := range box {
		target := row + index
		if target < 0 || target >= len(out) {
			continue
		}
		boxWidth := ansi.StringWidth(line)
		left := pad(ansi.Cut(out[target], 0, col), col)
		right := ansi.Cut(out[target], col+boxWidth, width)
		out[target] = left + line + right
	}
	return out
}
