package wire

import "errors"

// ErrInvalidRequest is returned for a request line that is not one known action.
var ErrInvalidRequest = errors.New("invalid member request")

// fieldReader takes typed fields out of a request object. It records each missing,
// mistyped, or unknown field, and remembers which fields it read.
type fieldReader struct {
	object Value
	errors fieldErrors
	read   map[string]bool
}

// newFieldReader starts reading the fields of object.
func newFieldReader(object Value) *fieldReader {
	return &fieldReader{object: object, read: map[string]bool{}}
}

// take returns a field and marks it as read. A JSON null counts as present.
func (reader *fieldReader) take(name string) (Value, bool) {
	reader.read[name] = true
	return reader.object.Get(name)
}

// missing records a required field that is absent.
func (reader *fieldReader) missing(name string) {
	reader.errors.add(name, problem("Field required", "missing"))
}

// text reads a required string.
func (reader *fieldReader) text(name string) string {
	value, found := reader.take(name)
	if !found {
		reader.missing(name)
		return ""
	}
	text, err := CoerceString(value)
	reader.errors.add(name, err)
	return text
}

// optionalText reads a string that may be absent or null.
func (reader *fieldReader) optionalText(name string) *string {
	value, found := reader.take(name)
	if !found || value.Kind == Null {
		return nil
	}
	text, err := CoerceString(value)
	reader.errors.add(name, err)
	return &text
}

// laxBool reads a lenient boolean with a default.
func (reader *fieldReader) laxBool(name string, fallback bool) bool {
	value, found := reader.take(name)
	if !found {
		return fallback
	}
	flag, err := CoerceLaxBool(value)
	reader.errors.add(name, err)
	return flag
}

// optionalLaxBool reads a lenient boolean that may be absent or null.
func (reader *fieldReader) optionalLaxBool(name string) *bool {
	value, found := reader.take(name)
	if !found || value.Kind == Null {
		return nil
	}
	flag, err := CoerceLaxBool(value)
	reader.errors.add(name, err)
	return &flag
}

// laxInt reads the lenient cursor integer with a default.
func (reader *fieldReader) laxInt(name string, fallback int64, limits IntRange) int64 {
	value, found := reader.take(name)
	if !found {
		return fallback
	}
	number, err := CoerceLaxInt(value, limits)
	reader.errors.add(name, err)
	return number
}

// strictInt reads a strict integer with a default.
func (reader *fieldReader) strictInt(name string, fallback int64, limits IntRange) int64 {
	value, found := reader.take(name)
	if !found {
		return fallback
	}
	number, err := CoerceStrictInt(value, limits)
	reader.errors.add(name, err)
	return number
}

// requiredStrictInt reads a strict integer that must be present.
func (reader *fieldReader) requiredStrictInt(name string, limits IntRange) int64 {
	if !reader.object.Has(name) {
		reader.take(name)
		reader.missing(name)
		return 0
	}
	return reader.strictInt(name, 0, limits)
}

// optionalStrictInt reads a strict integer that may be absent or null.
func (reader *fieldReader) optionalStrictInt(name string, limits IntRange) *int64 {
	value, found := reader.take(name)
	if !found || value.Kind == Null {
		return nil
	}
	number, err := CoerceStrictInt(value, limits)
	reader.errors.add(name, err)
	return &number
}

// literal reads a text from a fixed set, with a default.
func (reader *fieldReader) literal(name, fallback string, allowed []string) string {
	value, found := reader.take(name)
	if !found {
		return fallback
	}
	text, err := CheckLiteral(value, allowed)
	reader.errors.add(name, err)
	return text
}

// optionalLiteral reads a text from a fixed set that may be absent or null.
func (reader *fieldReader) optionalLiteral(name string, allowed []string) *string {
	value, found := reader.take(name)
	if !found || value.Kind == Null {
		return nil
	}
	text, err := CheckLiteral(value, allowed)
	reader.errors.add(name, err)
	return &text
}

