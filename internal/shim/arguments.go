package shim

import (
	"github.com/witseko/agentcy/internal/wire"
)

// argumentReader validates the arguments of one tool call the way the Python MCP SDK
// validates them against the tool function's signature: unknown arguments are ignored,
// errors are collected in signature order, and a string argument of a field whose type
// is not plain text is first parsed as JSON when that gives a null, a list, or an
// object (FuncMetadata.pre_parse_json in the mcp package).
type argumentReader struct {
	arguments wire.Value
	errors    []wire.FieldError
}

// newArgumentReader starts reading arguments, which may be any JSON value.
func newArgumentReader(arguments wire.Value) *argumentReader {
	if arguments.Kind != wire.Object {
		arguments = wire.NewObject()
	}
	return &argumentReader{arguments: arguments}
}

// value returns an argument and whether it is present. textField says whether the
// Python annotation is plain str, which skips the JSON pre-parse.
func (reader *argumentReader) value(name string, textField bool) (wire.Value, bool) {
	value, found := reader.arguments.Get(name)
	if !found || textField || value.Kind != wire.String {
		return value, found
	}
	parsed, err := wire.Parse([]byte(value.Text))
	if err == nil && (parsed.Kind == wire.Null || parsed.Kind == wire.Array || parsed.Kind == wire.Object) {
		return parsed, true
	}
	return value, true
}

// fail records a problem with one argument.
func (reader *argumentReader) fail(name string, problem *wire.FieldError) {
	if problem == nil {
		return
	}
	problem.Location = name
	reader.errors = append(reader.errors, *problem)
}

// missing records a required argument that is absent.
func (reader *argumentReader) missing(name string) {
	reader.errors = append(reader.errors, wire.FieldError{Location: name, Message: "Field required", Type: "missing"})
}

// err returns the collected errors titled like pydantic's argument model, or nil.
func (reader *argumentReader) err(tool string) error {
	if len(reader.errors) == 0 {
		return nil
	}
	return &wire.ValidationError{Title: tool + "Arguments", Fields: reader.errors}
}

// requiredText reads a required string argument.
func (reader *argumentReader) requiredText(name string) (string, bool) {
	value, found := reader.value(name, true)
	if !found {
		reader.missing(name)
		return "", false
	}
	text, problem := wire.CoerceString(value)
	reader.fail(name, problem)
	return text, problem == nil
}

// optionalText reads a string argument that may be absent or null.
func (reader *argumentReader) optionalText(name string) (*string, bool) {
	value, found := reader.value(name, false)
	if !found || value.Kind == wire.Null {
		return nil, false
	}
	text, problem := wire.CoerceString(value)
	reader.fail(name, problem)
	return &text, problem == nil
}

// key reads a required credential or ID argument (models.Key).
func (reader *argumentReader) key(name string) string {
	text, ok := reader.requiredText(name)
	if ok {
		reader.fail(name, wire.CheckKey(text))
	}
	return text
}

// plain reads a required argument annotated as plain str.
func (reader *argumentReader) plain(name string) string {
	text, _ := reader.requiredText(name)
	return text
}

// label reads a required participant name (models.Label) and strips it.
func (reader *argumentReader) label(name string) string {
	text, ok := reader.requiredText(name)
	if !ok {
		return text
	}
	stripped, problem := wire.NormalizeLabel(text)
	reader.fail(name, problem)
	return stripped
}

// optionalLabel reads a participant name that may be absent or null.
func (reader *argumentReader) optionalLabel(name string) *string {
	text, ok := reader.optionalText(name)
	if !ok {
		return text
	}
	stripped, problem := wire.NormalizeLabel(*text)
	reader.fail(name, problem)
	return &stripped
}

// body reads a required message text (models.Body) and strips it.
func (reader *argumentReader) body(name string) string {
	text, ok := reader.requiredText(name)
	if !ok {
		return text
	}
	stripped, problem := wire.NormalizeBody(text)
	reader.fail(name, problem)
	return stripped
}

// optionalBody reads a message text that may be absent or null.
func (reader *argumentReader) optionalBody(name string) *string {
	text, ok := reader.optionalText(name)
	if !ok {
		return text
	}
	stripped, problem := wire.NormalizeBody(*text)
	reader.fail(name, problem)
	return &stripped
}

