//go:build conformance

package conformance

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// From test_operator_core.py and the core's handler: malformed member requests all get
// "Invalid bridge arguments", while rule failures get their own text.
func TestInvalidRequests(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Validation", "initiator": "Lead", "invitees": []any{"Worker"}})
	lead := created.text("token")
	s.send("no action", fields{"workspace": s.workspace, "token": lead})
	s.send("an unknown action", fields{"action": "explode", "workspace": s.workspace})
	s.send("a numeric action", fields{"action": 7, "workspace": s.workspace})
	s.member("an unknown field", "status", fields{"token": lead, "extra": 1})
	s.member("no workspace", "status", fields{"token": lead, "workspace": nil})
	s.member("a short workspace", "status", fields{"token": lead, "workspace": "abc"})
	s.member("an uppercase workspace", "status", fields{"token": lead, "workspace": strings.Repeat("A", 64)})
	s.member("an empty token", "status", fields{"token": ""})
	s.member("a 129-character token", "status", fields{"token": strings.Repeat("t", 129)})
	s.member("a numeric token", "status", fields{"token": 5})
	s.member("an unknown token", "status", fields{"token": "unknown"})
	s.member("a 128-character token is valid but unknown", "status", fields{"token": strings.Repeat("t", 128)})
	s.member("join without a code", "join", fields{})
	s.member("join with a 129-character code", "join", fields{"code": strings.Repeat("c", 129)})
	s.member("post without a submission ID", "post", fields{"token": lead, "body": "Hi"})
	s.member("post with an empty submission ID", "post", fields{"token": lead, "submission_id": "", "body": "Hi"})
	s.member("post without a body", "post", fields{"token": lead, "submission_id": "x"})
	s.member("post with a numeric body", "post", fields{"token": lead, "submission_id": "x", "body": 5})
	s.member("post with a null kind", "post", fields{"token": lead, "submission_id": "x", "body": "Hi", "kind": nil})
	s.member("post with a reassign kind", "post", fields{"token": lead, "submission_id": "x", "body": "Hi", "kind": "reassign"})
	s.member("post with an event kind", "post", fields{"token": lead, "submission_id": "x", "body": "Hi", "kind": "event"})
	s.member("create with a boolean as the brief", "create", fields{"brief": true, "initiator": "Lead", "invitees": []any{"W"}})
	s.member("create with an invitee object missing its name", "create", fields{"brief": "B", "initiator": "Lead", "invitees": []any{fields{"role": "worker"}}})
	s.member("create with an invitee object and an unknown field", "create", fields{"brief": "B", "initiator": "Lead", "invitees": []any{fields{"name": "W", "extra": 1}}})
	s.member("create with a lead invitee role", "create", fields{"brief": "B", "initiator": "Lead", "invitees": []any{fields{"name": "W", "role": "lead"}}})
	s.member("create with a string open_invite", "create", fields{"brief": "B", "initiator": "Lead", "invitees": []any{"W"}, "open_invite": "yes"})
	s.member("create with an integer open_invite", "create", fields{"brief": "B", "initiator": "Lead", "invitees": []any{"W"}, "open_invite": 1})
	s.member("create with a float token budget", "create", fields{"brief": "B", "initiator": "Lead", "invitees": []any{"W"}, "initiator_token_budget": 10.5})
	s.member("create with a string token budget", "create", fields{"brief": "B", "initiator": "Lead", "invitees": []any{"W"}, "initiator_token_budget": "10"})
	s.member("read with a boolean cursor", "read", fields{"token": lead, "after": true})
	s.member("invite with a string reinvite", "invite", fields{"token": lead, "name": "X", "reinvite": "true"})
	s.member("open_invite with a null enabled", "open_invite", fields{"token": lead, "enabled": nil})
	s.member("set_role with a null role", "set_role", fields{"token": lead, "name": "Worker", "role": nil})
	s.member("list_closed with a string archived", "list_closed", fields{"archived": "yes"})
	s.member("read_closed with a numeric cursor string", "read_closed", fields{"conversation": "0123456789abcdef01234567", "after": "5"})
}

// From the core's handler: framing rules for one request line. A line may be 128 KiB
// including its newline; longer or unterminated lines get "Request exceeds size limit";
// a client that never finishes its line gets no reply after the daemon's 5-second timeout.
func TestRequestFraming(t *testing.T) {
	s := newScenario(t)
	s.sendRaw("an empty line", "a single newline", []byte("\n"), false)
	s.sendRaw("a line of spaces", "three spaces and a newline", []byte("   \n"), false)
	s.sendRaw("invalid JSON", "{not json", []byte("{not json\n"), false)
	s.sendRaw("a JSON array", "[1, 2]", []byte("[1, 2]\n"), false)
	s.sendRaw("a JSON string", "\"status\"", []byte("\"status\"\n"), false)
	s.sendRaw("null", "null", []byte("null\n"), false)
	s.sendRaw("two JSON objects on one line", "two objects", []byte(`{"action":"list_closed","workspace":"`+s.workspace+`"} {}`+"\n"), false)
	s.sendRaw("invalid UTF-8", "a request with the byte 0xff in a string", []byte(`{"action":"list_closed","workspace":"`+s.workspace+"\xff\"}\n"), false)
	s.sendRaw("a duplicate key, last one wins", "action given twice", []byte(`{"action":"explode","action":"list_closed","workspace":"`+s.workspace+`"}`+"\n"), false)
	s.sendRaw("a carriage return before the newline", "CRLF line ending", []byte(`{"action":"list_closed","workspace":"`+s.workspace+`"}`+"\r\n"), false)
	s.sendRaw("a second line is ignored", "two lines", []byte(`{"action":"list_closed","workspace":"`+s.workspace+`"}`+"\n"+`{"action":"explode"}`+"\n"), false)
	s.sendRaw("a line at the size limit", "131072 bytes with the newline", paddedStatusLine(s.workspace, maxRequest), false)
	s.sendRaw("a line one byte over the limit", "131073 bytes with the newline", paddedStatusLine(s.workspace, maxRequest+1), false)
	s.sendRaw("a much larger line", "300000 bytes", paddedStatusLine(s.workspace, 300000), false)
	s.sendRaw("a valid request without a newline, then half-close", "no newline, write side closed",
		[]byte(`{"action":"list_closed","workspace":"`+s.workspace+`"}`), true)
	s.sendRaw("an empty request, then half-close", "no bytes, write side closed", []byte{}, true)
	s.sendRaw("a stalled request", "no newline, connection kept open", []byte(`{"action":"list_closed"`), false)
	s.member("the daemon still serves after the stall", "list_closed", fields{})
}

// paddedStatusLine builds a status request with an unknown token, padded with JSON
// whitespace to exactly size bytes including the newline.
func paddedStatusLine(workspace string, size int) []byte {
	prefix := fmt.Sprintf(`{"action":"status","workspace":"%s","token":"unknown"`, workspace)
	padding := size - len(prefix) - len("}\n")
	return []byte(prefix + string(bytes.Repeat([]byte(" "), padding)) + "}\n")
}
