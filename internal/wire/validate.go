package wire

import "regexp"

// checker validates typed request fields, normalises texts in place, and collects the
// errors in field order.
type checker struct {
	errors fieldErrors
}

// workspace checks a workspace key.
func (check *checker) workspace(value string) {
	check.errors.add("workspace", CheckPattern(value, workspacePattern, workspacePatternText))
}

// key checks a credential or ID.
func (check *checker) key(name, value string) {
	check.errors.add(name, CheckKey(value))
}

// label strips and checks a required name.
func (check *checker) label(name string, value *string) {
	stripped, err := NormalizeLabel(*value)
	check.errors.add(name, err)
	if err == nil {
		*value = stripped
	}
}

// optionalLabel strips and checks a name that may be absent.
func (check *checker) optionalLabel(name string, value *string) {
	if value != nil {
		check.label(name, value)
	}
}

// body strips and checks a required message text.
func (check *checker) body(name string, value *string) {
	stripped, err := NormalizeBody(*value)
	check.errors.add(name, err)
	if err == nil {
		*value = stripped
	}
}

// optionalBody strips and checks a message text that may be absent.
func (check *checker) optionalBody(name string, value *string) {
	if value != nil {
		check.body(name, value)
	}
}

// pattern checks a text against an anchored pattern, when the text is present.
func (check *checker) pattern(name string, value *string, pattern *regexp.Regexp, patternText string) {
	if value != nil {
		check.errors.add(name, CheckPattern(*value, pattern, patternText))
	}
}

// model checks an optional model name.
func (check *checker) model(name string, value *string) {
	check.pattern(name, value, modelNamePattern, modelNamePatternText)
}

// number checks an integer against its range.
func (check *checker) number(name string, value int64, limits IntRange) {
	_, err := CoerceStrictInt(IntValue(value), limits)
	check.errors.add(name, err)
}

// optionalNumber checks an integer that may be absent.
func (check *checker) optionalNumber(name string, value *int64, limits IntRange) {
	if value != nil {
		check.number(name, *value, limits)
	}
}

// literal checks a text against its allowed values.
func (check *checker) literal(name, value string, allowed []string) {
	_, err := CheckLiteral(TextValue(value), allowed)
	check.errors.add(name, err)
}

// member checks the workspace and token of a token-carrying request.
func (check *checker) member(workspace, token string) {
	check.workspace(workspace)
	check.key("token", token)
}

// slots checks the invitees of a create request.
func (check *checker) slots(slots []Slot) {
	if len(slots) > MaxSlots {
		check.errors.add("invitees", problem("List should have at most 16 items after validation, not more", "too_long"))
		return
	}
	for index := range slots {
		location := "invitees." + itoa(index)
		slot := &slots[index]
		if slot.Invitation == nil {
			check.label(location, &slot.Name)
			continue
		}
		invitation := slot.Invitation
		check.label(location+".name", &invitation.Name)
		check.literal(location+".role", invitation.Role, InviteRoles)
		check.model(location+".model", invitation.Model)
		check.optionalNumber(location+".token_budget", invitation.TokenBudget, BudgetRange)
	}
}

// itoa formats a list index.
func itoa(index int) string {
	return IntValue(int64(index)).Literal
}

// Validate checks a create request, including its rules across fields.
func (request *Create) Validate() error {
	var check checker
	check.workspace(request.Workspace)
	check.body("brief", &request.Brief)
	check.label("initiator", &request.Initiator)
	check.model("initiator_model", request.InitiatorModel)
	check.optionalNumber("initiator_token_budget", request.InitiatorTokenBudget, BudgetRange)
	check.slots(request.Invitees)
	check.optionalNumber("deadline_minutes", request.DeadlineMinutes, DeadlineRange)
	if err := check.errors.result("Create"); err != nil {
		return err
	}
	if !request.OpenInvite && len(request.Invitees) == 0 {
		return modelError("Create", "Invite at least one participant or enable open_invite")
	}
	if request.DeadlineMinutes != nil && !request.Sealed {
		return modelError("Create", "A proposal deadline requires a sealed round")
	}
	return nil
}

