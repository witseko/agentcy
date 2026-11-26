package wire

import (
	"regexp"
	"strings"
)

// The schemas of the operator requests, from the former Python core's conversations/operator_models.py.
// Each lists its fields in the order of the Python model's dump.

// The limits of the operator fields, from the former Python core's conversations/operator_models.py
// and models.Title.
const (
	MaxTitleLength       = 80
	MaxSearchQueryLength = 200
)

// The patterns of the operator fields. "[^\x00]" also matches a newline, as in pydantic.
var (
	directoryPattern = regexp.MustCompile(`^/[^\x00]*$`)
	titlePattern     = regexp.MustCompile(`^[^\x00-\x1f\x7f]+$`)
)

// Pattern texts as pydantic prints them in errors.
const (
	directoryPatternText = `^/[^\x00]*$`
	titlePatternText     = `^[^\x00-\x1f\x7f]+$`
)

// NormalizeTitle strips a conversation title and validates it like models.Title: 1 to 80
// code points on one line without control characters.
func NormalizeTitle(text string) (string, *FieldError) {
	stripped := strings.TrimSpace(text)
	if err := CheckLength(stripped, 1, MaxTitleLength); err != nil {
		return "", err
	}
	if err := CheckPattern(stripped, titlePattern, titlePatternText); err != nil {
		return "", err
	}
	return stripped, nil
}

// checkTitle strips and checks a conversation title.
func checkTitle(errors *fieldErrors, location string, text *string) {
	normalize(errors, location, text, NormalizeTitle)
}

// checkCheckout checks the absolute path of a checkout. Both limits are reported.
func checkCheckout(errors *fieldErrors, location string, text *string) {
	errors.add(location, CheckLength(*text, 0, MaxDirectoryLength))
	errors.add(location, CheckPattern(*text, directoryPattern, directoryPatternText))
}

// checkSearchQuery checks the words of a search.
func checkSearchQuery(errors *fieldErrors, location string, text *string) {
	errors.add(location, CheckLength(*text, 1, MaxSearchQueryLength))
}

// operatorFields returns the version and key that every operator request carries.
func operatorFields(header *OperatorHeader) []fieldSpec {
	return []fieldSpec{versionField{}, &textField{"key", &header.Key, checkKey}}
}

// checkoutFields adds the checkout path to the operator fields.
func checkoutFields(header *CheckoutHeader) []fieldSpec {
	return append(operatorFields(&header.OperatorHeader), &textField{"directory", &header.Directory, checkCheckout})
}

// conversationFields adds the conversation ID to the checkout fields.
func conversationFields(header *ConversationHeader) []fieldSpec {
	return append(checkoutFields(&header.CheckoutHeader), &textField{"conversation", &header.Conversation, checkConversation})
}

// withAction returns the leading fields, the action, and then the extra fields.
func withAction(leading []fieldSpec, action string, extra ...fieldSpec) []fieldSpec {
	return append(append(leading, actionField{action}), extra...)
}

func (request *OperatorList) schema() requestSchema {
	return requestSchema{title: "ListConversations", fields: withAction(checkoutFields(&request.CheckoutHeader), request.Action(),
		&boolField{key: "archived", target: &request.Archived},
	)}
}

// schema allows only one pagination mode.
func (request *OperatorShow) schema() requestSchema {
	fields := withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&intField{key: "after", target: &request.After, limits: CursorRange, lax: true},
		&optionalIntField{"before", &request.Before, MessageRange},
		&boolField{key: "tail", target: &request.Tail},
	)
	rule := func() string {
		if request.Before != nil && (request.After != 0 || request.Tail) || request.Tail && request.After != 0 {
			return "Specify only one of after, before, or tail"
		}
		return ""
	}
	return requestSchema{title: "ShowConversation", fields: fields, rule: rule}
}

func (request *OperatorTasks) schema() requestSchema {
	return requestSchema{title: "ListTasks", fields: withAction(conversationFields(&request.ConversationHeader), request.Action())}
}

func (request *OperatorCreate) schema() requestSchema {
	fields := withAction(checkoutFields(&request.CheckoutHeader), request.Action(),
		&textField{"brief", &request.Brief, checkBody},
		&textField{"lead", &request.Lead, checkLabel},
		&slotsField{"invitees", &request.Invitees},
		&boolField{key: "open_invite", target: &request.OpenInvite},
		&boolField{key: "sealed", target: &request.Sealed},
		&optionalIntField{"deadline_minutes", &request.DeadlineMinutes, DeadlineRange},
	)
	rule := func() string {
		return createRule(request.Invitees, request.OpenInvite, request.Sealed, request.DeadlineMinutes)
	}
	return requestSchema{title: "CreateConversation", fields: fields, rule: rule}
}

