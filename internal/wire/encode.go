package wire

// field is one name and value of an encoded request, in order.
type field struct {
	name  string
	value Value
}

// objectOf builds an ordered object from fields.
func objectOf(fields ...field) Value {
	object := NewObject()
	for _, entry := range fields {
		object.Set(entry.name, entry.value)
	}
	return object
}

// optionalText encodes a missing text as null.
func optionalText(text *string) Value {
	if text == nil {
		return NullValue()
	}
	return TextValue(*text)
}

// optionalInt encodes a missing integer as null.
func optionalInt(number *int64) Value {
	if number == nil {
		return NullValue()
	}
	return IntValue(*number)
}

// optionalBool encodes a missing boolean as null.
func optionalBool(flag *bool) Value {
	if flag == nil {
		return NullValue()
	}
	return BoolValue(*flag)
}

// memberFields returns the leading fields of every request that carries a token.
func memberFields(workspace, token, action string) []field {
	return []field{{"workspace", TextValue(workspace)}, {"token", TextValue(token)}, {"action", TextValue(action)}}
}

// memberObject encodes a token-carrying request with extra fields after the action.
func memberObject(workspace, token, action string, extra ...field) Value {
	return objectOf(append(memberFields(workspace, token, action), extra...)...)
}

// encodeSlot encodes a plain name as a string and an invitation as an object.
func encodeSlot(slot Slot) Value {
	if slot.Invitation == nil {
		return TextValue(slot.Name)
	}
	invitation := slot.Invitation
	return objectOf(
		field{"name", TextValue(invitation.Name)},
		field{"role", TextValue(invitation.Role)},
		field{"model", optionalText(invitation.Model)},
		field{"token_budget", optionalInt(invitation.TokenBudget)},
	)
}

// Encode returns the ordered dump of a create request.
func (request *Create) Encode() Value {
	invitees := make([]Value, 0, len(request.Invitees))
	for _, slot := range request.Invitees {
		invitees = append(invitees, encodeSlot(slot))
	}
	return objectOf(
		field{"workspace", TextValue(request.Workspace)},
		field{"action", TextValue(request.Action())},
		field{"brief", TextValue(request.Brief)},
		field{"initiator", TextValue(request.Initiator)},
		field{"initiator_model", optionalText(request.InitiatorModel)},
		field{"initiator_token_budget", optionalInt(request.InitiatorTokenBudget)},
		field{"invitees", ListValue(invitees)},
		field{"open_invite", BoolValue(request.OpenInvite)},
		field{"sealed", BoolValue(request.Sealed)},
		field{"deadline_minutes", optionalInt(request.DeadlineMinutes)},
	)
}

// Encode returns the ordered dump of a join request.
func (request *Join) Encode() Value {
	return objectOf(
		field{"workspace", TextValue(request.Workspace)},
		field{"action", TextValue(request.Action())},
		field{"code", TextValue(request.Code)},
		field{"name", optionalText(request.Name)},
		field{"model", optionalText(request.Model)},
		field{"token_budget", optionalInt(request.TokenBudget)},
	)
}

// Encode returns the ordered dump of a post request. Python puts the submission fields
// first because Post inherits them from a second base class.
func (request *Post) Encode() Value {
	return objectOf(
		field{"submission_id", TextValue(request.SubmissionID)},
		field{"body", TextValue(request.Body)},
		field{"kind", TextValue(request.Kind)},
		field{"to", optionalText(request.To)},
		field{"reply_to", optionalInt(request.ReplyTo)},
		field{"due_minutes", optionalInt(request.DueMinutes)},
		field{"workspace", TextValue(request.Workspace)},
		field{"token", TextValue(request.Token)},
		field{"action", TextValue(request.Action())},
	)
}

// Encode returns the ordered dump of a read request.
func (request *Read) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action(),
		field{"after", IntValue(request.After)},
		field{"wait_seconds", IntValue(request.WaitSeconds)},
	)
}

// Encode returns the ordered dump of a listen request.
func (request *Listen) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action(),
		field{"after", IntValue(request.After)},
		field{"wait_seconds", IntValue(request.WaitSeconds)},
	)
}

// Encode returns the ordered dump of a close request.
func (request *Close) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action(), field{"summary", optionalText(request.Summary)})
}

// Encode returns the ordered dump of a reopen request.
func (request *Reopen) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action(), field{"note", optionalText(request.Note)})
}

// Encode returns the ordered dump of a status request.
func (request *Status) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action())
}

// Encode returns the ordered dump of an invite request.
func (request *Invite) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action(),
		field{"name", TextValue(request.Name)},
		field{"role", optionalText(request.Role)},
		field{"model", optionalText(request.Model)},
		field{"token_budget", optionalInt(request.TokenBudget)},
		field{"reinvite", BoolValue(request.Reinvite)},
	)
}

// Encode returns the ordered dump of a release request.
func (request *Release) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action())
}

// Encode returns the ordered dump of a launch request.
func (request *Launch) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action(),
		field{"name", TextValue(request.Name)},
		field{"plugin", TextValue(request.Plugin)},
		field{"directory", TextValue(request.Directory)},
		field{"mcp_task", BoolValue(request.McpTask)},
	)
}

// Encode returns the ordered dump of a retire request.
func (request *Retire) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action(), field{"name", TextValue(request.Name)})
}

// Encode returns the ordered dump of a set_role request.
func (request *SetRole) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action(),
		field{"name", TextValue(request.Name)},
		field{"role", TextValue(request.Role)},
	)
}

// Encode returns the ordered dump of a plugins request.
func (request *Plugins) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action())
}

// Encode returns the ordered dump of an open_invite request.
func (request *OpenInvite) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action(), field{"enabled", BoolValue(request.Enabled)})
}

// Encode returns the ordered dump of a report_usage request.
func (request *ReportUsage) Encode() Value {
	return memberObject(request.Workspace, request.Token, request.Action(),
		field{"input_tokens", IntValue(request.InputTokens)},
		field{"output_tokens", IntValue(request.OutputTokens)},
		field{"model", optionalText(request.Model)},
	)
}

// Encode returns the ordered dump of a list_closed request.
func (request *ListClosed) Encode() Value {
	return objectOf(
		field{"workspace", TextValue(request.Workspace)},
		field{"action", TextValue(request.Action())},
		field{"archived", optionalBool(request.Archived)},
	)
}

// Encode returns the ordered dump of a read_closed request.
func (request *ReadClosed) Encode() Value {
	return objectOf(
		field{"workspace", TextValue(request.Workspace)},
		field{"action", TextValue(request.Action())},
		field{"conversation", TextValue(request.Conversation)},
		field{"after", IntValue(request.After)},
	)
}

// Encode returns the ordered dump of an mcp_task_status request.
func (request *McpTaskStatus) Encode() Value {
	return taskObject(request.Workspace, request.Action(), request.TaskID)
}

// Encode returns the ordered dump of an mcp_task_cancel request.
func (request *McpTaskCancel) Encode() Value {
	return taskObject(request.Workspace, request.Action(), request.TaskID)
}

// taskObject encodes the two MCP task requests.
func taskObject(workspace, action, taskID string) Value {
	return objectOf(
		field{"workspace", TextValue(workspace)},
		field{"action", TextValue(action)},
		field{"task_id", TextValue(taskID)},
	)
}
