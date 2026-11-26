package wire

// OperatorRequest is one operator request to the core, sent by a local interface for the
// human such as the terminal UI. Each action has its own struct; the fields mirror the
// pydantic models in src/agentcy/conversations/operator_models.py.
type OperatorRequest interface {
	// Action returns the request's "action" value.
	Action() string
	// Validate checks every field, strips names and texts in place, and applies the rules
	// that span several fields. It returns a *ValidationError.
	Validate() error
	// Encode returns the request as an ordered JSON object with every field, in the field
	// order of the Python model's dump.
	Encode() Value
	// OperatorKey returns the key that the request carries. Compare it in constant time.
	OperatorKey() string
}

// HumanPostKinds are the kinds that the human may post.
var HumanPostKinds = []string{"note", "question"}

// OperatorProtocolVersion is the only "version" that operator requests may carry.
const OperatorProtocolVersion = 1

// OperatorHeader holds the operator key that every operator request carries. The decoder
// checks "version"; Encode always writes OperatorProtocolVersion.
type OperatorHeader struct {
	Key string
}

// OperatorKey returns the key that the request carries.
func (header *OperatorHeader) OperatorKey() string { return header.Key }

// CheckoutHeader adds the absolute path of the checkout that names the workspace.
type CheckoutHeader struct {
	OperatorHeader
	Directory string
}

// ConversationHeader adds one conversation of the checkout's workspace.
type ConversationHeader struct {
	CheckoutHeader
	Conversation string
}

// OperatorList lists the workspace's active or archived conversations, newest first.
type OperatorList struct {
	CheckoutHeader
	Archived bool
}

// OperatorShow reads status, the brief and one page of the full transcript.
type OperatorShow struct {
	ConversationHeader
	After  int64
	Before *int64
	Tail   bool
}

// OperatorTasks lists all tasks in one conversation.
type OperatorTasks struct {
	ConversationHeader
}

// OperatorCreate creates a conversation in which every slot, the lead included, is an
// invitation.
type OperatorCreate struct {
	CheckoutHeader
	Brief           string
	Lead            string
	Invitees        []Slot
	OpenInvite      bool
	Sealed          bool
	DeadlineMinutes *int64
}

// OperatorInvite adds a slot, reissues an unused code, or reinvites a member.
type OperatorInvite struct {
	ConversationHeader
	Name     string
	Role     *string
	Reinvite bool
}

// OperatorOpenInvite enables, rotates, or disables the shared open invite code.
type OperatorOpenInvite struct {
	ConversationHeader
	Enabled bool
}

// OperatorLaunch fills a worker slot with a fresh run from a configured plugin.
type OperatorLaunch struct {
	ConversationHeader
	Name   string
	Plugin string
}

// OperatorRetire retires a participant from an open conversation.
type OperatorRetire struct {
	ConversationHeader
	Name string
}

// OperatorSetRole changes an active participant's role in an open conversation.
type OperatorSetRole struct {
	ConversationHeader
	Name string
	Role string
}

// OperatorPost posts a note or question as the human, with retry protection.
type OperatorPost struct {
	ConversationHeader
	SubmissionID string
	Body         string
	Kind         string
	To           *string
	ReplyTo      *int64
	DueMinutes   *int64
}

// OperatorClose closes the conversation with an optional summary.
type OperatorClose struct {
	ConversationHeader
	Summary        *string
	DeprecateTasks bool
}

// OperatorReopen reopens a closed conversation with an optional note.
type OperatorReopen struct {
	ConversationHeader
	Note *string
}

// OperatorArchive moves a closed conversation to the archive, or back.
type OperatorArchive struct {
	ConversationHeader
	Archived       bool
	DeprecateTasks bool
}

// OperatorRename sets a custom title, or clears it when Title is nil.
type OperatorRename struct {
	ConversationHeader
	Title *string
}

// OperatorSearch finds words in the briefs and messages of the workspace.
type OperatorSearch struct {
	CheckoutHeader
	Query string
}

// OperatorPlugins lists the configured fresh-run plugins by name and model.
type OperatorPlugins struct {
	OperatorHeader
}

// OperatorSetMemberBudget binds a model name and/or a starting token budget to a
// participant.
type OperatorSetMemberBudget struct {
	ConversationHeader
	Name        string
	Model       *string
	TokenBudget *int64
}

// Action returns "operator_list".
func (*OperatorList) Action() string { return "operator_list" }

// Action returns "operator_show".
func (*OperatorShow) Action() string { return "operator_show" }

// Action returns "operator_tasks".
func (*OperatorTasks) Action() string { return "operator_tasks" }

// Action returns "operator_create".
func (*OperatorCreate) Action() string { return "operator_create" }

// Action returns "operator_invite".
func (*OperatorInvite) Action() string { return "operator_invite" }

// Action returns "operator_open_invite".
func (*OperatorOpenInvite) Action() string { return "operator_open_invite" }

// Action returns "operator_launch".
func (*OperatorLaunch) Action() string { return "operator_launch" }

// Action returns "operator_retire".
func (*OperatorRetire) Action() string { return "operator_retire" }

// Action returns "operator_set_role".
func (*OperatorSetRole) Action() string { return "operator_set_role" }

// Action returns "operator_post".
func (*OperatorPost) Action() string { return "operator_post" }

// Action returns "operator_close".
func (*OperatorClose) Action() string { return "operator_close" }

// Action returns "operator_reopen".
func (*OperatorReopen) Action() string { return "operator_reopen" }

// Action returns "operator_archive".
func (*OperatorArchive) Action() string { return "operator_archive" }

// Action returns "operator_rename".
func (*OperatorRename) Action() string { return "operator_rename" }

// Action returns "operator_search".
func (*OperatorSearch) Action() string { return "operator_search" }

// Action returns "operator_plugins".
func (*OperatorPlugins) Action() string { return "operator_plugins" }

// Action returns "operator_set_member_budget".
func (*OperatorSetMemberBudget) Action() string { return "operator_set_member_budget" }
