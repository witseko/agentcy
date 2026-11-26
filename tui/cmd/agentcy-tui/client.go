package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/wire"
)

const (
	operatorKeyFile = wire.OperatorKeyFileName
	socketFile      = wire.SocketFileName
	// The core answers this when a request does not match its schema. The UI sends only
	// requests that match its own core version, so a mismatch means a stale core.
	invalidArgumentsError = wire.InvalidArguments
	coreVersionHelp       = "The running core is likely an older version. Stop it with 'make stop-core' in the agentcy clone and restart the UI"
	callTimeout           = 10 * time.Second
)

// errCoreUnreachable marks a failed dial, for example after the core exited while idle.
var errCoreUnreachable = wire.ErrCoreUnreachable

// CoreError carries the error text that the core returned for one request.
type CoreError struct {
	Message string
}

// Error returns the core's error text, with a restart hint when the core rejects the request schema.
func (e *CoreError) Error() string {
	if e.Message == invalidArgumentsError {
		return e.Message + ". " + coreVersionHelp
	}
	return e.Message
}

// Operator is the set of operator protocol actions that the UI uses.
type Operator interface {
	Plugins() (PluginList, error)
	List(request ListRequest) (ConversationList, error)
	Show(request ShowRequest) (ConversationView, error)
	Tasks(request TasksRequest) (TaskList, error)
	Create(request CreateRequest) (CreatedConversation, error)
	Invite(request InviteRequest) (JoinInvitation, error)
	OpenInvite(request OpenInviteRequest) (OpenInviteStatus, error)
	Launch(request LaunchRequest) (LaunchQueued, error)
	Retire(request RetireRequest) (RetiredParticipant, error)
	SetRole(request SetRoleRequest) (ParticipantRoleChanged, error)
	SetMemberBudget(request SetMemberBudgetRequest) (MemberBudgetUpdated, error)
	Post(request PostRequest) (PostedMessage, error)
	Close(request CloseRequest) (ClosedConversation, error)
	Reopen(request ReopenRequest) (ReopenedConversation, error)
	Archive(request ArchiveRequest) (ArchivedConversation, error)
	Rename(request RenameRequest) (RenamedConversation, error)
	Search(request SearchRequest) (SearchResults, error)
}

// PluginsRequest lists configured plugins. It takes no fields besides the envelope.
type PluginsRequest struct{}

// ListRequest lists the active or archived conversations of a checkout's workspace.
type ListRequest struct {
	Directory string `json:"directory"`
	Archived  bool   `json:"archived,omitempty"`
}

// ShowRequest reads status, the brief and one transcript page: forward after a cursor,
// the newest page when Tail is set, or an older page before a message ID.
type ShowRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	After        int64  `json:"after"`
	Before       *int64 `json:"before,omitempty"`
	Tail         bool   `json:"tail,omitempty"`
}

// TasksRequest lists every task in a conversation with its current state and timeline.
type TasksRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
}

// CreateRequest creates a conversation in which every slot is an invitation.
type CreateRequest struct {
	Directory  string   `json:"directory"`
	Brief      string   `json:"brief"`
	Lead       string   `json:"lead"`
	Invitees   []string `json:"invitees"`
	OpenInvite bool     `json:"open_invite,omitempty"`
}

// InviteRequest adds a slot or reissues the unused code of an existing slot. With Reinvite,
// a joined or retired member gets a new code and its previous chat loses access.
type InviteRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Name         string `json:"name"`
	Reinvite     bool   `json:"reinvite,omitempty"`
}

// OpenInviteRequest enables, rotates, or disables a reusable room-level join code.
type OpenInviteRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Enabled      bool   `json:"enabled"`
}

// LaunchRequest fills a slot with a fresh run from a configured plugin.
type LaunchRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Name         string `json:"name"`
	Plugin       string `json:"plugin"`
}

// RetireRequest retires a participant from an open conversation.
type RetireRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Name         string `json:"name"`
}

// SetRoleRequest changes an active participant's role in an open conversation.
type SetRoleRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Name         string `json:"name"`
	Role         string `json:"role"`
}