// slots reads the invitees list: each item is a name or an invitation object.
func (reader *fieldReader) slots(name string) []Slot {
	value, found := reader.take(name)
	if !found {
		return []Slot{}
	}
	if value.Kind != Array {
		reader.errors.add(name, problem("Input should be a valid list", "list_type"))
		return nil
	}
	slots := make([]Slot, 0, len(value.Items))
	for index, item := range value.Items {
		slots = append(slots, reader.slot(name+"."+itoa(index), item))
	}
	return slots
}

// slot reads one invitee.
func (reader *fieldReader) slot(location string, item Value) Slot {
	switch item.Kind {
	case String:
		return Slot{Name: item.Text}
	case Object:
		nested := newFieldReader(item)
		invitation := &Invitation{
			Name:        nested.text("name"),
			Role:        nested.literal("role", "worker", InviteRoles),
			Model:       nested.optionalText("model"),
			TokenBudget: nested.optionalStrictInt("token_budget", BudgetRange),
		}
		nested.rejectUnknown()
		for _, nestedError := range nested.errors.fields {
			reader.errors.add(location, &FieldError{Location: nestedError.Location, Message: nestedError.Message, Type: nestedError.Type})
		}
		return Slot{Invitation: invitation}
	default:
		reader.errors.add(location, problem("Input should be a valid string or object", "union_type"))
		return Slot{}
	}
}

// rejectUnknown records every field of the object that no reader method read.
func (reader *fieldReader) rejectUnknown() {
	for _, key := range reader.object.Keys() {
		if !reader.read[key] {
			reader.errors.add(key, problem("Extra inputs are not permitted", "extra_forbidden"))
		}
	}
}

