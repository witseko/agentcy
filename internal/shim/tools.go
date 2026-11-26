package shim

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/witseko/agentcy/internal/wire"
)

// toolFunction runs one tool: it validates the arguments, calls the core, and returns
// the result object that becomes the tool's JSON text.
type toolFunction func(call toolCall) (wire.Value, error)

// toolCall is one tools/call request as a tool function sees it.
type toolCall struct {
	ctx     context.Context
	request *mcp.CallToolRequest
	// tool is the called tool's name, which titles argument errors.
	tool string
	// arguments is the arguments object as sent, parsed once per call. It also binds a
	// requestState to the call.
	arguments wire.Value
	// requestState is the state sealed in the call's verified requestState, or "" when the
	// call carries none.
	requestState string
}

// errNoCheckout is the tool error of launch_participant on a shim without a checkout.
var errNoCheckout = errors.New("This bridge has no configured checkout")

// sameNames binds each argument to the request field of the same name.
func sameNames(names ...string) []wire.Argument {
	bindings := make([]wire.Argument, len(names))
	for index, name := range names {
		bindings[index] = wire.Argument{Name: name, Field: name}
	}
	return bindings
}

// The arguments of each tool, in the order of the tool function's signature in
// the former Python core's mcp/server.py and fresh_server.py, bound to the request fields they fill.
// A tool without arguments of its own, such as the fresh worker's status, binds none.
var (
	createArguments = []wire.Argument{
		{Name: "brief", Field: "brief"},
		{Name: "your_name", Field: "initiator"},
		{Name: "invitees", Field: "invitees", NullIsAbsent: true},
		{Name: "open_invite", Field: "open_invite"},
		{Name: "sealed", Field: "sealed"},
		{Name: "deadline_minutes", Field: "deadline_minutes"},
		{Name: "your_model", Field: "initiator_model"},
		{Name: "your_token_budget", Field: "initiator_token_budget"},
	}
	inviteArguments     = sameNames("token", "name", "role", "model", "token_budget", "reinvite")
	openInviteArguments = sameNames("token", "enabled")
	nameArguments       = sameNames("token", "name")
	setRoleArguments    = sameNames("token", "name", "role")
	tokenArguments      = sameNames("token")
	joinArguments       = sameNames("code", "name", "model", "token_budget")
	usageArguments      = sameNames("token", "input_tokens", "output_tokens", "model")
	postArguments       = sameNames("token", "submission_id", "body", "kind", "to", "reply_to", "due_minutes")
	freshPostArguments  = sameNames("submission_id", "body", "kind", "to", "reply_to")
	readArguments       = sameNames("token", "after", "wait_seconds")
	freshReadArguments  = sameNames("after", "wait_seconds")
	launchArguments     = []wire.Argument{
		{Name: "token", Field: "token"},
		{Name: "name", Field: "name"},
		{Name: "plugin", Field: "plugin", TypeOnly: true},
		{Name: "relaunch", Field: "relaunch"},
	}
	closeArguments      = sameNames("token", "summary")
	reopenArguments     = sameNames("token", "note")
	listClosedArguments = sameNames("archived")
	readClosedArguments = sameNames("conversation", "after")
)

// memberTools returns the tool functions of the main shim by tool name. Each is a port
// of the tool of the same name in the former Python core's mcp/server.py.
func (shim *shimServer) memberTools() map[string]toolFunction {
	return map[string]toolFunction{
		"create_conversation":       shim.createConversation,
		"invite_participant":        shim.sender(func() wire.Request { return &wire.Invite{Workspace: shim.workspace} }, inviteArguments),
		"open_invite":               shim.sender(func() wire.Request { return &wire.OpenInvite{Workspace: shim.workspace} }, openInviteArguments),
		"retire_participant":        shim.sender(func() wire.Request { return &wire.Retire{Workspace: shim.workspace} }, nameArguments),
		"ping_participant":          shim.sender(func() wire.Request { return &wire.Ping{Workspace: shim.workspace} }, nameArguments),
		"set_participant_role":      shim.sender(func() wire.Request { return &wire.SetRole{Workspace: shim.workspace} }, setRoleArguments),
		"list_plugins":              shim.sender(func() wire.Request { return &wire.Plugins{Workspace: shim.workspace} }, tokenArguments),
		"join_conversation":         shim.joinConversation,
		"report_usage":              shim.reportUsage,
		"post_message":              shim.postMessage,
		"read_messages":             shim.readMessages,
		"launch_participant":        shim.launchParticipant,
		"conversation_status":       shim.conversationStatus,
		"release_proposals":         shim.sender(func() wire.Request { return &wire.Release{Workspace: shim.workspace} }, tokenArguments),
		"close_conversation":        shim.closeConversation,
		"reopen_conversation":       shim.sender(func() wire.Request { return &wire.Reopen{Workspace: shim.workspace} }, reopenArguments),
		"list_closed_conversations": shim.sender(func() wire.Request { return &wire.ListClosed{Workspace: shim.workspace} }, listClosedArguments),
		"read_closed_conversation":  shim.sender(func() wire.Request { return &wire.ReadClosed{Workspace: shim.workspace} }, readClosedArguments),
	}
}

