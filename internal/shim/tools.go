package shim

import (
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/witseko/agentcy/internal/wire"
)

// toolFunction runs one tool: it validates the arguments, calls the core, and returns
// the result object that becomes the tool's JSON text.
type toolFunction func(call toolCall) (wire.Value, error)

// toolCall is one tools/call request as a tool function sees it.
type toolCall struct {
	request   *mcp.CallToolRequest
	arguments *argumentReader
	// raw is the arguments object as sent, for binding a requestState to the call.
	raw wire.Value
}

// errNoCheckout is the tool error of launch_participant on a shim without a checkout.
var errNoCheckout = errors.New("This bridge has no configured checkout")

// memberTools returns the tool functions of the main shim by tool name. Each is a port
// of the tool of the same name in src/agentcy/mcp/server.py.
func (shim *shimServer) memberTools() map[string]toolFunction {
	return map[string]toolFunction{
		"create_conversation":       shim.createConversation,
		"invite_participant":        shim.inviteParticipant,
		"open_invite":               shim.openInvite,
		"retire_participant":        shim.retireParticipant,
		"set_participant_role":      shim.setParticipantRole,
		"list_plugins":              shim.listPlugins,
		"join_conversation":         shim.joinConversation,
		"report_usage":              shim.reportUsage,
		"post_message":              shim.postMessage,
		"read_messages":             shim.readMessages,
		"launch_participant":        shim.launchParticipant,
		"conversation_status":       shim.conversationStatus,
		"release_proposals":         shim.releaseProposals,
		"close_conversation":        shim.closeConversation,
		"reopen_conversation":       shim.reopenConversation,
		"list_closed_conversations": shim.listClosedConversations,
		"read_closed_conversation":  shim.readClosedConversation,
	}
}

// send validates the tool arguments, then validates and sends the request.
func (shim *shimServer) send(call toolCall, tool string, request wire.Request) (wire.Value, error) {
	if err := call.arguments.err(tool); err != nil {
		return wire.Value{}, err
	}
	return exchangeValid(shim.core, request)
}

func (shim *shimServer) createConversation(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.Create{Workspace: shim.workspace}
	request.Brief = arguments.body("brief")
	request.Initiator = arguments.label("your_name")
	request.Invitees = arguments.slots("invitees")
	request.OpenInvite = arguments.flag("open_invite", false)
	request.Sealed = arguments.flag("sealed", false)
	request.DeadlineMinutes = arguments.optionalStrictInt("deadline_minutes", wire.DeadlineRange)
	request.InitiatorModel = arguments.optionalModel("your_model")
	request.InitiatorTokenBudget = arguments.optionalStrictInt("your_token_budget", wire.BudgetRange)
	return shim.send(call, "create_conversation", request)
}

func (shim *shimServer) inviteParticipant(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.Invite{Workspace: shim.workspace}
	request.Token = arguments.key("token")
	request.Name = arguments.label("name")
	request.Role = arguments.optionalLiteral("role", wire.InviteRoles)
	request.Model = arguments.optionalModel("model")
	request.TokenBudget = arguments.optionalStrictInt("token_budget", wire.BudgetRange)
	request.Reinvite = arguments.flag("reinvite", false)
	return shim.send(call, "invite_participant", request)
}

func (shim *shimServer) openInvite(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.OpenInvite{Workspace: shim.workspace}
	request.Token = arguments.key("token")
	request.Enabled = arguments.flag("enabled", true)
	return shim.send(call, "open_invite", request)
}

func (shim *shimServer) retireParticipant(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.Retire{Workspace: shim.workspace}
	request.Token = arguments.key("token")
	request.Name = arguments.label("name")
	return shim.send(call, "retire_participant", request)
}

func (shim *shimServer) setParticipantRole(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.SetRole{Workspace: shim.workspace}
	request.Token = arguments.key("token")
	request.Name = arguments.label("name")
	request.Role = arguments.requiredLiteral("role", wire.AssignableRoles)
	return shim.send(call, "set_participant_role", request)
}

func (shim *shimServer) listPlugins(call toolCall) (wire.Value, error) {
	request := &wire.Plugins{Workspace: shim.workspace, Token: call.arguments.key("token")}
	return shim.send(call, "list_plugins", request)
}