// DecodeRequest parses and validates one member request line the way the Python core
// does: strict JSON, a known action, no unknown fields, typed and lenient fields as in
// the pydantic models, and every Validate rule. The error says nothing about the input.
func DecodeRequest(line []byte) (Request, error) {
	object, err := Parse(line)
	if err != nil || object.Kind != Object {
		return nil, ErrInvalidRequest
	}
	actionValue, found := object.Get("action")
	if !found || actionValue.Kind != String {
		return nil, ErrInvalidRequest
	}
	decode, known := decoders[actionValue.Text]
	if !known {
		return nil, ErrInvalidRequest
	}
	reader := newFieldReader(object)
	reader.take("action")
	request := decode(reader)
	reader.rejectUnknown()
	if len(reader.errors.fields) > 0 {
		return nil, reader.errors.result(request.Action())
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return request, nil
}

// decoders maps each action to the function that reads its fields.
var decoders = map[string]func(*fieldReader) Request{
	"create": func(reader *fieldReader) Request {
		return &Create{
			Workspace:            reader.text("workspace"),
			Brief:                reader.text("brief"),
			Initiator:            reader.text("initiator"),
			InitiatorModel:       reader.optionalText("initiator_model"),
			InitiatorTokenBudget: reader.optionalStrictInt("initiator_token_budget", BudgetRange),
			Invitees:             reader.slots("invitees"),
			OpenInvite:           reader.laxBool("open_invite", false),
			Sealed:               reader.laxBool("sealed", false),
			DeadlineMinutes:      reader.optionalStrictInt("deadline_minutes", DeadlineRange),
		}
	},
	"join": func(reader *fieldReader) Request {
		return &Join{
			Workspace:   reader.text("workspace"),
			Code:        reader.text("code"),
			Name:        reader.optionalText("name"),
			Model:       reader.optionalText("model"),
			TokenBudget: reader.optionalStrictInt("token_budget", BudgetRange),
		}
	},
	"post": func(reader *fieldReader) Request {
		return &Post{
			SubmissionID: reader.text("submission_id"),
			Body:         reader.text("body"),
			Kind:         reader.literal("kind", "note", PostKinds),
			To:           reader.optionalText("to"),
			ReplyTo:      reader.optionalStrictInt("reply_to", MessageRange),
			DueMinutes:   reader.optionalStrictInt("due_minutes", DueRange),
			Workspace:    reader.text("workspace"),
			Token:        reader.text("token"),
		}
	},
	"read": func(reader *fieldReader) Request {
		return &Read{
			Workspace:   reader.text("workspace"),
			Token:       reader.text("token"),
			After:       reader.laxInt("after", 0, CursorRange),
			WaitSeconds: reader.strictInt("wait_seconds", 0, WaitRange),
		}
	},
	"close": func(reader *fieldReader) Request {
		return &Close{Workspace: reader.text("workspace"), Token: reader.text("token"), Summary: reader.optionalText("summary")}
	},
	"reopen": func(reader *fieldReader) Request {
		return &Reopen{Workspace: reader.text("workspace"), Token: reader.text("token"), Note: reader.optionalText("note")}
	},
	"status": func(reader *fieldReader) Request {
		return &Status{Workspace: reader.text("workspace"), Token: reader.text("token")}
	},
	"invite": func(reader *fieldReader) Request {
		return &Invite{
			Workspace:   reader.text("workspace"),
			Token:       reader.text("token"),
			Name:        reader.text("name"),
			Role:        reader.optionalLiteral("role", InviteRoles),
			Model:       reader.optionalText("model"),
			TokenBudget: reader.optionalStrictInt("token_budget", BudgetRange),
			Reinvite:    reader.laxBool("reinvite", false),
		}
	},
	"release": func(reader *fieldReader) Request {
		return &Release{Workspace: reader.text("workspace"), Token: reader.text("token")}
	},
	"launch": func(reader *fieldReader) Request {
		return &Launch{
			Workspace: reader.text("workspace"),
			Token:     reader.text("token"),
			Name:      reader.text("name"),
			Plugin:    reader.text("plugin"),
			Directory: reader.text("directory"),
			McpTask:   reader.laxBool("mcp_task", false),
		}
	},
	"retire": func(reader *fieldReader) Request {
		return &Retire{Workspace: reader.text("workspace"), Token: reader.text("token"), Name: reader.text("name")}
	},
	"set_role": func(reader *fieldReader) Request {
		return &SetRole{
			Workspace: reader.text("workspace"),
			Token:     reader.text("token"),
			Name:      reader.text("name"),
			Role:      requiredLiteral(reader, "role", AssignableRoles),
		}
	},
	"plugins": func(reader *fieldReader) Request {
		return &Plugins{Workspace: reader.text("workspace"), Token: reader.text("token")}
	},
	"open_invite": func(reader *fieldReader) Request {
		return &OpenInvite{Workspace: reader.text("workspace"), Token: reader.text("token"), Enabled: reader.laxBool("enabled", true)}
	},
	"report_usage": func(reader *fieldReader) Request {
		return &ReportUsage{
			Workspace:    reader.text("workspace"),
			Token:        reader.text("token"),
			InputTokens:  reader.requiredStrictInt("input_tokens", CountRange),
			OutputTokens: reader.requiredStrictInt("output_tokens", CountRange),
			Model:        reader.optionalText("model"),
		}
	},
	"list_closed": func(reader *fieldReader) Request {
		return &ListClosed{Workspace: reader.text("workspace"), Archived: reader.optionalLaxBool("archived")}
	},
	"read_closed": func(reader *fieldReader) Request {
		return &ReadClosed{
			Workspace:    reader.text("workspace"),
			Conversation: reader.text("conversation"),
			After:        reader.laxInt("after", 0, CursorRange),
		}
	},
	"mcp_task_status": func(reader *fieldReader) Request {
		return &McpTaskStatus{Workspace: reader.text("workspace"), TaskID: reader.text("task_id")}
	},
	"mcp_task_cancel": func(reader *fieldReader) Request {
		return &McpTaskCancel{Workspace: reader.text("workspace"), TaskID: reader.text("task_id")}
	},
}

// requiredLiteral reads a text from a fixed set that must be present.
func requiredLiteral(reader *fieldReader, name string, allowed []string) string {
	if !reader.object.Has(name) {
		reader.take(name)
		reader.missing(name)
		return ""
	}
	return reader.literal(name, "", allowed)
}
