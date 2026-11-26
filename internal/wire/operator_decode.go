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
	object, action, err := parseRequestLine(line)
	if err != nil {
		return Incoming{}, err
	}
	if newRequest, known := operatorRequests[action]; known {
		request, err := decodeOperator(object, newRequest())
		return Incoming{Operator: request}, err
	}
	if newRequest, known := memberRequests[action]; known {
		request, err := decodeMember(object, newRequest())
		return Incoming{Member: request}, err
	}
	return Incoming{}, ErrInvalidRequest
}

// decodeOperator checks the key's shape first, then reads and validates every field.
// Pydantic reports all field errors of a request at once, and the core answers
// OperatorKeyRejected whenever one of them is at "key", so the key check must not depend
// on the other fields.
func decodeOperator(object Value, request OperatorRequest) (OperatorRequest, error) {
	key, found := object.Get("key")
	if !found || key.Kind != String || CheckKey(key.Text) != nil {
		return nil, ErrOperatorKeyRejected
	}
	if err := decodeFields(object, request); err != nil {
		return nil, err
	}
	return request, nil
}

// versionField is the required "version" of an operator request, which must equal 1.
type versionField struct{}

func (versionField) name() string { return "version" }

func (versionField) read(errors *fieldErrors, location string, value Value, found bool) bool {
	if !found {
		missingField(errors, location)
		return false
	}
	err := checkOperatorVersion(value)
	errors.add(location, err)
	return err == nil
}

func (versionField) check(*fieldErrors, string) {}

func (versionField) encode() Value { return IntValue(OperatorProtocolVersion) }

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

// operatorRequests maps each operator action to a constructor of its empty request.
var operatorRequests = map[string]func() OperatorRequest{
	"operator_list":              func() OperatorRequest { return &OperatorList{} },
	"operator_show":              func() OperatorRequest { return &OperatorShow{} },
	"operator_tasks":             func() OperatorRequest { return &OperatorTasks{} },
	"operator_create":            func() OperatorRequest { return &OperatorCreate{} },
	"operator_invite":            func() OperatorRequest { return &OperatorInvite{} },
	"operator_open_invite":       func() OperatorRequest { return &OperatorOpenInvite{} },
	"operator_launch":            func() OperatorRequest { return &OperatorLaunch{} },
	"operator_retire":            func() OperatorRequest { return &OperatorRetire{} },
	"operator_set_role":          func() OperatorRequest { return &OperatorSetRole{} },
	"operator_post":              func() OperatorRequest { return &OperatorPost{} },
	"operator_close":             func() OperatorRequest { return &OperatorClose{} },
	"operator_reopen":            func() OperatorRequest { return &OperatorReopen{} },
	"operator_archive":           func() OperatorRequest { return &OperatorArchive{} },
	"operator_rename":            func() OperatorRequest { return &OperatorRename{} },
	"operator_search":            func() OperatorRequest { return &OperatorSearch{} },
	"operator_plugins":           func() OperatorRequest { return &OperatorPlugins{} },
	"operator_set_member_budget": func() OperatorRequest { return &OperatorSetMemberBudget{} },
}