// PostRequest posts a note or question as the human.
type PostRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	SubmissionID string `json:"submission_id"`
	Body         string `json:"body"`
	Kind         string `json:"kind,omitempty"`
	To           string `json:"to,omitempty"`
}

// CloseRequest closes a conversation with an optional summary.
type CloseRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Summary      string `json:"summary,omitempty"`
	// DeprecateTasks ends every unfinished task as deprecated in the same step.
	DeprecateTasks bool `json:"deprecate_tasks,omitempty"`
}

// ReopenRequest reopens a closed conversation with an optional note.
type ReopenRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Note         string `json:"note,omitempty"`
}

// ArchiveRequest moves a closed conversation to the archive, or back to the active list.
type ArchiveRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Archived     bool   `json:"archived"`
	// DeprecateTasks ends every unfinished task as deprecated in the same step.
	DeprecateTasks bool `json:"deprecate_tasks,omitempty"`
}

// RenameRequest sets a custom title. A nil Title uses the first line of the brief again.
type RenameRequest struct {
	Directory    string  `json:"directory"`
	Conversation string  `json:"conversation"`
	Title        *string `json:"title"`
}

// SearchRequest finds words in every brief and published message of the workspace.
type SearchRequest struct {
	Directory string `json:"directory"`
	Query     string `json:"query"`
}

// SetMemberBudgetRequest binds a model name and/or starting token budget to one participant.
type SetMemberBudgetRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Name         string `json:"name"`
	Model        string `json:"model,omitempty"`
	TokenBudget  *int64 `json:"token_budget,omitempty"`
}

// MemberBudgetUpdated confirms a participant's updated model and/or starting token budget.
type MemberBudgetUpdated struct {
	Participant string `json:"participant"`
	Model       string `json:"model,omitempty"`
	TokenBudget *int64 `json:"token_budget,omitempty"`
}

// PluginSummary names one configured plugin and its model.
type PluginSummary struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

// PluginList lists the configured plugins.
type PluginList struct {
	Plugins []PluginSummary `json:"plugins"`
}

// ConversationSummary summarizes one conversation for the list screen.
type ConversationSummary struct {
	Conversation string `json:"conversation"`
	Title        string `json:"title"`
	Open         bool   `json:"open"`
	Created      *int64 `json:"created"`
	Members      int    `json:"members"`
	Joined       int    `json:"joined"`
}

// ConversationList holds up to 100 conversations, newest first.
type ConversationList struct {
	Conversations []ConversationSummary `json:"conversations"`
}

// OpenTask identifies one unfinished task of a member, with its due time as Unix seconds.
type OpenTask struct {
	ID      int64 `json:"id"`
	Blocked *bool `json:"blocked,omitempty"`
	Due     int64 `json:"due,omitempty"`
	Overdue bool  `json:"overdue,omitempty"`
}

// TaskEvent is one message in a task's timeline.
type TaskEvent struct {
	ID      int64  `json:"id"`
	Author  string `json:"author"`
	Body    string `json:"body"`
	Created int64  `json:"created"`
	Kind    string `json:"kind,omitempty"`
	To      string `json:"to,omitempty"`
	ReplyTo *int64 `json:"reply_to,omitempty"`
}

// TaskDetail holds one task's summary, owner, state, due time (Unix seconds), and chronological events.
type TaskDetail struct {
	ID               int64       `json:"id"`
	Title            string      `json:"title"`
	Body             string      `json:"body"`
	Author           string      `json:"author"`
	Owner            string      `json:"owner"`
	State            string      `json:"state"`
	Created          int64       `json:"created"`
	Updated          int64       `json:"updated"`
	PendingHandoffTo string      `json:"pending_handoff_to,omitempty"`
	Due              int64       `json:"due,omitempty"`
	Overdue          bool        `json:"overdue,omitempty"`
	Events           []TaskEvent `json:"events"`
}

// TaskList holds every task in a conversation, newest first.
type TaskList struct {
	Tasks []TaskDetail `json:"tasks"`
}

