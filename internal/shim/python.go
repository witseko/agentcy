package shim

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/witseko/agentcy/internal/wire"
)

// pythonDumps encodes a value like Python's json.dumps with its defaults: ", " and ": "
// separators and every non-ASCII character escaped as \uXXXX.
func pythonDumps(value wire.Value) string {
	var builder strings.Builder
	writePythonValue(&builder, value)
	return builder.String()
}

// writePythonValue writes one value in json.dumps form.
func writePythonValue(builder *strings.Builder, value wire.Value) {
	switch value.Kind {
	case wire.Array:
		builder.WriteByte('[')
		for index, item := range value.Items {
			if index > 0 {
				builder.WriteString(", ")
			}
			writePythonValue(builder, item)
		}
		builder.WriteByte(']')
	case wire.Object:
		builder.WriteByte('{')
		for index, key := range value.Keys() {
			if index > 0 {
				builder.WriteString(", ")
			}
			member, _ := value.Get(key)
			writePythonString(builder, key)
			builder.WriteString(": ")
			writePythonValue(builder, member)
		}
		builder.WriteByte('}')
	case wire.String:
		writePythonString(builder, value.Text)
	default:
		builder.WriteString(value.Compact())
	}
}

// pythonShortEscape returns json.dumps's two-character escape of a character, or "".
func pythonShortEscape(character rune) string {
	switch character {
	case '"':
		return `\"`
	case '\\':
		return `\\`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	}
	return ""
}

// writePythonString writes a string with json.dumps's ensure_ascii escaping.
func writePythonString(builder *strings.Builder, text string) {
	builder.WriteByte('"')
	for _, character := range text {
		if escaped := pythonShortEscape(character); escaped != "" {
			builder.WriteString(escaped)
			continue
		}
		switch {
		case character < 0x20 || (character > 0x7e && character <= 0xffff):
			fmt.Fprintf(builder, `\u%04x`, character)
		case character > 0xffff:
			high, low := utf16.EncodeRune(character)
			fmt.Fprintf(builder, `\u%04x\u%04x`, high, low)
		default:
			builder.WriteRune(character)
		}
	}
	builder.WriteByte('"')
}

// pythonRepr quotes a string the way Python's repr does for text without quotes or
// backslashes, which covers the fixed texts it is used for.
func pythonRepr(text string) string {
	if strings.Contains(text, "'") && !strings.Contains(text, `"`) {
		return `"` + text + `"`
	}
	quoted := strconv.Quote(text)
	return "'" + strings.ReplaceAll(quoted[1:len(quoted)-1], `\"`, `"`) + "'"
}

// sortTexts sorts texts in place.
func sortTexts(texts []string) {
	sort.Strings(texts)
}
