//go:build conformance

package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// orderedObject is a JSON object that keeps its keys in the order the daemon sent them.
// Models read tool results as text, so the port must reproduce field order, and the
// goldens record it.
type orderedObject []objectMember

// objectMember is one key and value of an orderedObject.
type objectMember struct {
	key   string
	value any
}

// decodeOrdered parses one JSON document. Objects become orderedObject, arrays []any,
// numbers json.Number (so the exact digits stay), and the rest string, bool or nil.
func decodeOrdered(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("unexpected data after the JSON value")
	}
	return value, nil
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		return decodeObject(decoder)
	case json.Delim('['):
		return decodeArray(decoder)
	}
	return token, nil
}

func decodeObject(decoder *json.Decoder) (orderedObject, error) {
	object := orderedObject{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		value, err := decodeValue(decoder)
		if err != nil {
			return nil, err
		}
		object = append(object, objectMember{key: token.(string), value: value})
	}
	_, err := decoder.Token()
	return object, err
}

func decodeArray(decoder *json.Decoder) ([]any, error) {
	array := []any{}
	for decoder.More() {
		value, err := decodeValue(decoder)
		if err != nil {
			return nil, err
		}
		array = append(array, value)
	}
	_, err := decoder.Token()
	return array, err
}

// formatJSON writes a decoded value back as text. Pretty output uses two-space indents;
// compact output is one line with a space after each colon and comma.
func formatJSON(value any, pretty bool) string {
	var builder strings.Builder
	writeJSON(&builder, value, "", pretty)
	return builder.String()
}

func writeJSON(builder *strings.Builder, value any, indent string, pretty bool) {
	switch typed := value.(type) {
	case orderedObject:
		writeObject(builder, typed, indent, pretty)
	case []any:
		writeArray(builder, typed, indent, pretty)
	case json.Number:
		builder.WriteString(typed.String())
	default:
		builder.WriteString(quoteJSON(typed))
	}
}

func writeObject(builder *strings.Builder, object orderedObject, indent string, pretty bool) {
	if len(object) == 0 {
		builder.WriteString("{}")
		return
	}
	inner := indent + "  "
	builder.WriteString("{")
	for index, member := range object {
		writeSeparator(builder, index, inner, pretty)
		builder.WriteString(quoteJSON(member.key))
		builder.WriteString(": ")
		writeJSON(builder, member.value, inner, pretty)
	}
	writeClosing(builder, indent, pretty)
	builder.WriteString("}")
}

func writeArray(builder *strings.Builder, array []any, indent string, pretty bool) {
	if len(array) == 0 {
		builder.WriteString("[]")
		return
	}
	inner := indent + "  "
	builder.WriteString("[")
	for index, item := range array {
		writeSeparator(builder, index, inner, pretty)
		writeJSON(builder, item, inner, pretty)
	}
	writeClosing(builder, indent, pretty)
	builder.WriteString("]")
}

// writeSeparator starts one member or item: a comma after the first, then a new line or space.
func writeSeparator(builder *strings.Builder, index int, inner string, pretty bool) {
	if index > 0 {
		builder.WriteString(",")
		if !pretty {
			builder.WriteString(" ")
		}
	}
	if pretty {
		builder.WriteString("\n" + inner)
	}
}

func writeClosing(builder *strings.Builder, indent string, pretty bool) {
	if pretty {
		builder.WriteString("\n" + indent)
	}
}

// quoteJSON encodes a string, bool or null without HTML escaping, as the daemon does.
func quoteJSON(value any) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Sprintf("<unencodable %T>", value)
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

// encodeRequest turns a request map into one JSON line without HTML escaping.
func encodeRequest(request map[string]any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(request); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
