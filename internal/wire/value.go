package wire

import (
	"strconv"
	"strings"
)

// Kind tells which JSON type a Value holds.
type Kind int

// The JSON types.
const (
	Null Kind = iota
	Bool
	Number
	String
	Array
	Object
)

// Value is one parsed JSON value. Objects keep their members in the order they arrived,
// like a Python dict, so a result passes through the shim with its fields in the order
// the core wrote them. Numbers keep their literal text.
type Value struct {
	Kind    Kind
	Boolean bool
	// Literal is the number exactly as written, for Kind Number.
	Literal string
	Text    string
	Items   []Value
	Members *Members
}

// Members is the ordered member list of a JSON object. Setting an existing key replaces
// its value in place; a new key goes to the end, as in a Python dict.
type Members struct {
	keys   []string
	values []Value
}

// NewObject returns an empty object value.
func NewObject() Value {
	return Value{Kind: Object, Members: &Members{}}
}

// NullValue returns JSON null.
func NullValue() Value { return Value{Kind: Null} }

// BoolValue returns a JSON boolean.
func BoolValue(value bool) Value { return Value{Kind: Bool, Boolean: value} }

// TextValue returns a JSON string.
func TextValue(text string) Value { return Value{Kind: String, Text: text} }

// IntValue returns a JSON integer.
func IntValue(number int64) Value {
	return Value{Kind: Number, Literal: strconv.FormatInt(number, 10)}
}

// ListValue returns a JSON array holding items.
func ListValue(items []Value) Value {
	if items == nil {
		items = []Value{}
	}
	return Value{Kind: Array, Items: items}
}

// TextList returns a JSON array of strings.
func TextList(texts []string) Value {
	items := make([]Value, 0, len(texts))
	for _, text := range texts {
		items = append(items, TextValue(text))
	}
	return ListValue(items)
}

// Get returns the value of key and whether the object has it. It is safe on any value.
func (value Value) Get(key string) (Value, bool) {
	if value.Kind != Object {
		return Value{}, false
	}
	for index, candidate := range value.Members.keys {
		if candidate == key {
			return value.Members.values[index], true
		}
	}
	return Value{}, false
}

// Has reports whether an object holds key.
func (value Value) Has(key string) bool {
	_, found := value.Get(key)
	return found
}

// Set gives key a value: in place when the key exists, else as the last member. Like
// Delete, it does nothing on a value that is not an object, so a result of an unexpected
// shape cannot crash the caller.
func (value Value) Set(key string, member Value) {
	if value.Kind != Object {
		return
	}
	for index, candidate := range value.Members.keys {
		if candidate == key {
			value.Members.values[index] = member
			return
		}
	}
	value.Members.keys = append(value.Members.keys, key)
	value.Members.values = append(value.Members.values, member)
}

// Delete removes key from an object and returns its value and whether it was there.
func (value Value) Delete(key string) (Value, bool) {
	if value.Kind != Object {
		return Value{}, false
	}
	for index, candidate := range value.Members.keys {
		if candidate == key {
			removed := value.Members.values[index]
			value.Members.keys = append(value.Members.keys[:index], value.Members.keys[index+1:]...)
			value.Members.values = append(value.Members.values[:index], value.Members.values[index+1:]...)
			return removed, true
		}
	}
	return Value{}, false
}

// Keys returns the member names of an object in order.
func (value Value) Keys() []string {
	if value.Kind != Object {
		return nil
	}
	return append([]string(nil), value.Members.keys...)
}

// Len returns the number of items of an array or members of an object.
func (value Value) Len() int {
	switch value.Kind {
	case Array:
		return len(value.Items)
	case Object:
		return len(value.Members.keys)
	default:
		return 0
	}
}

// Truthy applies Python truthiness: null, false, zero, "" and empty containers are false.
func (value Value) Truthy() bool {
	switch value.Kind {
	case Bool:
		return value.Boolean
	case Number:
		number, err := strconv.ParseFloat(value.Literal, 64)
		return err != nil || number != 0
	case String:
		return value.Text != ""
	case Array, Object:
		return value.Len() > 0
	default:
		return false
	}
}

