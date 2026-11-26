package wire

// operatorFields returns the leading fields of every operator request.
func operatorFields(header OperatorHeader) []field {
	return []field{{"version", IntValue(OperatorProtocolVersion)}, {"key", TextValue(header.Key)}}
}

// checkoutFields returns the leading fields of a request that names a checkout.
func checkoutFields(header CheckoutHeader) []field {
	return append(operatorFields(header.OperatorHeader), field{"directory", TextValue(header.Directory)})
}

// conversationFields returns the leading fields of a request that names a conversation.
func conversationFields(header ConversationHeader) []field {
	return append(checkoutFields(header.CheckoutHeader), field{"conversation", TextValue(header.Conversation)})
}

// operatorObject encodes the leading fields, the action, and then the extra fields.
func operatorObject(leading []field, action string, extra ...field) Value {
	fields := append(leading, field{"action", TextValue(action)})
	return objectOf(append(fields, extra...)...)
}

// Encode returns the ordered dump of an operator_list request.
func (request *OperatorList) Encode() Value {
	return operatorObject(checkoutFields(request.CheckoutHeader), request.Action(), field{"archived", BoolValue(request.Archived)})
}

// Encode returns the ordered dump of an operator_show request.
func (request *OperatorShow) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(),
		field{"after", IntValue(request.After)},
		field{"before", optionalInt(request.Before)},
		field{"tail", BoolValue(request.Tail)},
	)
}

// Encode returns the ordered dump of an operator_tasks request.
func (request *OperatorTasks) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action())
}

// Encode returns the ordered dump of an operator_create request.
func (request *OperatorCreate) Encode() Value {
	invitees := make([]Value, 0, len(request.Invitees))
	for _, slot := range request.Invitees {
		invitees = append(invitees, encodeSlot(slot))
	}
	return operatorObject(checkoutFields(request.CheckoutHeader), request.Action(),
		field{"brief", TextValue(request.Brief)},
		field{"lead", TextValue(request.Lead)},
		field{"invitees", ListValue(invitees)},
		field{"open_invite", BoolValue(request.OpenInvite)},
		field{"sealed", BoolValue(request.Sealed)},
		field{"deadline_minutes", optionalInt(request.DeadlineMinutes)},
	)
}

// Encode returns the ordered dump of an operator_invite request.
func (request *OperatorInvite) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(),
		field{"name", TextValue(request.Name)},
		field{"role", optionalText(request.Role)},
		field{"reinvite", BoolValue(request.Reinvite)},
	)
}

// Encode returns the ordered dump of an operator_open_invite request.
func (request *OperatorOpenInvite) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(), field{"enabled", BoolValue(request.Enabled)})
}

// Encode returns the ordered dump of an operator_launch request.
func (request *OperatorLaunch) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(),
		field{"name", TextValue(request.Name)},
		field{"plugin", TextValue(request.Plugin)},
	)
}

// Encode returns the ordered dump of an operator_retire request.
func (request *OperatorRetire) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(), field{"name", TextValue(request.Name)})
}

// Encode returns the ordered dump of an operator_set_role request.
func (request *OperatorSetRole) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(),
		field{"name", TextValue(request.Name)},
		field{"role", TextValue(request.Role)},
	)
}

// Encode returns the ordered dump of an operator_post request. Python puts the submission
// fields first because PostMessage inherits them from a second base class.
func (request *OperatorPost) Encode() Value {
	submission := []field{
		{"submission_id", TextValue(request.SubmissionID)},
		{"body", TextValue(request.Body)},
		{"kind", TextValue(request.Kind)},
		{"to", optionalText(request.To)},
		{"reply_to", optionalInt(request.ReplyTo)},
		{"due_minutes", optionalInt(request.DueMinutes)},
	}
	return operatorObject(append(submission, conversationFields(request.ConversationHeader)...), request.Action())
}

// Encode returns the ordered dump of an operator_close request.
func (request *OperatorClose) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(),
		field{"summary", optionalText(request.Summary)},
		field{"deprecate_tasks", BoolValue(request.DeprecateTasks)},
	)
}

// Encode returns the ordered dump of an operator_reopen request.
func (request *OperatorReopen) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(), field{"note", optionalText(request.Note)})
}

// Encode returns the ordered dump of an operator_archive request.
func (request *OperatorArchive) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(),
		field{"archived", BoolValue(request.Archived)},
		field{"deprecate_tasks", BoolValue(request.DeprecateTasks)},
	)
}

// Encode returns the ordered dump of an operator_rename request.
func (request *OperatorRename) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(), field{"title", optionalText(request.Title)})
}

// Encode returns the ordered dump of an operator_search request.
func (request *OperatorSearch) Encode() Value {
	return operatorObject(checkoutFields(request.CheckoutHeader), request.Action(), field{"query", TextValue(request.Query)})
}

// Encode returns the ordered dump of an operator_plugins request.
func (request *OperatorPlugins) Encode() Value {
	return operatorObject(operatorFields(request.OperatorHeader), request.Action())
}

// Encode returns the ordered dump of an operator_set_member_budget request.
func (request *OperatorSetMemberBudget) Encode() Value {
	return operatorObject(conversationFields(request.ConversationHeader), request.Action(),
		field{"name", TextValue(request.Name)},
		field{"model", optionalText(request.Model)},
		field{"token_budget", optionalInt(request.TokenBudget)},
	)
}