// MemberUsage reports one member's token spend and remaining budget.
type MemberUsage struct {
	Model              string   `json:"model,omitempty"`
	ConversationTokens int64    `json:"conversation_tokens"`
	InputTokens        int64    `json:"input_tokens"`
	OutputTokens       int64    `json:"output_tokens"`
	ModelTokensUsed    int64    `json:"model_tokens_used"`
	EstimatedTokens    int64    `json:"estimated_tokens"`
	ActualTokens       *int64   `json:"actual_tokens,omitempty"`
	Source             string   `json:"source"`
	InitialTokens      *int64   `json:"initial_tokens,omitempty"`
	RemainingTokens    *int64   `json:"remaining_tokens,omitempty"`
	UsedPercent        *float64 `json:"used_percent,omitempty"`
	RemainingPercent   *float64 `json:"remaining_percent,omitempty"`
	Status             string   `json:"status,omitempty"`
}

// TokenBalance summarizes whether active models are spending evenly against their budgets.
type TokenBalance struct {
	Balanced            bool     `json:"balanced"`
	ImbalanceGapPercent float64  `json:"imbalance_gap_percent"`
	Overloaded          []string `json:"overloaded,omitempty"`
	Preferred           []string `json:"preferred,omitempty"`
	Recommendation      string   `json:"recommendation,omitempty"`
}

// ConversationUsage sums the tokens all agents spent: actual where known, else estimated.
type ConversationUsage struct {
	TotalTokens      int64 `json:"total_tokens"`
	ActualTokens     int64 `json:"actual_tokens"`
	EstimatedTokens  int64 `json:"estimated_tokens"`
	ActualMembers    int64 `json:"actual_members"`
	EstimatedMembers int64 `json:"estimated_members"`
}

// MemberStatus reports one member's role, state, launch state, open tasks and usage.
type MemberStatus struct {
	Role        string       `json:"role"`
	State       string       `json:"state"`
	LaunchState string       `json:"launch_state,omitempty"`
	IdleSeconds *int64       `json:"idle_seconds,omitempty"`
	Messages    *int64       `json:"messages,omitempty"`
	OpenTasks   []OpenTask   `json:"open_tasks,omitempty"`
	Usage       *MemberUsage `json:"usage,omitempty"`
}

// ConversationStatus reports whether the conversation is open and how each member stands.
type ConversationStatus struct {
	Open          bool                    `json:"open"`
	Members       map[string]MemberStatus `json:"members"`
	OpenInvite    bool                    `json:"open_invite,omitempty"`
	Round         string                  `json:"round,omitempty"`
	RoundDeadline *int64                  `json:"round_deadline,omitempty"`
	TokenBalance  *TokenBalance           `json:"token_balance,omitempty"`
	TokenUsage    *ConversationUsage      `json:"token_usage,omitempty"`
}

// TranscriptMessage is one published message.
type TranscriptMessage struct {
	ID      int64  `json:"id"`
	Label   string `json:"label"`
	Body    string `json:"body"`
	Created int64  `json:"created"`
	Kind    string `json:"kind,omitempty"`
	To      string `json:"to,omitempty"`
	ReplyTo *int64 `json:"reply_to,omitempty"`
}

// ConversationView holds the title, brief, status and one transcript page.
type ConversationView struct {
	Conversation  string              `json:"conversation"`
	Title         string              `json:"title"`
	Brief         string              `json:"brief"`
	Created       *int64              `json:"created"`
	Archived      bool                `json:"archived"`
	Status        ConversationStatus  `json:"status"`
	Messages      []TranscriptMessage `json:"messages"`
	More          bool                `json:"more"`
	Cursor        int64               `json:"cursor"`
	TotalMessages *int                `json:"total_messages,omitempty"`
}

// JoinInvitation holds a slot's join code and a ready join prompt. The code is a secret.
type JoinInvitation struct {
	Participant string `json:"participant"`
	Role        string `json:"role"`
	Code        string `json:"code"`
	JoinPrompt  string `json:"join_prompt"`
}

