package wire

// The schemas of the member requests, from src/agentcy/conversations/models.py. Each lists
// its fields in the order of the Python model's dump.

// memberFields returns the leading fields of every request that carries a token.
func memberFields(workspace, token *string, action string) []fieldSpec {
	return []fieldSpec{
		&textField{"workspace", workspace, checkWorkspace},
		&textField{"token", token, checkKey},
		actionField{action},
	}
}

// workspaceFields returns the leading fields of a request without a token.
func workspaceFields(workspace *string, action string) []fieldSpec {
	return []fieldSpec{&textField{"workspace", workspace, checkWorkspace}, actionField{action}}
}

// createRule checks the rules of a create request that span several fields.
func createRule(invitees []Slot, openInvite, sealed bool, deadlineMinutes *int64) string {
	if !openInvite && len(invitees) == 0 {
		return "Invite at least one participant or enable open_invite"
	}
	if deadlineMinutes != nil && !sealed {
		return "A proposal deadline requires a sealed round"
	}
	return ""
}

func (request *Create) schema() requestSchema {
	fields := append(workspaceFields(&request.Workspace, request.Action()),
		&textField{"brief", &request.Brief, checkBody},
		&textField{"initiator", &request.Initiator, checkLabel},
		&optionalTextField{key: "initiator_model", target: &request.InitiatorModel, rule: checkModel},
		&optionalIntField{"initiator_token_budget", &request.InitiatorTokenBudget, BudgetRange},
		&slotsField{"invitees", &request.Invitees},
		&boolField{key: "open_invite", target: &request.OpenInvite},
		&boolField{key: "sealed", target: &request.Sealed},
		&optionalIntField{"deadline_minutes", &request.DeadlineMinutes, DeadlineRange},
	)
	rule := func() string {
		return createRule(request.Invitees, request.OpenInvite, request.Sealed, request.DeadlineMinutes)
	}
	return requestSchema{title: "Create", fields: fields, rule: rule}
}

func (request *Join) schema() requestSchema {
	return requestSchema{title: "Join", fields: append(workspaceFields(&request.Workspace, request.Action()),
		&textField{"code", &request.Code, checkKey},
		&optionalTextField{key: "name", target: &request.Name, rule: checkLabel},
		&optionalTextField{key: "model", target: &request.Model, rule: checkModel},
		&optionalIntField{"token_budget", &request.TokenBudget, BudgetRange},
	)}
}

// schema puts the submission fields first because Post inherits them from a second base
// class in Python.
func (request *Post) schema() requestSchema {
	return requestSchema{title: "Post", fields: append([]fieldSpec{
		&textField{"submission_id", &request.SubmissionID, checkKey},
		&textField{"body", &request.Body, checkBody},
		&literalField{key: "kind", target: &request.Kind, allowed: PostKinds, fallback: "note"},
		&optionalTextField{key: "to", target: &request.To, rule: checkLabel},
		&optionalIntField{"reply_to", &request.ReplyTo, MessageRange},
		&optionalIntField{"due_minutes", &request.DueMinutes, DueRange},
	}, memberFields(&request.Workspace, &request.Token, request.Action())...)}
}

func (request *Read) schema() requestSchema {
	return requestSchema{title: "Read", fields: append(memberFields(&request.Workspace, &request.Token, request.Action()),
		&intField{key: "after", target: &request.After, limits: CursorRange, lax: true},
		&intField{key: "wait_seconds", target: &request.WaitSeconds, limits: WaitRange},
	)}
}

func (request *Listen) schema() requestSchema {
	return requestSchema{title: "Listen", fields: append(memberFields(&request.Workspace, &request.Token, request.Action()),
		&intField{key: "after", target: &request.After, limits: CursorRange, lax: true},
		&intField{key: "wait_seconds", target: &request.WaitSeconds, limits: ListenWaitRange},
	)}
}

func (request *Close) schema() requestSchema {
	return requestSchema{title: "Close", fields: append(memberFields(&request.Workspace, &request.Token, request.Action()),
		&optionalTextField{key: "summary", target: &request.Summary, rule: checkBody},
	)}
}

