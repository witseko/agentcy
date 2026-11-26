package wire

import (
	"errors"
	"strconv"
	"strings"
)

// OperatorKeyRejected is the one answer to a missing, malformed, or wrong operator key.
// The terminal UI matches it and then reads operator.key again.
const OperatorKeyRejected = "Operator key rejected"

// ErrOperatorKeyRejected is returned for an operator request whose key is missing, is not
// a string, or is not 1 to 128 code points long.
var ErrOperatorKeyRejected = errors.New(OperatorKeyRejected)

// Incoming is one validated request line for the core. Exactly one field is set.
type Incoming struct {
	Member   Request
	Operator OperatorRequest
}

// DecodeIncoming parses and validates one request line for the core, member or operator,
// the way the Python core does (REQUEST and rejects_operator_key in src/agentcy/mcp/core.py).
//
// The error decides the answer, in this order:
//
//  1. A line that is not one JSON object with a known action: ErrInvalidRequest.
//  2. An operator request whose key is missing, not a string, empty, or longer than 128
//     code points: ErrOperatorKeyRejected, even when other fields are also bad.
//  3. Any other bad field or broken rule: ErrInvalidRequest or a *ValidationError, which
//     both mean "Invalid bridge arguments", even when the key is wrong.
//
// A well-formed key is not compared here: the caller compares it in constant time and
// answers OperatorKeyRejected when it differs.
func DecodeIncoming(line []byte) (Incoming, error) {
	object, err := Parse(line)
	if err != nil || object.Kind != Object {
		return Incoming{}, ErrInvalidRequest
	}
	actionValue, found := object.Get("action")
	if !found || actionValue.Kind != String {
		return Incoming{}, ErrInvalidRequest
	}
	if decode, known := operatorDecoders[actionValue.Text]; known {
		request, err := decodeOperator(object, decode)
		return Incoming{Operator: request}, err
	}
	if decode, known := decoders[actionValue.Text]; known {
		request, err := decodeMember(object, decode)
		return Incoming{Member: request}, err
	}
	return Incoming{}, ErrInvalidRequest
}