// OpenInviteInvitation holds a reusable room-level join code and a ready join prompt.
type OpenInviteInvitation struct {
	Code       string `json:"code"`
	JoinPrompt string `json:"join_prompt"`
}

// OpenInviteStatus reports whether a conversation has an active reusable invite code.
type OpenInviteStatus struct {
	OpenInvite bool   `json:"open_invite"`
	Code       string `json:"code,omitempty"`
	JoinPrompt string `json:"join_prompt,omitempty"`
}

// CreatedConversation holds the new conversation with one invitation per slot.
type CreatedConversation struct {
	Conversation  string                `json:"conversation"`
	Invitations   []JoinInvitation      `json:"invitations"`
	Roster        map[string]string     `json:"roster"`
	OpenInvite    *OpenInviteInvitation `json:"open_invite,omitempty"`
	Round         string                `json:"round,omitempty"`
	RoundDeadline *int64                `json:"round_deadline,omitempty"`
}

// LaunchQueued reports the assignment that will fill the slot.
type LaunchQueued struct {
	AssignmentID string `json:"assignment_id"`
	State        string `json:"state"`
}

// RetiredParticipant confirms that a participant has been retired.
type RetiredParticipant struct {
	Participant string `json:"participant"`
	Retired     bool   `json:"retired"`
}

// ParticipantRoleChanged confirms a participant's new role and the updated roster.
type ParticipantRoleChanged struct {
	Participant string            `json:"participant"`
	Role        string            `json:"role"`
	Roster      map[string]string `json:"roster"`
}

// PostedMessage holds the ID of the posted message.
type PostedMessage struct {
	MessageID int64 `json:"message_id"`
}

// ClosedConversation confirms that the conversation is closed.
type ClosedConversation struct {
	Closed bool `json:"closed"`
}

// ReopenedConversation confirms that the conversation is open again.
type ReopenedConversation struct {
	Reopened bool `json:"reopened"`
}

// ArchivedConversation reports whether the conversation is now in the archive.
type ArchivedConversation struct {
	Archived bool `json:"archived"`
}

// RenamedConversation holds the title that list views now show.
type RenamedConversation struct {
	Title string `json:"title"`
}

// SearchHit locates one match. A match in the brief has no message ID or author.
type SearchHit struct {
	Conversation string  `json:"conversation"`
	Title        string  `json:"title"`
	Archived     bool    `json:"archived"`
	MessageID    *int64  `json:"message_id"`
	Author       *string `json:"author"`
	Created      *int64  `json:"created"`
	Snippet      string  `json:"snippet"`
}

// SearchResults holds up to 50 matches, best first.
type SearchResults struct {
	Hits []SearchHit `json:"hits"`
}

// Client talks to the core over its Unix socket with one request per connection.
type Client struct {
	operator *wire.OperatorClient
	timeout  time.Duration
}

// NewClient returns a client for the bridge data directory home.
func NewClient(home string) *Client {
	return &Client{operator: wire.NewOperatorClient(home), timeout: callTimeout}
}

// bridgeHome returns the validated bridge data directory from AGENTCY_HOME or ~/.agentcy.
func bridgeHome() (string, error) {
	home, set := os.LookupEnv(paths.HomeVariable)
	return paths.DataDir(home, set)
}

// Plugins lists the configured plugin names and models.
func (c *Client) Plugins() (PluginList, error) {
	var result PluginList
	err := c.call("operator_plugins", PluginsRequest{}, &result)
	return result, err
}

// List lists the conversations of the request's checkout.
func (c *Client) List(request ListRequest) (ConversationList, error) {
	var result ConversationList
	err := c.call("operator_list", request, &result)
	return result, err
}

// Show returns the brief, status and one transcript page.
func (c *Client) Show(request ShowRequest) (ConversationView, error) {
	var result ConversationView
	err := c.call("operator_show", request, &result)
	return result, err
}

// Tasks returns all tasks in one conversation with their state and timeline.
func (c *Client) Tasks(request TasksRequest) (TaskList, error) {
	var result TaskList
	err := c.call("operator_tasks", request, &result)
	return result, err
}

