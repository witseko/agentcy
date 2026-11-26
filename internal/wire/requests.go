package wire

// Request is one member request to the core. Each action has its own struct; the fields
// mirror the pydantic models in src/agentcy/conversations/models.py.
type Request interface {
	// Action returns the request's "action" value.
	Action() string
	// Validate checks every field, strips names and texts in place, and applies the rules
	// that span several fields. It returns a *ValidationError.
	Validate() error
	// Encode returns the request as an ordered JSON object with every field, in the field
	// order of the Python model's dump.
	Encode() Value
}

// The post kinds and roles that the models accept.
var (
	PostKinds       = []string{"note", "question", "finding", "blocker", "resume", "done", "task", "handoff", "accept", "decline", "proposal"}
	InviteRoles     = []string{"worker", "co-lead"}
	AssignableRoles = []string{"worker", "lead", "co-lead"}
)

// Invitation reserves a worker or co-lead slot when creating a conversation.
type Invitation struct {
	Name        string
	Role        string
	Model       *string
	TokenBudget *int64
}

// Slot is one invitee of a create request: a plain name, or an Invitation when set.
type Slot struct {
	Name       string
	Invitation *Invitation
}

// Create creates an open conversation and its invitation slots.
type Create struct {
	Workspace            string
	Brief                string
	Initiator            string
	InitiatorModel       *string
	InitiatorTokenBudget *int64
	Invitees             []Slot
	OpenInvite           bool
	Sealed               bool
	DeadlineMinutes      *int64
}

// Join redeems a single-use or shared open invitation.
type Join struct {
	Workspace   string
	Code        string
	Name        *string
	Model       *string
	TokenBudget *int64
}

// Post appends a participant message with retry protection.
type Post struct {
	SubmissionID string
	Body         string
	Kind         string
	To           *string
	ReplyTo      *int64
	DueMinutes   *int64
	Workspace    string
	Token        string
}

// Read reads a bounded page after a sequence number.
type Read struct {
	Workspace   string
	Token       string
	After       int64
	WaitSeconds int64
}

// Listen is a waiting read that also returns the caller's status (port plan fix L2).
// Only the Go core knows it: a shim falls back to read and status when the core rejects
// it as Invalid bridge arguments.
type Listen struct {
	Workspace   string
	Token       string
	After       int64
	WaitSeconds int64
}

// Read returns the waiting read that the listen performs.
func (request *Listen) Read() *Read {
	return &Read{Workspace: request.Workspace, Token: request.Token, After: request.After, WaitSeconds: request.WaitSeconds}
}

// Close closes a conversation and optionally records its outcome.
type Close struct {
	Workspace string
	Token     string
	Summary   *string
}

// Reopen reopens a closed conversation and optionally records a note.
type Reopen struct {
	Workspace string
	Token     string
	Note      *string
}

// Status reads the current activity of conversation members.
type Status struct {
	Workspace string
	Token     string
}

// Invite creates an invitation slot, reissues an unused join code, or reinvites a member.
type Invite struct {
	Workspace   string
	Token       string
	Name        string
	Role        *string
	Model       *string
	TokenBudget *int64
	Reinvite    bool
}

// Release releases a sealed proposal round.
type Release struct {
	Workspace string
	Token     string
}

// Launch reserves a worker slot and dispatches a configured local plugin.
type Launch struct {
	Workspace string
	Token     string
	Name      string
	Plugin    string
	Directory string
	McpTask   bool
}

// Retire retires a participant from an open conversation.
type Retire struct {
	Workspace string
	Token     string
	Name      string
}

// SetRole changes a participant's role in an open conversation.
type SetRole struct {
	Workspace string
	Token     string
	Name      string
	Role      string
}

// Plugins lists the locally configured fresh-run plugins.
type Plugins struct {
	Workspace string
	Token     string
}

// OpenInvite enables, rotates, or disables a conversation's shared open invite code.
type OpenInvite struct {
	Workspace string
	Token     string
	Enabled   bool
}

// ReportUsage reports exact cumulative token usage for the authenticated participant.
type ReportUsage struct {
	Workspace    string
	Token        string
	InputTokens  int64
	OutputTokens int64
	Model        *string
}

// ListClosed lists the workspace's closed conversations without a participant token.
type ListClosed struct {
	Workspace string
	Archived  *bool
}

// ReadClosed reads one page of a closed conversation without a participant token.
type ReadClosed struct {
	Workspace    string
	Conversation string
	After        int64
}

// McpTaskStatus reads the fresh-run state behind an MCP task.
type McpTaskStatus struct {
	Workspace string
	TaskID    string
}

// McpTaskCancel asks the fresh-run worker to cancel the run behind an MCP task.
type McpTaskCancel struct {
	Workspace string
	TaskID    string
}

// Action returns "create".
func (*Create) Action() string { return "create" }

// Action returns "join".
func (*Join) Action() string { return "join" }

// Action returns "post".
func (*Post) Action() string { return "post" }

// Action returns "read".
func (*Read) Action() string { return "read" }

// Action returns "listen".
func (*Listen) Action() string { return "listen" }

// Action returns "close".
func (*Close) Action() string { return "close" }

// Action returns "reopen".
func (*Reopen) Action() string { return "reopen" }

// Action returns "status".
func (*Status) Action() string { return "status" }

// Action returns "invite".
func (*Invite) Action() string { return "invite" }

// Action returns "release".
func (*Release) Action() string { return "release" }

// Action returns "launch".
func (*Launch) Action() string { return "launch" }

// Action returns "retire".
func (*Retire) Action() string { return "retire" }

// Action returns "set_role".
func (*SetRole) Action() string { return "set_role" }

// Action returns "plugins".
func (*Plugins) Action() string { return "plugins" }

// Action returns "open_invite".
func (*OpenInvite) Action() string { return "open_invite" }

// Action returns "report_usage".
func (*ReportUsage) Action() string { return "report_usage" }

// Action returns "list_closed".
func (*ListClosed) Action() string { return "list_closed" }

// Action returns "read_closed".
func (*ReadClosed) Action() string { return "read_closed" }

// Action returns "mcp_task_status".
func (*McpTaskStatus) Action() string { return "mcp_task_status" }

// Action returns "mcp_task_cancel".
func (*McpTaskCancel) Action() string { return "mcp_task_cancel" }