// decodeOperator checks the key's shape first, then reads and validates every field.
// Pydantic reports all field errors of a request at once, and the core answers
// OperatorKeyRejected whenever one of them is at "key", so the key check must not depend
// on the other fields.
func decodeOperator(object Value, decode func(*fieldReader) OperatorRequest) (OperatorRequest, error) {
	key, found := object.Get("key")
	if !found || key.Kind != String || CheckKey(key.Text) != nil {
		return nil, ErrOperatorKeyRejected
	}
	reader := newFieldReader(object)
	reader.take("action")
	reader.operatorVersion()
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

// operatorVersion reads the required "version", which must equal 1.
func (reader *fieldReader) operatorVersion() {
	value, found := reader.take("version")
	if !found {
		reader.missing("version")
		return
	}
	reader.errors.add("version", checkOperatorVersion(value))
}

// checkOperatorVersion accepts what pydantic's Literal[1] accepts in lax mode: every JSON
// value that Python finds equal to 1, which includes true, 1.0, 1e0, and floats that
// round to 1.0. The string "1" is not equal to 1.
func checkOperatorVersion(value Value) *FieldError {
	switch value.Kind {
	case Bool:
		if value.Boolean {
			return nil
		}
	case Number:
		if value.Literal == "1" {
			return nil
		}
		if strings.ContainsAny(value.Literal, ".eE") {
			number, err := strconv.ParseFloat(value.Literal, 64)
			if err == nil && number == OperatorProtocolVersion {
				return nil
			}
		}
	}
	return problem("Input should be 1", "literal_error")
}

// requiredLaxBool reads a lenient boolean that must be present.
func requiredLaxBool(reader *fieldReader, name string) bool {
	if !reader.object.Has(name) {
		reader.take(name)
		reader.missing(name)
		return false
	}
	return reader.laxBool(name, false)
}

// requiredOptionalText reads a string that must be present but may be null.
func requiredOptionalText(reader *fieldReader, name string) *string {
	if !reader.object.Has(name) {
		reader.take(name)
		reader.missing(name)
		return nil
	}
	return reader.optionalText(name)
}

// checkoutHeader reads the key and directory of a request that names a checkout.
func checkoutHeader(reader *fieldReader) CheckoutHeader {
	return CheckoutHeader{OperatorHeader: OperatorHeader{Key: reader.text("key")}, Directory: reader.text("directory")}
}

// conversationHeader reads the key, directory, and conversation of a request that names
// one conversation.
func conversationHeader(reader *fieldReader) ConversationHeader {
	return ConversationHeader{CheckoutHeader: checkoutHeader(reader), Conversation: reader.text("conversation")}
}

// operatorDecoders maps each operator action to the function that reads its fields.
var operatorDecoders = map[string]func(*fieldReader) OperatorRequest{
	"operator_list": func(reader *fieldReader) OperatorRequest {
		return &OperatorList{CheckoutHeader: checkoutHeader(reader), Archived: reader.laxBool("archived", false)}
	},
	"operator_show": func(reader *fieldReader) OperatorRequest {
		return &OperatorShow{
			ConversationHeader: conversationHeader(reader),
			After:              reader.laxInt("after", 0, CursorRange),
			Before:             reader.optionalStrictInt("before", MessageRange),
			Tail:               reader.laxBool("tail", false),
		}
	},
	"operator_tasks": func(reader *fieldReader) OperatorRequest {
		return &OperatorTasks{ConversationHeader: conversationHeader(reader)}
	},
	"operator_create": func(reader *fieldReader) OperatorRequest {
		return &OperatorCreate{
			CheckoutHeader:  checkoutHeader(reader),
			Brief:           reader.text("brief"),
			Lead:            reader.text("lead"),
			Invitees:        reader.slots("invitees"),
			OpenInvite:      reader.laxBool("open_invite", false),
			Sealed:          reader.laxBool("sealed", false),
			DeadlineMinutes: reader.optionalStrictInt("deadline_minutes", DeadlineRange),
		}
	},
	"operator_invite": func(reader *fieldReader) OperatorRequest {
		return &OperatorInvite{
			ConversationHeader: conversationHeader(reader),
			Name:               reader.text("name"),
			Role:               reader.optionalLiteral("role", InviteRoles),
			Reinvite:           reader.laxBool("reinvite", false),
		}
	},
	"operator_open_invite": func(reader *fieldReader) OperatorRequest {
		return &OperatorOpenInvite{ConversationHeader: conversationHeader(reader), Enabled: reader.laxBool("enabled", true)}
	},
	"operator_launch": func(reader *fieldReader) OperatorRequest {
		return &OperatorLaunch{ConversationHeader: conversationHeader(reader), Name: reader.text("name"), Plugin: reader.text("plugin")}
	},
	"operator_retire": func(reader *fieldReader) OperatorRequest {
		return &OperatorRetire{ConversationHeader: conversationHeader(reader), Name: reader.text("name")}
	},
	"operator_set_role": func(reader *fieldReader) OperatorRequest {
		return &OperatorSetRole{
			ConversationHeader: conversationHeader(reader),
			Name:               reader.text("name"),
			Role:               requiredLiteral(reader, "role", AssignableRoles),
		}
	},
	"operator_post": func(reader *fieldReader) OperatorRequest {
		return &OperatorPost{
			ConversationHeader: conversationHeader(reader),
			SubmissionID:       reader.text("submission_id"),
			Body:               reader.text("body"),
			Kind:               reader.literal("kind", "note", HumanPostKinds),
			To:                 reader.optionalText("to"),
			ReplyTo:            reader.optionalStrictInt("reply_to", MessageRange),
			DueMinutes:         reader.optionalStrictInt("due_minutes", DueRange),
		}
	},
	"operator_close": func(reader *fieldReader) OperatorRequest {
		return &OperatorClose{
			ConversationHeader: conversationHeader(reader),
			Summary:            reader.optionalText("summary"),
			DeprecateTasks:     reader.laxBool("deprecate_tasks", false),
		}
	},
	"operator_reopen": func(reader *fieldReader) OperatorRequest {
		return &OperatorReopen{ConversationHeader: conversationHeader(reader), Note: reader.optionalText("note")}
	},
	"operator_archive": func(reader *fieldReader) OperatorRequest {
		return &OperatorArchive{
			ConversationHeader: conversationHeader(reader),
			Archived:           requiredLaxBool(reader, "archived"),
			DeprecateTasks:     reader.laxBool("deprecate_tasks", false),
		}
	},
	"operator_rename": func(reader *fieldReader) OperatorRequest {
		return &OperatorRename{ConversationHeader: conversationHeader(reader), Title: requiredOptionalText(reader, "title")}
	},
	"operator_search": func(reader *fieldReader) OperatorRequest {
		return &OperatorSearch{CheckoutHeader: checkoutHeader(reader), Query: reader.text("query")}
	},
	"operator_plugins": func(reader *fieldReader) OperatorRequest {
		return &OperatorPlugins{OperatorHeader: OperatorHeader{Key: reader.text("key")}}
	},
	"operator_set_member_budget": func(reader *fieldReader) OperatorRequest {
		return &OperatorSetMemberBudget{
			ConversationHeader: conversationHeader(reader),
			Name:               reader.text("name"),
			Model:              reader.optionalText("model"),
			TokenBudget:        reader.optionalStrictInt("token_budget", BudgetRange),
		}
	},
}