func (request *OperatorInvite) schema() requestSchema {
	return requestSchema{title: "InviteParticipant", fields: withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&textField{"name", &request.Name, checkLabel},
		&optionalLiteralField{"role", &request.Role, InviteRoles},
		&boolField{key: "reinvite", target: &request.Reinvite},
	)}
}

func (request *OperatorOpenInvite) schema() requestSchema {
	return requestSchema{title: "SetOpenInvite", fields: withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&boolField{key: "enabled", target: &request.Enabled, fallback: true},
	)}
}

func (request *OperatorLaunch) schema() requestSchema {
	return requestSchema{title: "LaunchParticipant", fields: withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&textField{"name", &request.Name, checkLabel},
		&textField{"plugin", &request.Plugin, checkPlugin},
	)}
}

func (request *OperatorRetire) schema() requestSchema {
	return requestSchema{title: "RetireParticipant", fields: withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&textField{"name", &request.Name, checkLabel},
	)}
}

func (request *OperatorSetRole) schema() requestSchema {
	return requestSchema{title: "SetParticipantRole", fields: withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&textField{"name", &request.Name, checkLabel},
		&literalField{key: "role", target: &request.Role, allowed: AssignableRoles, required: true},
	)}
}

// schema puts the submission fields first because PostMessage inherits them from a second
// base class in Python.
func (request *OperatorPost) schema() requestSchema {
	submission := []fieldSpec{
		&textField{"submission_id", &request.SubmissionID, checkKey},
		&textField{"body", &request.Body, checkBody},
		&literalField{key: "kind", target: &request.Kind, allowed: HumanPostKinds, fallback: "note"},
		&optionalTextField{key: "to", target: &request.To, rule: checkLabel},
		&optionalIntField{"reply_to", &request.ReplyTo, MessageRange},
		&optionalIntField{"due_minutes", &request.DueMinutes, DueRange},
	}
	return requestSchema{title: "PostMessage", fields: withAction(append(submission, conversationFields(&request.ConversationHeader)...), request.Action())}
}

func (request *OperatorClose) schema() requestSchema {
	return requestSchema{title: "CloseConversation", fields: withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&optionalTextField{key: "summary", target: &request.Summary, rule: checkBody},
		&boolField{key: "deprecate_tasks", target: &request.DeprecateTasks},
	)}
}

func (request *OperatorReopen) schema() requestSchema {
	return requestSchema{title: "ReopenConversation", fields: withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&optionalTextField{key: "note", target: &request.Note, rule: checkBody},
	)}
}

func (request *OperatorArchive) schema() requestSchema {
	return requestSchema{title: "ArchiveConversation", fields: withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&boolField{key: "archived", target: &request.Archived, required: true},
		&boolField{key: "deprecate_tasks", target: &request.DeprecateTasks},
	)}
}

// schema requires the title, which is null to clear a custom title.
func (request *OperatorRename) schema() requestSchema {
	return requestSchema{title: "RenameConversation", fields: withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&optionalTextField{key: "title", target: &request.Title, rule: checkTitle, required: true},
	)}
}

func (request *OperatorSearch) schema() requestSchema {
	return requestSchema{title: "SearchConversations", fields: withAction(checkoutFields(&request.CheckoutHeader), request.Action(),
		&textField{"query", &request.Query, checkSearchQuery},
	)}
}

func (request *OperatorPlugins) schema() requestSchema {
	return requestSchema{title: "ListPlugins", fields: withAction(operatorFields(&request.OperatorHeader), request.Action())}
}

// schema needs a model, a budget, or both.
func (request *OperatorSetMemberBudget) schema() requestSchema {
	fields := withAction(conversationFields(&request.ConversationHeader), request.Action(),
		&textField{"name", &request.Name, checkLabel},
		&optionalTextField{key: "model", target: &request.Model, rule: checkModel},
		&optionalIntField{"token_budget", &request.TokenBudget, BudgetRange},
	)
	rule := func() string {
		if request.Model == nil && request.TokenBudget == nil {
			return "Specify model, token_budget, or both"
		}
		return ""
	}
	return requestSchema{title: "SetMemberBudget", fields: fields, rule: rule}
}