func (shim *shimServer) joinConversation(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.Join{Workspace: shim.workspace}
	request.Code = arguments.key("code")
	request.Name = arguments.optionalLabel("name")
	request.Model = arguments.optionalModel("model")
	request.TokenBudget = arguments.optionalStrictInt("token_budget", wire.BudgetRange)
	return shim.send(call, "join_conversation", request)
}

func (shim *shimServer) reportUsage(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.ReportUsage{Workspace: shim.workspace}
	request.Token = arguments.key("token")
	request.InputTokens = arguments.requiredStrictInt("input_tokens", wire.CountRange)
	request.OutputTokens = arguments.requiredStrictInt("output_tokens", wire.CountRange)
	request.Model = arguments.optionalModel("model")
	return shim.send(call, "report_usage", request)
}

// postMessage posts and then forgets the poster's quiet waits.
func (shim *shimServer) postMessage(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.Post{Workspace: shim.workspace}
	request.Token = arguments.key("token")
	request.SubmissionID = arguments.key("submission_id")
	request.Body = arguments.body("body")
	request.Kind = arguments.literal("kind", "note", wire.PostKinds)
	request.To = arguments.optionalLabel("to")
	request.ReplyTo = arguments.optionalStrictInt("reply_to", wire.MessageRange)
	request.DueMinutes = arguments.optionalStrictInt("due_minutes", wire.DueRange)
	result, err := shim.send(call, "post_message", request)
	if err != nil {
		return wire.Value{}, err
	}
	shim.state.resetWaits(tokenDigest(request.Token))
	return result, nil
}

func (shim *shimServer) readMessages(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	token := arguments.key("token")
	after := arguments.cursor("after")
	waitSeconds := arguments.strictInt("wait_seconds", 0, wire.WaitRange)
	if err := arguments.err("read_messages"); err != nil {
		return wire.Value{}, err
	}
	return readMember(shim.core, shim.state, shim.workspace, token, after, waitSeconds)
}

func (shim *shimServer) launchParticipant(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.Launch{Workspace: shim.workspace}
	request.Token = arguments.key("token")
	request.Name = arguments.label("name")
	request.Plugin = arguments.plain("plugin")
	if err := arguments.err("launch_participant"); err != nil {
		return wire.Value{}, err
	}
	if shim.directory == "" {
		return wire.Value{}, errNoCheckout
	}
	request.Directory = shim.directory
	request.McpTask = wantsMcpTask(call.request)
	return exchangeValid(shim.core, request)
}

// conversationStatus returns the status without the caller's own name.
func (shim *shimServer) conversationStatus(call toolCall) (wire.Value, error) {
	request := &wire.Status{Workspace: shim.workspace, Token: call.arguments.key("token")}
	status, err := shim.send(call, "conversation_status", request)
	if err != nil {
		return wire.Value{}, err
	}
	status.Delete("participant")
	return status, nil
}

func (shim *shimServer) releaseProposals(call toolCall) (wire.Value, error) {
	request := &wire.Release{Workspace: shim.workspace, Token: call.arguments.key("token")}
	return shim.send(call, "release_proposals", request)
}

// closeConversation asks the user to confirm when the client can ask, then closes.
func (shim *shimServer) closeConversation(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.Close{Workspace: shim.workspace}
	request.Token = arguments.key("token")
	request.Summary = arguments.optionalBody("summary")
	if err := arguments.err("close_conversation"); err != nil {
		return wire.Value{}, err
	}
	if err := shim.confirmClose(call.request, call.raw); err != nil {
		return wire.Value{}, err
	}
	return exchangeValid(shim.core, request)
}

func (shim *shimServer) reopenConversation(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.Reopen{Workspace: shim.workspace}
	request.Token = arguments.key("token")
	request.Note = arguments.optionalBody("note")
	return shim.send(call, "reopen_conversation", request)
}

func (shim *shimServer) listClosedConversations(call toolCall) (wire.Value, error) {
	request := &wire.ListClosed{Workspace: shim.workspace, Archived: call.arguments.optionalFlag("archived")}
	return shim.send(call, "list_closed_conversations", request)
}

func (shim *shimServer) readClosedConversation(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.ReadClosed{Workspace: shim.workspace}
	request.Conversation = arguments.conversation("conversation")
	request.After = arguments.cursor("after")
	return shim.send(call, "read_closed_conversation", request)
}
