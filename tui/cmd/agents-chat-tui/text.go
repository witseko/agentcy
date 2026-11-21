package main

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// clean makes untrusted text safe to draw: it removes ANSI escape sequences, control
// characters and bidirectional overrides, so a model cannot move the cursor, rewrite the
// screen or send commands to the terminal. Newlines stay and tabs become spaces.
func clean(text string) string {
	stripped := ansi.Strip(text)
	var builder strings.Builder
	builder.Grow(len(stripped))
	for _, character := range stripped {
		switch {
		case character == '\n':
			builder.WriteRune(character)
		case character == '\t':
			builder.WriteString("    ")
		case unicode.IsControl(character), isBidiControl(character):
			continue
		default:
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

// cleanLine cleans text and folds it onto one line.
func cleanLine(text string) string {
	return strings.ReplaceAll(clean(text), "\n", " ")
}

// isBidiControl reports the Unicode characters that reorder how text is displayed.
func isBidiControl(character rune) bool {
	return (character >= '‪' && character <= '‮') ||
		(character >= '⁦' && character <= '⁩') ||
		character == '‎' || character == '‏' || character == '؜'
}

// wrapLines cleans text and wraps it to width columns, with indent before every line.
func wrapLines(text string, width int, indent string) []string {
	limit := max(width-ansi.StringWidth(indent), 10)
	var lines []string
	for _, paragraph := range strings.Split(clean(text), "\n") {
		wrapped := ansi.Wrap(paragraph, limit, "")
		for _, line := range strings.Split(wrapped, "\n") {
			lines = append(lines, indent+line)
		}
	}
	return lines
}

// fit cuts a single line to width columns.
func fit(line string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(line, width, "")
}

// idle formats a number of seconds as a short duration.
func idle(seconds int64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%dh", seconds/3600)
	}
}

// age formats the time since a Unix timestamp as a short duration.
func age(created *int64, now time.Time) string {
	if created == nil {
		return "-"
	}
	elapsed := now.Sub(time.Unix(*created, 0))
	switch {
	case elapsed < time.Minute:
		return "now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%dd", int(elapsed.Hours()/24))
	}
}