// optionalModel reads a model name that may be absent or null.
func (reader *argumentReader) optionalModel(name string) *string {
	text, ok := reader.optionalText(name)
	if ok {
		reader.fail(name, wire.CheckModelName(*text))
	}
	return text
}

// conversation reads a required conversation ID.
func (reader *argumentReader) conversation(name string) string {
	text, ok := reader.requiredText(name)
	if ok {
		reader.fail(name, wire.CheckConversationID(text))
	}
	return text
}

// strictInt reads a strict integer with a default.
func (reader *argumentReader) strictInt(name string, fallback int64, limits wire.IntRange) int64 {
	value, found := reader.value(name, false)
	if !found {
		return fallback
	}
	number, problem := wire.CoerceStrictInt(value, limits)
	reader.fail(name, problem)
	return number
}

// requiredStrictInt reads a strict integer that must be present.
func (reader *argumentReader) requiredStrictInt(name string, limits wire.IntRange) int64 {
	if !reader.arguments.Has(name) {
		reader.missing(name)
		return 0
	}
	return reader.strictInt(name, 0, limits)
}

// optionalStrictInt reads a strict integer that may be absent or null.
func (reader *argumentReader) optionalStrictInt(name string, limits wire.IntRange) *int64 {
	value, found := reader.value(name, false)
	if !found || value.Kind == wire.Null {
		return nil
	}
	number, problem := wire.CoerceStrictInt(value, limits)
	reader.fail(name, problem)
	return &number
}

// cursor reads the lenient cursor integer (models.Cursor) with a default of zero.
func (reader *argumentReader) cursor(name string) int64 {
	value, found := reader.value(name, false)
	if !found {
		return 0
	}
	number, problem := wire.CoerceLaxInt(value, wire.CursorRange)
	reader.fail(name, problem)
	return number
}

// flag reads a lenient boolean with a default.
func (reader *argumentReader) flag(name string, fallback bool) bool {
	value, found := reader.value(name, false)
	if !found {
		return fallback
	}
	result, problem := wire.CoerceLaxBool(value)
	reader.fail(name, problem)
	return result
}

// optionalFlag reads a lenient boolean that may be absent or null.
func (reader *argumentReader) optionalFlag(name string) *bool {
	value, found := reader.value(name, false)
	if !found || value.Kind == wire.Null {
		return nil
	}
	result, problem := wire.CoerceLaxBool(value)
	reader.fail(name, problem)
	return &result
}

// literal reads one of a fixed set of texts with a default.
func (reader *argumentReader) literal(name, fallback string, allowed []string) string {
	value, found := reader.value(name, false)
	if !found {
		return fallback
	}
	text, problem := wire.CheckLiteral(value, allowed)
	reader.fail(name, problem)
	return text
}

// requiredLiteral reads one of a fixed set of texts that must be present.
func (reader *argumentReader) requiredLiteral(name string, allowed []string) string {
	if !reader.arguments.Has(name) {
		reader.missing(name)
		return ""
	}
	return reader.literal(name, "", allowed)
}

// optionalLiteral reads one of a fixed set of texts that may be absent or null.
func (reader *argumentReader) optionalLiteral(name string, allowed []string) *string {
	value, found := reader.value(name, false)
	if !found || value.Kind == wire.Null {
		return nil
	}
	text, problem := wire.CheckLiteral(value, allowed)
	reader.fail(name, problem)
	return &text
}

// slots reads the optional invitees list; absent or null means no invitees.
func (reader *argumentReader) slots(name string) []wire.Slot {
	value, found := reader.value(name, false)
	if !found || value.Kind == wire.Null {
		return []wire.Slot{}
	}
	slots, problems := wire.ParseSlots(value)
	for _, problem := range problems {
		problem.Location = joinName(name, problem.Location)
		reader.errors = append(reader.errors, problem)
	}
	return slots
}

// joinName joins an argument name and a nested location with a dot.
func joinName(name, location string) string {
	if location == "" {
		return name
	}
	return name + "." + location
}