// Create creates a conversation and returns an invitation per slot.
func (c *Client) Create(request CreateRequest) (CreatedConversation, error) {
	var result CreatedConversation
	err := c.call("operator_create", request, &result)
	return result, err
}

// Invite adds or reissues a slot and returns its ready join prompt.
func (c *Client) Invite(request InviteRequest) (JoinInvitation, error) {
	var result JoinInvitation
	err := c.call("operator_invite", request, &result)
	return result, err
}

// OpenInvite enables, rotates, or disables a reusable room-level join code.
func (c *Client) OpenInvite(request OpenInviteRequest) (OpenInviteStatus, error) {
	var result OpenInviteStatus
	err := c.call("operator_open_invite", request, &result)
	return result, err
}

// Launch queues a fresh run for a slot.
func (c *Client) Launch(request LaunchRequest) (LaunchQueued, error) {
	var result LaunchQueued
	err := c.call("operator_launch", request, &result)
	return result, err
}

// Retire retires a participant from an open conversation.
func (c *Client) Retire(request RetireRequest) (RetiredParticipant, error) {
	var result RetiredParticipant
	err := c.call("operator_retire", request, &result)
	return result, err
}

// SetRole changes an active participant's role in an open conversation.
func (c *Client) SetRole(request SetRoleRequest) (ParticipantRoleChanged, error) {
	var result ParticipantRoleChanged
	err := c.call("operator_set_role", request, &result)
	return result, err
}

// SetMemberBudget updates a participant's model and/or starting token budget.
func (c *Client) SetMemberBudget(request SetMemberBudgetRequest) (MemberBudgetUpdated, error) {
	var result MemberBudgetUpdated
	err := c.call("operator_set_member_budget", request, &result)
	return result, err
}

// Post posts a message as the human.
func (c *Client) Post(request PostRequest) (PostedMessage, error) {
	var result PostedMessage
	err := c.call("operator_post", request, &result)
	return result, err
}

// Close closes a conversation.
func (c *Client) Close(request CloseRequest) (ClosedConversation, error) {
	var result ClosedConversation
	err := c.call("operator_close", request, &result)
	return result, err
}

// Reopen reopens a closed conversation.
func (c *Client) Reopen(request ReopenRequest) (ReopenedConversation, error) {
	var result ReopenedConversation
	err := c.call("operator_reopen", request, &result)
	return result, err
}

// Archive archives or unarchives a closed conversation.
func (c *Client) Archive(request ArchiveRequest) (ArchivedConversation, error) {
	var result ArchivedConversation
	err := c.call("operator_archive", request, &result)
	return result, err
}

// Rename sets or clears a conversation's custom title.
func (c *Client) Rename(request RenameRequest) (RenamedConversation, error) {
	var result RenamedConversation
	err := c.call("operator_rename", request, &result)
	return result, err
}

// Search finds words in the workspace's briefs and messages.
func (c *Client) Search(request SearchRequest) (SearchResults, error) {
	var result SearchResults
	err := c.call("operator_search", request, &result)
	return result, err
}

// call sends one request through wire.OperatorClient and wraps wire.CoreError into the
// TUI's CoreError (which adds the stale-core restart hint for Invalid bridge arguments).
func (c *Client) call(action string, request any, result any) error {
	c.operator.Timeout = c.timeout
	err := c.operator.Call(context.Background(), action, request, result)
	var wireErr *wire.CoreError
	if errors.As(err, &wireErr) {
		return &CoreError{Message: wireErr.Message}
	}
	return err
}

// encodeRequest merges the envelope into the request object as one JSON line.
func encodeRequest(action string, key string, request any) ([]byte, error) {
	return wire.EncodeOperatorRequest(action, key, request)
}

// decodeResponse maps an error line to CoreError and decodes a result line into result.
func decodeResponse(reply []byte, result any) error {
	err := wire.DecodeOperatorResponse(reply, result)
	var wireErr *wire.CoreError
	if errors.As(err, &wireErr) {
		return &CoreError{Message: wireErr.Message}
	}
	return err
}
