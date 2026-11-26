package shim

import (
	"encoding/json"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/witseko/agentcy/internal/wire"
)

// Close confirmation, from src/agentcy/mcp/confirmation.py. The confirmation is a
// safeguard against a model acting by mistake; the core still authorizes the close.
const (
	confirmInputID  = "agentcy.mcp.confirmation:confirm_close"
	confirmState    = "confirm_close"
	confirmQuestion = "Close this conversation?"
)

// The form schemas the Python shim sends. The handshake era also carries the title and
// description of the form model.
var (
	legacyConfirmSchema = json.RawMessage(`{"description":"The one-field form that the human answers.","properties":{"confirm":{"description":"Select to confirm this action.","title":"Confirm","type":"boolean"}},"required":["confirm"],"title":"Confirmation","type":"object"}`)
	modernConfirmSchema = json.RawMessage(`{"properties":{"confirm":{"description":"Select to confirm this action.","title":"Confirm","type":"boolean"}},"required":["confirm"],"type":"object"}`)
)

// askUser is returned by a tool that needs the user's answer before it can run. Its
// result carries the input request; the SDK sends it as an input_required result in the
// 2026-07-28 era and as an elicitation/create request before.
type askUser struct {
	result *mcp.CallToolResult
}

// Error describes the pending question for logs; it is never shown to the client.
func (*askUser) Error() string { return "the tool waits for the user's answer" }

// userRefusal is the tool error for a declined or cancelled confirmation.
type userRefusal struct {
	verb string
}

// Error returns the text the calling model sees.
func (refusal *userRefusal) Error() string {
	return "The user " + refusal.verb + " this action, so nothing changed. Do not retry it unless the user asks again."
}

// declaresFormElicitation tells whether this request may get a form elicitation: a bare
// `elicitation: {}` or one with `form` qualifies; a URL-only client does not.
func declaresFormElicitation(request *mcp.CallToolRequest) bool {
	capabilities := request.ClientCapabilities()
	if capabilities == nil || capabilities.Elicitation == nil {
		return false
	}
	return capabilities.Elicitation.Form != nil || capabilities.Elicitation.URL == nil
}

// confirmClose returns nil when the user confirmed the close or cannot be asked, an
// *askUser when the question must go out, and a *userRefusal on a decline or cancel.
// An answer counts only together with a requestState that this process sealed for the
// same call; any other answer is discarded and the question is asked again.
func (shim *shimServer) confirmClose(request *mcp.CallToolRequest, arguments wire.Value) error {
	if !declaresFormElicitation(request) {
		return nil
	}
	params := request.Params
	if answer, found := params.InputResponses[confirmInputID]; found && params.RequestState != "" {
		state, err := shim.signer.verify(params.RequestState, params.Name, arguments)
		if err == nil && state == confirmState {
			return confirmationAnswer(answer)
		}
	}
	schema := legacyConfirmSchema
	if isModern(request) {
		schema = modernConfirmSchema
	}
	return &askUser{result: &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{
			confirmInputID: &mcp.ElicitParams{Mode: "form", Message: confirmQuestion, RequestedSchema: schema},
		},
		RequestState: shim.signer.seal(params.Name, arguments, confirmState),
	}}
}

// confirmationAnswer maps the user's answer to nil for a confirmed close and to a
// refusal otherwise. An accept whose confirm field is false counts as a decline.
func confirmationAnswer(answer mcp.InputResponse) error {
	result, ok := answer.(*mcp.ElicitResult)
	if ok && result.Action == "accept" && confirmedField(result.Content["confirm"]) {
		return nil
	}
	if ok && result.Action == "cancel" {
		return &userRefusal{verb: "cancelled"}
	}
	return &userRefusal{verb: "declined"}
}

// confirmedField reads the form's confirm field with pydantic's lax boolean rules.
func confirmedField(value any) bool {
	var converted wire.Value
	switch typed := value.(type) {
	case bool:
		converted = wire.BoolValue(typed)
	case string:
		converted = wire.TextValue(typed)
	case float64:
		converted = wire.Value{Kind: wire.Number, Literal: strconv.FormatFloat(typed, 'f', -1, 64)}
	default:
		return false
	}
	confirmed, problem := wire.CoerceLaxBool(converted)
	return problem == nil && confirmed
}
