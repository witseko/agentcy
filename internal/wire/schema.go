package wire

import (
	"strconv"
)

// requestSchema is the one description of a request. Decoding, validation, encoding and
// the shim's MCP argument reading all follow it, so the four cannot drift apart.
type requestSchema struct {
	// title names the Python model in validation errors, such as "Create".
	title string
	// fields lists every field in the order of the Python model's dump.
	fields []fieldSpec
	// rule checks the rules that span several fields after every field is valid. It
	// returns the broken rule's message, or "". It is nil when the request has none.
	rule func() string
}

// fieldSpec is one field of a request schema. Each implementation stores its value
// through a pointer into the request struct.
type fieldSpec interface {
	// name returns the field's JSON name.
	name() string
	// read stores the field's JSON value, or its default when found is false. It records
	// a missing or mistyped value at location and reports whether the value was read.
	read(errors *fieldErrors, location string, value Value, found bool) bool
	// check applies the field's value rules, normalises the stored value in place, and
	// records each broken rule at location.
	check(errors *fieldErrors, location string)
	// encode returns the stored value as JSON.
	encode() Value
}

// textRule checks a stored text and may normalise it in place.
type textRule func(errors *fieldErrors, location string, text *string)

// missingField records a required field that is absent.
func missingField(errors *fieldErrors, location string) {
	errors.add(location, problem("Field required", "missing"))
}

// readObject reads every field of specs from object, then records each member of object
// that no field names.
func readObject(errors *fieldErrors, location string, object Value, specs []fieldSpec) {
	for _, spec := range specs {
		value, found := object.Get(spec.name())
		spec.read(errors, joinLocation(location, spec.name()), value, found)
	}
	for _, key := range object.Keys() {
		if findField(specs, key) == nil {
			errors.add(joinLocation(location, key), problem("Extra inputs are not permitted", "extra_forbidden"))
		}
	}
}

// checkFields applies the value rules of every field of specs.
func checkFields(errors *fieldErrors, location string, specs []fieldSpec) {
	for _, spec := range specs {
		spec.check(errors, joinLocation(location, spec.name()))
	}
}

// encodeFields returns the fields of specs as an ordered JSON object.
func encodeFields(specs []fieldSpec) Value {
	object := NewObject()
	for _, spec := range specs {
		if flag, ok := spec.(*boolField); ok && flag.omitFalse && !*flag.target {
			continue
		}
		object.Set(spec.name(), spec.encode())
	}
	return object
}

// findField returns the field of specs with the given name, or nil.
func findField(specs []fieldSpec, name string) fieldSpec {
	for _, spec := range specs {
		if spec.name() == name {
			return spec
		}
	}
	return nil
}

// ruleError returns the error of the first broken rule that spans several fields.
func (schema requestSchema) ruleError() error {
	if schema.rule == nil {
		return nil
	}
	if message := schema.rule(); message != "" {
		return modelError(schema.title, message)
	}
	return nil
}

// Validate checks every field of a request, strips names and texts in place, and applies
// the rules that span several fields. It returns a *ValidationError titled like the
// Python model.
func Validate(request Request) error {
	return ValidateRest(request, nil)
}

// Encode returns a request as an ordered JSON object with every field, in the field order
// of the Python model's dump.
func Encode(request Request) Value {
	return encodeFields(request.schema().fields)
}

// decodeFields reads the fields of a parsed request object into request, then validates
// it. Type errors are titled with the action and value errors with the model, as in the
// Python core, which first parses the request and then builds the model.
func decodeFields(object Value, request Request) error {
	schema := request.schema()
	var errors fieldErrors
	readObject(&errors, "", object, schema.fields)
	if err := errors.result(request.Action()); err != nil {
		return err
	}
	return Validate(request)
}

// textField is a required string.
type textField struct {
	key    string
	target *string
	rule   textRule
}

func (field *textField) name() string { return field.key }

func (field *textField) read(errors *fieldErrors, location string, value Value, found bool) bool {
	if !found {
		missingField(errors, location)
		return false
	}
	text, err := CoerceString(value)
	*field.target = text
	errors.add(location, err)
	return err == nil
}

func (field *textField) check(errors *fieldErrors, location string) {
	field.rule(errors, location, field.target)
}

func (field *textField) encode() Value { return TextValue(*field.target) }

// optionalTextField is a string that may be null. An absent field is null, unless
// required is set: then it must be present, but may still be null.
type optionalTextField struct {
	key      string
	target   **string
	rule     textRule
	required bool
}

func (field *optionalTextField) name() string { return field.key }

func (field *optionalTextField) read(errors *fieldErrors, location string, value Value, found bool) bool {
	if !found && field.required {
		missingField(errors, location)
		return false
	}
	if !found || value.Kind == Null {
		*field.target = nil
		return true
	}
	text, err := CoerceString(value)
	*field.target = &text
	errors.add(location, err)
	return err == nil
}

func (field *optionalTextField) check(errors *fieldErrors, location string) {
	if *field.target != nil {
		field.rule(errors, location, *field.target)
	}
}

