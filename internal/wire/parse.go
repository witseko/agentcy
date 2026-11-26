package wire

import (
	"errors"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// maxDepth bounds the nesting of arrays and objects, so a hostile line cannot exhaust the
// stack.
const maxDepth = 256

// ErrInvalidJSON is the one error for text that is not a single strict JSON value.
var ErrInvalidJSON = errors.New("invalid JSON")

// Parse reads exactly one JSON value surrounded by optional whitespace.
//
// It follows RFC 8259 strictly, like the Python core's validator: no NaN or Infinity, no
// leading zeros or plus signs, no byte order mark, no raw control characters in strings,
// no lone UTF-16 surrogate escapes, and valid UTF-8 only. A repeated object key keeps its
// first position and its last value, as a Python dict does.
func Parse(data []byte) (Value, error) {
	parser := jsonParser{data: data}
	parser.skipSpace()
	value, err := parser.value(0)
	if err != nil {
		return Value{}, err
	}
	parser.skipSpace()
	if parser.position != len(parser.data) {
		return Value{}, ErrInvalidJSON
	}
	return value, nil
}

// jsonParser is a recursive descent parser over one byte slice.
type jsonParser struct {
	data     []byte
	position int
}

// skipSpace moves past the four JSON whitespace characters.
func (parser *jsonParser) skipSpace() {
	for parser.position < len(parser.data) {
		switch parser.data[parser.position] {
		case ' ', '\t', '\n', '\r':
			parser.position++
		default:
			return
		}
	}
}

// value parses the value that starts at the current position.
func (parser *jsonParser) value(depth int) (Value, error) {
	if depth > maxDepth || parser.position >= len(parser.data) {
		return Value{}, ErrInvalidJSON
	}
	switch character := parser.data[parser.position]; {
	case character == '{':
		return parser.object(depth)
	case character == '[':
		return parser.array(depth)
	case character == '"':
		text, err := parser.text()
		return TextValue(text), err
	case character == '-' || (character >= '0' && character <= '9'):
		return parser.number()
	default:
		return parser.literal()
	}
}

// literal parses true, false or null.
func (parser *jsonParser) literal() (Value, error) {
	for word, value := range map[string]Value{"true": BoolValue(true), "false": BoolValue(false), "null": NullValue()} {
		end := parser.position + len(word)
		if end <= len(parser.data) && string(parser.data[parser.position:end]) == word {
			parser.position = end
			return value, nil
		}
	}
	return Value{}, ErrInvalidJSON
}

// object parses an object whose opening brace is at the current position.
func (parser *jsonParser) object(depth int) (Value, error) {
	object := NewObject()
	parser.position++
	parser.skipSpace()
	if parser.peek() == '}' {
		parser.position++
		return object, nil
	}
	for {
		parser.skipSpace()
		if parser.peek() != '"' {
			return Value{}, ErrInvalidJSON
		}
		key, err := parser.text()
		if err != nil {
			return Value{}, err
		}
		parser.skipSpace()
		if parser.peek() != ':' {
			return Value{}, ErrInvalidJSON
		}
		parser.position++
		parser.skipSpace()
		member, err := parser.value(depth + 1)
		if err != nil {
			return Value{}, err
		}
		object.Set(key, member)
		parser.skipSpace()
		switch parser.peek() {
		case ',':
			parser.position++
		case '}':
			parser.position++
			return object, nil
		default:
			return Value{}, ErrInvalidJSON
		}
	}
}

// array parses an array whose opening bracket is at the current position.
func (parser *jsonParser) array(depth int) (Value, error) {
	items := []Value{}
	parser.position++
	parser.skipSpace()
	if parser.peek() == ']' {
		parser.position++
		return ListValue(items), nil
	}
	for {
		parser.skipSpace()
		item, err := parser.value(depth + 1)
		if err != nil {
			return Value{}, err
		}
		items = append(items, item)
		parser.skipSpace()
		switch parser.peek() {
		case ',':
			parser.position++
		case ']':
			parser.position++
			return ListValue(items), nil
		default:
			return Value{}, ErrInvalidJSON
		}
	}
}

// peek returns the byte at the current position, or 0 at the end.
func (parser *jsonParser) peek() byte {
	if parser.position >= len(parser.data) {
		return 0
	}
	return parser.data[parser.position]
}

// number parses a number and keeps its literal text.
func (parser *jsonParser) number() (Value, error) {
	start := parser.position
	if parser.peek() == '-' {
		parser.position++
	}
	switch {
	case parser.peek() == '0':
		parser.position++
	case isDigit(parser.peek()):
		parser.digits()
	default:
		return Value{}, ErrInvalidJSON
	}
	if parser.peek() == '.' {
		parser.position++
		if !parser.digits() {
			return Value{}, ErrInvalidJSON
		}
	}
	if parser.peek() == 'e' || parser.peek() == 'E' {
		parser.position++
		if parser.peek() == '+' || parser.peek() == '-' {
			parser.position++
		}
		if !parser.digits() {
			return Value{}, ErrInvalidJSON
		}
	}
	return Value{Kind: Number, Literal: string(parser.data[start:parser.position])}, nil
}

// digits moves past one or more ASCII digits and reports whether there was one.
func (parser *jsonParser) digits() bool {
	start := parser.position
	for isDigit(parser.peek()) {
		parser.position++
	}
	return parser.position > start
}

// isDigit reports whether character is an ASCII digit.
func isDigit(character byte) bool {
	return character >= '0' && character <= '9'
}

// text parses a string whose opening quote is at the current position.
func (parser *jsonParser) text() (string, error) {
	parser.position++
	var decoded []byte
	for {
		if parser.position >= len(parser.data) {
			return "", ErrInvalidJSON
		}
		character := parser.data[parser.position]
		switch {
		case character == '"':
			parser.position++
			return string(decoded), nil
		case character == '\\':
			var err error
			if decoded, err = parser.escape(decoded); err != nil {
				return "", err
			}
		case character < 0x20:
			return "", ErrInvalidJSON
		case character < utf8.RuneSelf:
			decoded = append(decoded, character)
			parser.position++
		default:
			character, size := utf8.DecodeRune(parser.data[parser.position:])
			if character == utf8.RuneError && size <= 1 {
				return "", ErrInvalidJSON
			}
			decoded = append(decoded, parser.data[parser.position:parser.position+size]...)
			parser.position += size
		}
	}
}

// escape decodes one backslash escape and appends it to decoded.
func (parser *jsonParser) escape(decoded []byte) ([]byte, error) {
	parser.position++
	shortForms := map[byte]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}
	if replacement, ok := shortForms[parser.peek()]; ok {
		parser.position++
		return append(decoded, replacement), nil
	}
	if parser.peek() != 'u' {
		return nil, ErrInvalidJSON
	}
	first, err := parser.hexUnit()
	if err != nil {
		return nil, err
	}
	if !utf16.IsSurrogate(first) {
		return utf8.AppendRune(decoded, first), nil
	}
	if first >= 0xdc00 || parser.peek() != '\\' {
		return nil, ErrInvalidJSON
	}
	parser.position++
	if parser.peek() != 'u' {
		return nil, ErrInvalidJSON
	}
	second, err := parser.hexUnit()
	if err != nil {
		return nil, err
	}
	combined := utf16.DecodeRune(first, second)
	if combined == utf8.RuneError {
		return nil, ErrInvalidJSON
	}
	return utf8.AppendRune(decoded, combined), nil
}

// hexUnit reads the "uXXXX" part of an escape, with the position on the "u".
func (parser *jsonParser) hexUnit() (rune, error) {
	start := parser.position + 1
	end := start + 4
	if end > len(parser.data) {
		return 0, ErrInvalidJSON
	}
	unit, err := strconv.ParseUint(string(parser.data[start:end]), 16, 32)
	if err != nil {
		return 0, ErrInvalidJSON
	}
	parser.position = end
	return rune(unit), nil
}
