package wire

import (
	"fmt"
	"strings"
)

// pydanticErrorsURL is the help link pydantic appends to each error line. The Python shim
// shows it in tool errors, so the Go shim keeps it for the calling model.
const pydanticErrorsURL = "https://errors.pydantic.dev/2.14/v/"

// FieldError is one rejected field: where it is, what is wrong, and pydantic's error type.
// It never holds the rejected value, which may be a credential.
type FieldError struct {
	// Location is the field path, such as "token" or "invitees.0.name". It is empty for a
	// rule that spans several fields.
	Location string
	Message  string
	Type     string
}

// ValidationError lists every rejected field of one request.
type ValidationError struct {
	// Title names what was validated, such as "Create" or "read_messagesArguments".
	Title  string
	Fields []FieldError
}

// Error renders the errors in pydantic's layout with hidden input values.
func (validation *ValidationError) Error() string {
	noun := "errors"
	if len(validation.Fields) == 1 {
		noun = "error"
	}
	lines := []string{fmt.Sprintf("%d validation %s for %s", len(validation.Fields), noun, validation.Title)}
	for _, field := range validation.Fields {
		if field.Location != "" {
			lines = append(lines, field.Location)
		}
		lines = append(lines,
			fmt.Sprintf("  %s [type=%s]", field.Message, field.Type),
			"    For further information visit "+pydanticErrorsURL+field.Type)
	}
	return strings.Join(lines, "\n")
}

// fieldErrors collects the errors of one validation pass in field order.
type fieldErrors struct {
	fields []FieldError
}

// add records an error for the field at location, unless problem is nil.
func (errors *fieldErrors) add(location string, problem *FieldError) {
	if problem == nil {
		return
	}
	problem.Location = joinLocation(location, problem.Location)
	errors.fields = append(errors.fields, *problem)
}

// result returns a ValidationError with the given title, or nil when nothing failed.
func (errors *fieldErrors) result(title string) error {
	if len(errors.fields) == 0 {
		return nil
	}
	return &ValidationError{Title: title, Fields: errors.fields}
}

// joinLocation joins a field name and a nested location with a dot.
func joinLocation(outer, inner string) string {
	switch {
	case outer == "":
		return inner
	case inner == "":
		return outer
	default:
		return outer + "." + inner
	}
}

// modelError returns the error of a rule that spans several fields.
func modelError(title, message string) error {
	return &ValidationError{Title: title, Fields: []FieldError{{Message: "Value error, " + message, Type: "value_error"}}}
}
