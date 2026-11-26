package wire

import "fmt"

// Argument binds one MCP tool argument to the request field that it fills. A tool lists
// its arguments in the order of the Python tool function's signature.
type Argument struct {
	// Name is the argument's name in the tool's input schema.
	Name string
	// Field is the JSON name of the request field.
	Field string
	// TypeOnly reads the value without the field's value rules: the signature annotates
	// the argument as plain str, and the rules apply only when the model is built.
	TypeOnly bool
	// NullIsAbsent treats a null argument like a missing one, for a signature default of
	// None that the tool replaces with the field's default.
	NullIsAbsent bool
}

// ReadArguments fills fields of request from the arguments of one MCP tool call, the way
// the Python MCP SDK validates them against the tool function's signature: in the order
// of bindings, each argument with its field's type and then its value rules, unknown
// arguments ignored. A string argument of a field that is not a required text is first
// parsed as JSON when that gives a null, a list, or an object (FuncMetadata.pre_parse_json
// in the mcp package). The error is titled like pydantic's argument model.
func ReadArguments(request Request, tool string, arguments Value, bindings []Argument) error {
	fields := request.schema().fields
	var errors fieldErrors
	for _, binding := range bindings {
		field := findField(fields, binding.Field)
		if field == nil {
			panic(fmt.Sprintf("wire: %s has no field %q for argument %q", request.Action(), binding.Field, binding.Name))
		}
		value, found := argumentValue(arguments, binding, field)
		if field.read(&errors, binding.Name, value, found) && !binding.TypeOnly {
			field.check(&errors, binding.Name)
		}
	}
	return errors.result(tool + "Arguments")
}

// argumentValue returns an argument after the SDK's JSON pre-parse, and whether it counts
// as present.
func argumentValue(arguments Value, binding Argument, field fieldSpec) (Value, bool) {
	value, found := arguments.Get(binding.Name)
	if _, requiredText := field.(*textField); found && !requiredText && value.Kind == String {
		parsed, err := Parse([]byte(value.Text))
		if err == nil && (parsed.Kind == Null || parsed.Kind == Array || parsed.Kind == Object) {
			value = parsed
		}
	}
	if binding.NullIsAbsent && value.Kind == Null {
		return value, false
	}
	return value, found
}

// ValidateRest checks the fields of request that ReadArguments did not check with the
// same bindings, then the rules that span several fields. Its errors carry the title of
// the request model, as when the Python shim builds the model after the SDK validated
// the arguments.
func ValidateRest(request Request, bindings []Argument) error {
	schema := request.schema()
	var errors fieldErrors
	for _, field := range schema.fields {
		if !checkedByArguments(bindings, field.name()) {
			field.check(&errors, field.name())
		}
	}
	if err := errors.result(schema.title); err != nil {
		return err
	}
	return schema.ruleError()
}

// checkedByArguments reports whether ReadArguments applied the value rules of the field.
func checkedByArguments(bindings []Argument, field string) bool {
	for _, binding := range bindings {
		if binding.Field == field && !binding.TypeOnly {
			return true
		}
	}
	return false
}