// Validate checks a join request.
func (request *Join) Validate() error {
	var check checker
	check.workspace(request.Workspace)
	check.key("code", request.Code)
	check.optionalLabel("name", request.Name)
	check.model("model", request.Model)
	check.optionalNumber("token_budget", request.TokenBudget, BudgetRange)
	return check.errors.result("Join")
}

// Validate checks a post request.
func (request *Post) Validate() error {
	var check checker
	check.key("submission_id", request.SubmissionID)
	check.body("body", &request.Body)
	check.literal("kind", request.Kind, PostKinds)
	check.optionalLabel("to", request.To)
	check.optionalNumber("reply_to", request.ReplyTo, MessageRange)
	check.optionalNumber("due_minutes", request.DueMinutes, DueRange)
	check.member(request.Workspace, request.Token)
	return check.errors.result("Post")
}

// Validate checks a read request.
func (request *Read) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	check.number("after", request.After, CursorRange)
	check.number("wait_seconds", request.WaitSeconds, WaitRange)
	return check.errors.result("Read")
}

// Validate checks a listen request.
func (request *Listen) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	check.number("after", request.After, CursorRange)
	check.number("wait_seconds", request.WaitSeconds, ListenWaitRange)
	return check.errors.result("Listen")
}

// Validate checks a close request.
func (request *Close) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	check.optionalBody("summary", request.Summary)
	return check.errors.result("Close")
}

// Validate checks a reopen request.
func (request *Reopen) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	check.optionalBody("note", request.Note)
	return check.errors.result("Reopen")
}

// Validate checks a status request.
func (request *Status) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	return check.errors.result("Status")
}

// Validate checks an invite request.
func (request *Invite) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	check.label("name", &request.Name)
	if request.Role != nil {
		check.literal("role", *request.Role, InviteRoles)
	}
	check.model("model", request.Model)
	check.optionalNumber("token_budget", request.TokenBudget, BudgetRange)
	return check.errors.result("Invite")
}

// Validate checks a release request.
func (request *Release) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	return check.errors.result("Release")
}

// Validate checks a launch request.
func (request *Launch) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	check.label("name", &request.Name)
	check.pattern("plugin", &request.Plugin, pluginPattern, pluginPatternText)
	check.errors.add("directory", CheckLength(request.Directory, 1, MaxDirectoryLength))
	return check.errors.result("Launch")
}

// Validate checks a retire request.
func (request *Retire) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	check.label("name", &request.Name)
	return check.errors.result("Retire")
}

// Validate checks a set_role request.
func (request *SetRole) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	check.label("name", &request.Name)
	check.literal("role", request.Role, AssignableRoles)
	return check.errors.result("SetRole")
}

// Validate checks a plugins request.
func (request *Plugins) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	return check.errors.result("Plugins")
}

// Validate checks an open_invite request.
func (request *OpenInvite) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	return check.errors.result("OpenInvite")
}

// Validate checks a report_usage request.
func (request *ReportUsage) Validate() error {
	var check checker
	check.member(request.Workspace, request.Token)
	check.number("input_tokens", request.InputTokens, CountRange)
	check.number("output_tokens", request.OutputTokens, CountRange)
	check.model("model", request.Model)
	return check.errors.result("ReportUsage")
}

// Validate checks a list_closed request.
func (request *ListClosed) Validate() error {
	var check checker
	check.workspace(request.Workspace)
	return check.errors.result("ListClosed")
}

// Validate checks a read_closed request.
func (request *ReadClosed) Validate() error {
	var check checker
	check.workspace(request.Workspace)
	check.pattern("conversation", &request.Conversation, conversationPattern, conversationPatternText)
	check.number("after", request.After, CursorRange)
	return check.errors.result("ReadClosed")
}

// Validate checks an mcp_task_status request.
func (request *McpTaskStatus) Validate() error {
	var check checker
	check.workspace(request.Workspace)
	check.pattern("task_id", &request.TaskID, mcpTaskPattern, mcpTaskPatternText)
	return check.errors.result("McpTaskStatus")
}

// Validate checks an mcp_task_cancel request.
func (request *McpTaskCancel) Validate() error {
	var check checker
	check.workspace(request.Workspace)
	check.pattern("task_id", &request.TaskID, mcpTaskPattern, mcpTaskPatternText)
	return check.errors.result("McpTaskCancel")
}
