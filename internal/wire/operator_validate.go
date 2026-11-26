package wire

import (
	"regexp"
	"strings"
)

// The limits of the operator fields, from src/agentcy/conversations/operator_models.py
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

// operatorKey checks the operator key of a request.
func (check *checker) operatorKey(key string) {
	check.key("key", key)
}

// checkout checks the key and the absolute checkout path.
func (check *checker) checkout(header CheckoutHeader) {
	check.operatorKey(header.Key)
	check.errors.add("directory", CheckLength(header.Directory, 0, MaxDirectoryLength))
	check.pattern("directory", &header.Directory, directoryPattern, directoryPatternText)
}

// conversation checks the key, the checkout path, and the conversation ID.
func (check *checker) conversation(header ConversationHeader) {
	check.checkout(header.CheckoutHeader)
	check.pattern("conversation", &header.Conversation, conversationPattern, conversationPatternText)
}

// title strips and checks a title that may be absent.
func (check *checker) title(name string, value *string) {
	if value == nil {
		return
	}
	stripped, err := NormalizeTitle(*value)
	check.errors.add(name, err)
	if err == nil {
		*value = stripped
	}
}

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

// Validate checks an operator_list request.
func (request *OperatorList) Validate() error {
	var check checker
	check.checkout(request.CheckoutHeader)
	return check.errors.result("ListConversations")
}

// Validate checks an operator_show request, including its single pagination mode.
func (request *OperatorShow) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	check.number("after", request.After, CursorRange)
	check.optionalNumber("before", request.Before, MessageRange)
	if err := check.errors.result("ShowConversation"); err != nil {
		return err
	}
	if request.Before != nil && (request.After != 0 || request.Tail) || request.Tail && request.After != 0 {
		return modelError("ShowConversation", "Specify only one of after, before, or tail")
	}
	return nil
}

// Validate checks an operator_tasks request.
func (request *OperatorTasks) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	return check.errors.result("ListTasks")
}

// Validate checks an operator_create request, including its rules across fields.
func (request *OperatorCreate) Validate() error {
	var check checker
	check.checkout(request.CheckoutHeader)
	check.body("brief", &request.Brief)
	check.label("lead", &request.Lead)
	check.slots(request.Invitees)
	check.optionalNumber("deadline_minutes", request.DeadlineMinutes, DeadlineRange)
	if err := check.errors.result("CreateConversation"); err != nil {
		return err
	}
	if !request.OpenInvite && len(request.Invitees) == 0 {
		return modelError("CreateConversation", "Invite at least one participant or enable open_invite")
	}
	if request.DeadlineMinutes != nil && !request.Sealed {
		return modelError("CreateConversation", "A proposal deadline requires a sealed round")
	}
	return nil
}

// Validate checks an operator_invite request.
func (request *OperatorInvite) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	check.label("name", &request.Name)
	if request.Role != nil {
		check.literal("role", *request.Role, InviteRoles)
	}
	return check.errors.result("InviteParticipant")
}

// Validate checks an operator_open_invite request.
func (request *OperatorOpenInvite) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	return check.errors.result("SetOpenInvite")
}

// Validate checks an operator_launch request.
func (request *OperatorLaunch) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	check.label("name", &request.Name)
	check.pattern("plugin", &request.Plugin, pluginPattern, pluginPatternText)
	return check.errors.result("LaunchParticipant")
}

// Validate checks an operator_retire request.
func (request *OperatorRetire) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	check.label("name", &request.Name)
	return check.errors.result("RetireParticipant")
}

// Validate checks an operator_set_role request.
func (request *OperatorSetRole) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	check.label("name", &request.Name)
	check.literal("role", request.Role, AssignableRoles)
	return check.errors.result("SetParticipantRole")
}

// Validate checks an operator_post request.
func (request *OperatorPost) Validate() error {
	var check checker
	check.key("submission_id", request.SubmissionID)
	check.body("body", &request.Body)
	check.literal("kind", request.Kind, HumanPostKinds)
	check.optionalLabel("to", request.To)
	check.optionalNumber("reply_to", request.ReplyTo, MessageRange)
	check.optionalNumber("due_minutes", request.DueMinutes, DueRange)
	check.conversation(request.ConversationHeader)
	return check.errors.result("PostMessage")
}

// Validate checks an operator_close request.
func (request *OperatorClose) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	check.optionalBody("summary", request.Summary)
	return check.errors.result("CloseConversation")
}

// Validate checks an operator_reopen request.
func (request *OperatorReopen) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	check.optionalBody("note", request.Note)
	return check.errors.result("ReopenConversation")
}

// Validate checks an operator_archive request.
func (request *OperatorArchive) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	return check.errors.result("ArchiveConversation")
}

// Validate checks an operator_rename request.
func (request *OperatorRename) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	check.title("title", request.Title)
	return check.errors.result("RenameConversation")
}

// Validate checks an operator_search request.
func (request *OperatorSearch) Validate() error {
	var check checker
	check.checkout(request.CheckoutHeader)
	check.errors.add("query", CheckLength(request.Query, 1, MaxSearchQueryLength))
	return check.errors.result("SearchConversations")
}

// Validate checks an operator_plugins request.
func (request *OperatorPlugins) Validate() error {
	var check checker
	check.operatorKey(request.Key)
	return check.errors.result("ListPlugins")
}

// Validate checks an operator_set_member_budget request, which needs a model or a budget.
func (request *OperatorSetMemberBudget) Validate() error {
	var check checker
	check.conversation(request.ConversationHeader)
	check.label("name", &request.Name)
	check.model("model", request.Model)
	check.optionalNumber("token_budget", request.TokenBudget, BudgetRange)
	if err := check.errors.result("SetMemberBudget"); err != nil {
		return err
	}
	if request.Model == nil && request.TokenBudget == nil {
		return modelError("SetMemberBudget", "Specify model, token_budget, or both")
	}
	return nil
}