func (field *optionalTextField) encode() Value {
	if *field.target == nil {
		return NullValue()
	}
	return TextValue(**field.target)
}

// literalField is one text of a fixed set, with a default unless required is set.
type literalField struct {
	key      string
	target   *string
	allowed  []string
	fallback string
	required bool
}

func (field *literalField) name() string { return field.key }

func (field *literalField) read(errors *fieldErrors, location string, value Value, found bool) bool {
	if !found && field.required {
		missingField(errors, location)
		return false
	}
	if !found {
		*field.target = field.fallback
		return true
	}
	text, err := CheckLiteral(value, field.allowed)
	*field.target = text
	errors.add(location, err)
	return err == nil
}

func (field *literalField) check(errors *fieldErrors, location string) {
	_, err := CheckLiteral(TextValue(*field.target), field.allowed)
	errors.add(location, err)
}

func (field *literalField) encode() Value { return TextValue(*field.target) }

// optionalLiteralField is one text of a fixed set that may be absent or null.
type optionalLiteralField struct {
	key     string
	target  **string
	allowed []string
}

func (field *optionalLiteralField) name() string { return field.key }

func (field *optionalLiteralField) read(errors *fieldErrors, location string, value Value, found bool) bool {
	if !found || value.Kind == Null {
		*field.target = nil
		return true
	}
	text, err := CheckLiteral(value, field.allowed)
	*field.target = &text
	errors.add(location, err)
	return err == nil
}

func (field *optionalLiteralField) check(errors *fieldErrors, location string) {
	if *field.target != nil {
		_, err := CheckLiteral(TextValue(**field.target), field.allowed)
		errors.add(location, err)
	}
}

func (field *optionalLiteralField) encode() Value {
	if *field.target == nil {
		return NullValue()
	}
	return TextValue(**field.target)
}

// boolField is a lenient boolean with a default, unless required is set.
type boolField struct {
	key       string
	target    *bool
	fallback  bool
	required  bool
	omitFalse bool
}

func (field *boolField) name() string { return field.key }

func (field *boolField) read(errors *fieldErrors, location string, value Value, found bool) bool {
	if !found && field.required {
		missingField(errors, location)
		return false
	}
	if !found {
		*field.target = field.fallback
		return true
	}
	flag, err := CoerceLaxBool(value)
	*field.target = flag
	errors.add(location, err)
	return err == nil
}

func (field *boolField) check(*fieldErrors, string) {}

func (field *boolField) encode() Value { return BoolValue(*field.target) }

// optionalBoolField is a lenient boolean that may be absent or null.
type optionalBoolField struct {
	key    string
	target **bool
}

func (field *optionalBoolField) name() string { return field.key }

func (field *optionalBoolField) read(errors *fieldErrors, location string, value Value, found bool) bool {
	if !found || value.Kind == Null {
		*field.target = nil
		return true
	}
	flag, err := CoerceLaxBool(value)
	*field.target = &flag
	errors.add(location, err)
	return err == nil
}

func (field *optionalBoolField) check(*fieldErrors, string) {}

func (field *optionalBoolField) encode() Value {
	if *field.target == nil {
		return NullValue()
	}
	return BoolValue(**field.target)
}

// intField is an integer in limits with a default of zero, unless required is set. A
// strict field accepts only integer literals; a lax one (the cursor) also accepts
// integral floats, booleans and decimal strings.
type intField struct {
	key      string
	target   *int64
	limits   IntRange
	lax      bool
	required bool
}

func (field *intField) name() string { return field.key }

func (field *intField) read(errors *fieldErrors, location string, value Value, found bool) bool {
	if !found && field.required {
		missingField(errors, location)
		return false
	}
	if !found {
		*field.target = 0
		return true
	}
	coerce := CoerceStrictInt
	if field.lax {
		coerce = CoerceLaxInt
	}
	number, err := coerce(value, field.limits)
	*field.target = number
	errors.add(location, err)
	return err == nil
}

func (field *intField) check(errors *fieldErrors, location string) {
	errors.add(location, checkInt64Range(*field.target, field.limits))
}

func (field *intField) encode() Value { return IntValue(*field.target) }

// optionalIntField is a strict integer in limits that may be absent or null.
type optionalIntField struct {
	key    string
	target **int64
	limits IntRange
}

func (field *optionalIntField) name() string { return field.key }

func (field *optionalIntField) read(errors *fieldErrors, location string, value Value, found bool) bool {
	if !found || value.Kind == Null {
		*field.target = nil
		return true
	}
	number, err := CoerceStrictInt(value, field.limits)
	*field.target = &number
	errors.add(location, err)
	return err == nil
}

func (field *optionalIntField) check(errors *fieldErrors, location string) {
	if *field.target != nil {
		errors.add(location, checkInt64Range(**field.target, field.limits))
	}
}

func (field *optionalIntField) encode() Value {
	if *field.target == nil {
		return NullValue()
	}
	return IntValue(**field.target)
}