func (request *Reopen) schema() requestSchema {
	return requestSchema{title: "Reopen", fields: append(memberFields(&request.Workspace, &request.Token, request.Action()),
		&optionalTextField{key: "note", target: &request.Note, rule: checkBody},
	)}
}

func (request *Status) schema() requestSchema {
	return requestSchema{title: "Status", fields: memberFields(&request.Workspace, &request.Token, request.Action())}
}

func (request *Invite) schema() requestSchema {
	return requestSchema{title: "Invite", fields: append(memberFields(&request.Workspace, &request.Token, request.Action()),
		&textField{"name", &request.Name, checkLabel},
		&optionalLiteralField{"role", &request.Role, InviteRoles},
		&optionalTextField{key: "model", target: &request.Model, rule: checkModel},
		&optionalIntField{"token_budget", &request.TokenBudget, BudgetRange},
		&boolField{key: "reinvite", target: &request.Reinvite},
	)}
}

func (request *Release) schema() requestSchema {
	return requestSchema{title: "Release", fields: memberFields(&request.Workspace, &request.Token, request.Action())}
}

func (request *Launch) schema() requestSchema {
	return requestSchema{title: "Launch", fields: append(memberFields(&request.Workspace, &request.Token, request.Action()),
		&textField{"name", &request.Name, checkLabel},
		&textField{"plugin", &request.Plugin, checkPlugin},
		&textField{"directory", &request.Directory, checkLaunchDirectory},
		&boolField{key: "mcp_task", target: &request.McpTask},
	)}
}

func (request *Retire) schema() requestSchema {
	return requestSchema{title: "Retire", fields: append(memberFields(&request.Workspace, &request.Token, request.Action()),
		&textField{"name", &request.Name, checkLabel},
	)}
}

func (request *SetRole) schema() requestSchema {
	return requestSchema{title: "SetRole", fields: append(memberFields(&request.Workspace, &request.Token, request.Action()),
		&textField{"name", &request.Name, checkLabel},
		&literalField{key: "role", target: &request.Role, allowed: AssignableRoles, required: true},
	)}
}

func (request *Plugins) schema() requestSchema {
	return requestSchema{title: "Plugins", fields: memberFields(&request.Workspace, &request.Token, request.Action())}
}

func (request *OpenInvite) schema() requestSchema {
	return requestSchema{title: "OpenInvite", fields: append(memberFields(&request.Workspace, &request.Token, request.Action()),
		&boolField{key: "enabled", target: &request.Enabled, fallback: true},
	)}
}

func (request *ReportUsage) schema() requestSchema {
	return requestSchema{title: "ReportUsage", fields: append(memberFields(&request.Workspace, &request.Token, request.Action()),
		&intField{key: "input_tokens", target: &request.InputTokens, limits: CountRange, required: true},
		&intField{key: "output_tokens", target: &request.OutputTokens, limits: CountRange, required: true},
		&optionalTextField{key: "model", target: &request.Model, rule: checkModel},
	)}
}

func (request *ListClosed) schema() requestSchema {
	return requestSchema{title: "ListClosed", fields: append(workspaceFields(&request.Workspace, request.Action()),
		&optionalBoolField{"archived", &request.Archived},
	)}
}

func (request *ReadClosed) schema() requestSchema {
	return requestSchema{title: "ReadClosed", fields: append(workspaceFields(&request.Workspace, request.Action()),
		&textField{"conversation", &request.Conversation, checkConversation},
		&intField{key: "after", target: &request.After, limits: CursorRange, lax: true},
	)}
}

func (request *McpTaskStatus) schema() requestSchema {
	return requestSchema{title: "McpTaskStatus", fields: append(workspaceFields(&request.Workspace, request.Action()),
		&textField{"task_id", &request.TaskID, checkMcpTask},
	)}
}

func (request *McpTaskCancel) schema() requestSchema {
	return requestSchema{title: "McpTaskCancel", fields: append(workspaceFields(&request.Workspace, request.Action()),
		&textField{"task_id", &request.TaskID, checkMcpTask},
	)}
}
