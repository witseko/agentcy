package wire

import (
	"errors"
)

// ErrInvalidRequest is returned for a request line that is not one known action.
var ErrInvalidRequest = errors.New("invalid member request")

// DecodeRequest parses and validates one member request line the way the Python core
// does: strict JSON, a known action, no unknown fields, typed and lenient fields as in
// the pydantic models, and every Validate rule. The error says nothing about the input.
func DecodeRequest(line []byte) (Request, error) {
	object, action, err := parseRequestLine(line)
	if err != nil {
		return nil, err
	}
	newRequest, known := memberRequests[action]
	if !known {
		return nil, ErrInvalidRequest
	}
	return decodeMember(object, newRequest())
}

// decodeMember reads and validates the fields of a parsed member request object.
func decodeMember(object Value, request Request) (Request, error) {
	if err := decodeFields(object, request); err != nil {
		return nil, err
	}
	return request, nil
}

// parseRequestLine parses a request line into an object and its string action.
func parseRequestLine(line []byte) (Value, string, error) {
	object, err := Parse(line)
	if err != nil || object.Kind != Object {
		return Value{}, "", ErrInvalidRequest
	}
	action, found := object.Get("action")
	if !found || action.Kind != String {
		return Value{}, "", ErrInvalidRequest
	}
	return object, action.Text, nil
}

// memberRequests maps each member action to a constructor of its empty request.
var memberRequests = map[string]func() Request{
	"create":          func() Request { return &Create{} },
	"join":            func() Request { return &Join{} },
	"post":            func() Request { return &Post{} },
	"read":            func() Request { return &Read{} },
	"listen":          func() Request { return &Listen{} },
	"close":           func() Request { return &Close{} },
	"reopen":          func() Request { return &Reopen{} },
	"status":          func() Request { return &Status{} },
	"invite":          func() Request { return &Invite{} },
	"release":         func() Request { return &Release{} },
	"launch":          func() Request { return &Launch{} },
	"ping":            func() Request { return &Ping{} },
	"session":         func() Request { return &Session{} },
	"retire":          func() Request { return &Retire{} },
	"set_role":        func() Request { return &SetRole{} },
	"plugins":         func() Request { return &Plugins{} },
	"open_invite":     func() Request { return &OpenInvite{} },
	"report_usage":    func() Request { return &ReportUsage{} },
	"list_closed":     func() Request { return &ListClosed{} },
	"read_closed":     func() Request { return &ReadClosed{} },
	"mcp_task_status": func() Request { return &McpTaskStatus{} },
	"mcp_task_cancel": func() Request { return &McpTaskCancel{} },
}