// slotsField is the invitees list of a create request: each item is a name or an
// invitation object. An absent list is empty.
type slotsField struct {
	key    string
	target *[]Slot
}

func (field *slotsField) name() string { return field.key }

func (field *slotsField) read(errors *fieldErrors, location string, value Value, found bool) bool {
	if !found {
		*field.target = []Slot{}
		return true
	}
	if value.Kind != Array {
		*field.target = nil
		errors.add(location, problem("Input should be a valid list", "list_type"))
		return false
	}
	before := len(errors.fields)
	slots := make([]Slot, 0, len(value.Items))
	for index, item := range value.Items {
		slots = append(slots, readSlot(errors, location+"."+strconv.Itoa(index), item))
	}
	*field.target = slots
	return len(errors.fields) == before
}

// readSlot reads one invitee: a plain name or an invitation without unknown fields.
func readSlot(errors *fieldErrors, location string, item Value) Slot {
	switch item.Kind {
	case String:
		return Slot{Name: item.Text}
	case Object:
		invitation := &Invitation{}
		readObject(errors, location, item, invitation.fields())
		return Slot{Invitation: invitation}
	default:
		errors.add(location, problem("Input should be a valid string or object", "union_type"))
		return Slot{}
	}
}

func (field *slotsField) check(errors *fieldErrors, location string) {
	slots := *field.target
	if len(slots) > MaxSlots {
		errors.add(location, problem("List should have at most 16 items after validation, not more", "too_long"))
		return
	}
	for index := range slots {
		itemLocation := location + "." + strconv.Itoa(index)
		if slots[index].Invitation == nil {
			checkLabel(errors, itemLocation, &slots[index].Name)
			continue
		}
		checkFields(errors, itemLocation, slots[index].Invitation.fields())
	}
}

func (field *slotsField) encode() Value {
	items := make([]Value, 0, len(*field.target))
	for _, slot := range *field.target {
		if slot.Invitation == nil {
			items = append(items, TextValue(slot.Name))
		} else {
			items = append(items, encodeFields(slot.Invitation.fields()))
		}
	}
	return ListValue(items)
}

// fields describes an invitation object of the invitees list.
func (invitation *Invitation) fields() []fieldSpec {
	return []fieldSpec{
		&textField{"name", &invitation.Name, checkLabel},
		&literalField{key: "role", target: &invitation.Role, allowed: InviteRoles, fallback: "worker"},
		&optionalTextField{key: "model", target: &invitation.Model, rule: checkModel},
		&optionalIntField{"token_budget", &invitation.TokenBudget, BudgetRange},
	}
}

// actionField is the "action" of a request. The decoder has already matched it, so
// reading it always succeeds.
type actionField struct {
	action string
}

func (field actionField) name() string { return "action" }

func (field actionField) read(*fieldErrors, string, Value, bool) bool { return true }

func (field actionField) check(*fieldErrors, string) {}

func (field actionField) encode() Value { return TextValue(field.action) }

// checkKey checks a credential or ID.
func checkKey(errors *fieldErrors, location string, text *string) {
	errors.add(location, CheckKey(*text))
}

// checkLabel strips and checks a participant name.
func checkLabel(errors *fieldErrors, location string, text *string) {
	normalize(errors, location, text, NormalizeLabel)
}

// checkBody strips and checks a message text.
func checkBody(errors *fieldErrors, location string, text *string) {
	normalize(errors, location, text, NormalizeBody)
}

// normalize replaces text with its normal form, or records why it has none.
func normalize(errors *fieldErrors, location string, text *string, normalizer func(string) (string, *FieldError)) {
	normalized, err := normalizer(*text)
	errors.add(location, err)
	if err == nil {
		*text = normalized
	}
}

// checkWorkspace checks a workspace key.
func checkWorkspace(errors *fieldErrors, location string, text *string) {
	errors.add(location, CheckPattern(*text, workspacePattern, workspacePatternText))
}

// checkModel checks a model name such as "claude-opus-4".
func checkModel(errors *fieldErrors, location string, text *string) {
	errors.add(location, CheckPattern(*text, modelNamePattern, modelNamePatternText))
}

// checkPlugin checks the name of a configured plugin.
func checkPlugin(errors *fieldErrors, location string, text *string) {
	errors.add(location, CheckPattern(*text, pluginPattern, pluginPatternText))
}

// checkConversation checks a 24-digit lowercase hex conversation ID.
func checkConversation(errors *fieldErrors, location string, text *string) {
	errors.add(location, CheckPattern(*text, conversationPattern, conversationPatternText))
}

// checkMcpTask checks an MCP task ID.
func checkMcpTask(errors *fieldErrors, location string, text *string) {
	errors.add(location, CheckPattern(*text, mcpTaskPattern, mcpTaskPatternText))
}

// checkLaunchDirectory checks the checkout path that a launch hands to the plugin.
func checkLaunchDirectory(errors *fieldErrors, location string, text *string) {
	errors.add(location, CheckLength(*text, 1, MaxDirectoryLength))
}
