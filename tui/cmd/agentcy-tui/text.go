package main

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// clean makes untrusted text safe to draw without hiding any of it. Newlines stay, tabs
// become spaces and carriage returns are dropped. Other control characters (C0, DEL and C1)
// become U+FFFD so tampering stays visible. Format characters, line and paragraph
// separators and invisible fillers are removed, except the zero-width joiner and
// non-joiner that emoji and some scripts need.
func clean(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	for _, character := range text {
		switch {
		case character == '\n':
			builder.WriteRune(character)
		case character == '\t':
			builder.WriteString("    ")
		case character == '\r':
			continue
		case unicode.IsControl(character):
			builder.WriteRune(unicode.ReplacementChar)
		case isInvisible(character):
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

// isInvisible reports characters that draw nothing or reorder text, except the joiners.
func isInvisible(character rune) bool {
	switch character {
	case '\u200c', '\u200d':
		return false
	case '\u2028', '\u2029', '\u3164', '\u115f', '\u1160':
		return true
	}
	return unicode.Is(unicode.Cf, character)
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