// OnlyKnownFields reports whether value is an object whose keys are all in known.
func (value Value) OnlyKnownFields(known ...string) bool {
	if value.Kind != Object {
		return false
	}
	for _, key := range value.Keys() {
		found := false
		for _, candidate := range known {
			if candidate == key {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// StringOf returns the text of a string value, or "" for any other kind.
func (value Value) StringOf() string {
	if value.Kind != String {
		return ""
	}
	return value.Text
}

// IntOf returns an integer value and whether the value is an integer that fits int64.
func (value Value) IntOf() (int64, bool) {
	if value.Kind != Number {
		return 0, false
	}
	number, err := strconv.ParseInt(value.Literal, 10, 64)
	return number, err == nil
}

// Copy returns a deep copy, so that changing the copy leaves the original alone.
func (value Value) Copy() Value {
	switch value.Kind {
	case Array:
		items := make([]Value, len(value.Items))
		for index, item := range value.Items {
			items[index] = item.Copy()
		}
		return Value{Kind: Array, Items: items}
	case Object:
		copied := NewObject()
		for index, key := range value.Members.keys {
			copied.Set(key, value.Members.values[index].Copy())
		}
		return copied
	default:
		return value
	}
}

// Compact encodes the value as one line of JSON with no spaces.
func (value Value) Compact() string {
	var builder strings.Builder
	writeValue(&builder, value, "", "")
	return builder.String()
}

// Indented encodes the value with two-space indentation, byte for byte like the Python
// shim's tool result text (pydantic_core.to_json with indent=2).
func (value Value) Indented() string {
	var builder strings.Builder
	writeValue(&builder, value, "\n", "  ")
	return builder.String()
}

// writeValue encodes one value. newline and indent are empty for compact output.
func writeValue(builder *strings.Builder, value Value, newline, indent string) {
	switch value.Kind {
	case Null:
		builder.WriteString("null")
	case Bool:
		builder.WriteString(strconv.FormatBool(value.Boolean))
	case Number:
		builder.WriteString(value.Literal)
	case String:
		writeString(builder, value.Text)
	case Array:
		writeArray(builder, value, newline, indent)
	case Object:
		writeObject(builder, value, newline, indent)
	}
}

// writeArray encodes an array; an empty array is always "[]".
func writeArray(builder *strings.Builder, value Value, newline, indent string) {
	if len(value.Items) == 0 {
		builder.WriteString("[]")
		return
	}
	inner := newline + indent
	builder.WriteByte('[')
	for index, item := range value.Items {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(inner)
		writeValue(builder, item, inner, indent)
	}
	builder.WriteString(newline)
	builder.WriteByte(']')
}

// writeObject encodes an object; an empty object is always "{}".
func writeObject(builder *strings.Builder, value Value, newline, indent string) {
	if value.Len() == 0 {
		builder.WriteString("{}")
		return
	}
	inner := newline + indent
	separator := ":"
	if newline != "" {
		separator = ": "
	}
	builder.WriteByte('{')
	for index, key := range value.Members.keys {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(inner)
		writeString(builder, key)
		builder.WriteString(separator)
		writeValue(builder, value.Members.values[index], inner, indent)
	}
	builder.WriteString(newline)
	builder.WriteByte('}')
}

// writeString quotes text like serde_json: non-ASCII stays as is, HTML characters are not
// escaped, and control characters use the short escapes or a lowercase \u00XX form.
func writeString(builder *strings.Builder, text string) {
	const hexDigits = "0123456789abcdef"
	builder.WriteByte('"')
	for _, character := range text {
		switch character {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if character < 0x20 {
				builder.WriteString(`\u00`)
				builder.WriteByte(hexDigits[character>>4])
				builder.WriteByte(hexDigits[character&0xf])
				continue
			}
			builder.WriteRune(character)
		}
	}
	builder.WriteByte('"')
}