// sender returns a tool that fills a new request from the tool arguments and sends it.
func (shim *shimServer) sender(newRequest func() wire.Request, bindings []wire.Argument) toolFunction {
	return func(call toolCall) (wire.Value, error) {
		return shim.send(call, newRequest(), bindings)
	}
}

// send fills request from the tool arguments, validates the fields that the arguments
// did not fill, and sends the request.
func (shim *shimServer) send(call toolCall, request wire.Request, bindings []wire.Argument) (wire.Value, error) {
	if err := wire.ReadArguments(request, call.tool, call.arguments, bindings); err != nil {
		return wire.Value{}, err
	}
	return exchangeRest(call.ctx, shim.core, request, bindings)
}

// exchangeRest validates the fields of request that bindings did not check, then sends
// it. A validation failure has the text pydantic gives when the Python shim builds the
// model.
func exchangeRest(ctx context.Context, core Exchanger, request wire.Request, bindings []wire.Argument) (wire.Value, error) {
	if err := wire.ValidateRest(request, bindings); err != nil {
		return wire.Value{}, err
	}
	return core.Exchange(ctx, request)
}

// createConversation creates a conversation and registers the creator's MCP session.
func (shim *shimServer) createConversation(call toolCall) (wire.Value, error) {
	result, err := shim.send(call, &wire.Create{Workspace: shim.workspace}, createArguments)
	if err != nil {
		return wire.Value{}, err
	}
	token, _ := result.Get("token")
	shim.presence.touch(call.request, token.StringOf())
	return result, nil
}

// joinConversation redeems a join code and registers the participant's MCP session.
func (shim *shimServer) joinConversation(call toolCall) (wire.Value, error) {
	result, err := shim.send(call, &wire.Join{Workspace: shim.workspace}, joinArguments)
	if err != nil {
		return wire.Value{}, err
	}
	token, _ := result.Get("token")
	shim.presence.touch(call.request, token.StringOf())
	return result, nil
}

// reportUsage records usage and registers the participant's MCP session.
func (shim *shimServer) reportUsage(call toolCall) (wire.Value, error) {
	request := &wire.ReportUsage{Workspace: shim.workspace}
	result, err := shim.send(call, request, usageArguments)
	if err != nil {
		return wire.Value{}, err
	}
	shim.presence.touch(call.request, request.Token)
	return result, nil
}

// postMessage posts and then forgets the poster's quiet waits.
func (shim *shimServer) postMessage(call toolCall) (wire.Value, error) {
	request := &wire.Post{Workspace: shim.workspace}
	result, err := shim.send(call, request, postArguments)
	if err != nil {
		return wire.Value{}, err
	}
	shim.state.resetWaits(tokenDigest(request.Token))
	shim.presence.touch(call.request, request.Token)
	return result, nil
}

// readMessages reads after the cursor and optionally waits for activity.
func (shim *shimServer) readMessages(call toolCall) (wire.Value, error) {
	return shim.readFor(call, &wire.Read{Workspace: shim.workspace}, readArguments)
}

// readFor fills a read from the tool arguments, validates it, and reads for its token.
func (shim *shimServer) readFor(call toolCall, request *wire.Read, bindings []wire.Argument) (wire.Value, error) {
	if err := wire.ReadArguments(request, call.tool, call.arguments, bindings); err != nil {
		return wire.Value{}, err
	}
	if err := wire.ValidateRest(request, bindings); err != nil {
		return wire.Value{}, err
	}
	shim.presence.touch(call.request, request.Token)
	return readMember(call.ctx, shim.core, shim.state, request.Workspace, request.Token, request.After, request.WaitSeconds)
}

// conversationStatus returns the status without the caller's own name.
func (shim *shimServer) conversationStatus(call toolCall) (wire.Value, error) {
	return shim.statusFor(call, &wire.Status{Workspace: shim.workspace}, tokenArguments)
}

// statusFor fetches conversation status and removes the caller's own name.
func (shim *shimServer) statusFor(call toolCall, request *wire.Status, bindings []wire.Argument) (wire.Value, error) {
	status, err := shim.send(call, request, bindings)
	if err != nil {
		return wire.Value{}, err
	}
	shim.presence.touch(call.request, request.Token)
	status.Delete("participant")
	return status, nil
}

func (shim *shimServer) launchParticipant(call toolCall) (wire.Value, error) {
	request := &wire.Launch{Workspace: shim.workspace}
	if err := wire.ReadArguments(request, call.tool, call.arguments, launchArguments); err != nil {
		return wire.Value{}, err
	}
	if shim.directory == "" {
		return wire.Value{}, errNoCheckout
	}
	request.Directory = shim.directory
	request.McpTask = wantsMcpTask(call.request)
	return exchangeRest(call.ctx, shim.core, request, launchArguments)
}

// closeConversation asks the user to confirm when the client can ask, then closes.
func (shim *shimServer) closeConversation(call toolCall) (wire.Value, error) {
	request := &wire.Close{Workspace: shim.workspace}
	if err := wire.ReadArguments(request, call.tool, call.arguments, closeArguments); err != nil {
		return wire.Value{}, err
	}
	if err := shim.confirmClose(call); err != nil {
		return wire.Value{}, err
	}
	return exchangeRest(call.ctx, shim.core, request, closeArguments)
}
