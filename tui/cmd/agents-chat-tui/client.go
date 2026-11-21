package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	protocolVersion  = 1
	operatorKeyFile  = "operator.key"
	socketFile       = "core.sock"
	keyRejectedError = "Operator key rejected"
	maxKeyBytes      = 1024
	// maxResponseBytes matches MAX_RESPONSE in the Python transport.
	maxResponseBytes = 2 * 1024 * 1024
	dialTimeout      = 2 * time.Second
	callTimeout      = 10 * time.Second
)

// errCoreUnreachable marks a failed dial, for example after the core exited while idle.
var errCoreUnreachable = errors.New("core is not reachable")

// CoreError carries the error text that the core returned for one request.
type CoreError struct {
	Message string
}

// Error returns the core's error text.
func (e *CoreError) Error() string {
	return e.Message
}

// Operator is the set of operator protocol actions that the UI uses.
type Operator interface {
	Plugins() (PluginList, error)
	List(request ListRequest) (ConversationList, error)
	Show(request ShowRequest) (ConversationView, error)
	Create(request CreateRequest) (CreatedConversation, error)
	Invite(request InviteRequest) (JoinInvitation, error)
	Launch(request LaunchRequest) (LaunchQueued, error)
	Post(request PostRequest) (PostedMessage, error)
	Close(request CloseRequest) (ClosedConversation, error)
}

// PluginsRequest lists configured plugins. It takes no fields besides the envelope.
type PluginsRequest struct{}

// ListRequest lists the conversations of a checkout's workspace.
type ListRequest struct {
	Directory string `json:"directory"`
}

// ShowRequest reads status, the brief and one transcript page after a cursor.
type ShowRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	After        int64  `json:"after"`
}

// CreateRequest creates a conversation in which every slot is an invitation.
type CreateRequest struct {
	Directory string   `json:"directory"`
	Brief     string   `json:"brief"`
	Lead      string   `json:"lead"`
	Invitees  []string `json:"invitees"`
}

// InviteRequest adds a slot or reissues the unused code of an existing slot.
type InviteRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Name         string `json:"name"`
}

// LaunchRequest fills a slot with a fresh run from a configured plugin.
type LaunchRequest struct {
	Directory    string `json:"directory"`
	Conversation string `json:"conversation"`
	Name         string `json:"name"`
	Plugin       string `json:"plugin"`
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

// OpenTask identifies one unfinished task of a member.
type OpenTask struct {
	ID      int64 `json:"id"`
	Blocked *bool `json:"blocked,omitempty"`
}

// MemberStatus reports one member's role, state, launch state and open tasks.
type MemberStatus struct {
	Role        string     `json:"role"`
	State       string     `json:"state"`
	LaunchState string     `json:"launch_state,omitempty"`
	IdleSeconds *int64     `json:"idle_seconds,omitempty"`
	Messages    *int64     `json:"messages,omitempty"`
	OpenTasks   []OpenTask `json:"open_tasks,omitempty"`
}

// ConversationStatus reports whether the conversation is open and how each member stands.
type ConversationStatus struct {
	Open          bool                    `json:"open"`
	Members       map[string]MemberStatus `json:"members"`
	Round         string                  `json:"round,omitempty"`
	RoundDeadline *int64                  `json:"round_deadline,omitempty"`
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

// ConversationView holds the brief, status and one transcript page.
type ConversationView struct {
	Conversation string              `json:"conversation"`
	Brief        string              `json:"brief"`
	Created      *int64              `json:"created"`
	Status       ConversationStatus  `json:"status"`
	Messages     []TranscriptMessage `json:"messages"`
	More         bool                `json:"more"`
	Cursor       int64               `json:"cursor"`
}

// JoinInvitation holds a slot's join code and a ready join prompt. The code is a secret.
type JoinInvitation struct {
	Participant string `json:"participant"`
	Role        string `json:"role"`
	Code        string `json:"code"`
	JoinPrompt  string `json:"join_prompt"`
}

// CreatedConversation holds the new conversation with one invitation per slot.
type CreatedConversation struct {
	Conversation  string            `json:"conversation"`
	Invitations   []JoinInvitation  `json:"invitations"`
	Roster        map[string]string `json:"roster"`
	Round         string            `json:"round,omitempty"`
	RoundDeadline *int64            `json:"round_deadline,omitempty"`
}

// LaunchQueued reports the assignment that will fill the slot.
type LaunchQueued struct {
	AssignmentID string `json:"assignment_id"`
	State        string `json:"state"`
}

// PostedMessage holds the ID of the posted message.
type PostedMessage struct {
	MessageID int64 `json:"message_id"`
}

// ClosedConversation confirms that the conversation is closed.
type ClosedConversation struct {
	Closed bool `json:"closed"`
}

// envelope holds the fields that every operator request carries.
type envelope struct {
	Version int    `json:"version"`
	Key     string `json:"key"`
	Action  string `json:"action"`
}

// response is one line from the core: a result or an error.
type response struct {
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
}

// Client talks to the core over its Unix socket with one request per connection.
type Client struct {
	home    string
	timeout time.Duration
	mutex   sync.Mutex
	key     string
}

// NewClient returns a client for the bridge data directory home.
func NewClient(home string) *Client {
	return &Client{home: home, timeout: callTimeout}
}

// bridgeHome returns AGENTS_CHAT_HOME, or ~/.agents-chat when it is unset.
func bridgeHome() (string, error) {
	if home := os.Getenv("AGENTS_CHAT_HOME"); home != "" {
		return filepath.Abs(home)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".agents-chat"), nil
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

// Launch queues a fresh run for a slot.
func (c *Client) Launch(request LaunchRequest) (LaunchQueued, error) {
	var result LaunchQueued
	err := c.call("operator_launch", request, &result)
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

// call sends one request and decodes its result. After a key rejection it re-reads the key
// file once and retries, because the core writes a new key at every start.
func (c *Client) call(action string, request any, result any) error {
	key, err := c.operatorKey(false)
	if err != nil {
		return err
	}
	err = c.exchange(action, key, request, result)
	var coreError *CoreError
	if !errors.As(err, &coreError) || coreError.Message != keyRejectedError {
		return err
	}
	key, err = c.operatorKey(true)
	if err != nil {
		return err
	}
	return c.exchange(action, key, request, result)
}

// operatorKey returns the cached key, reading the key file when asked or when none is cached.
func (c *Client) operatorKey(reload bool) (string, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.key != "" && !reload {
		return c.key, nil
	}
	file, err := os.Open(filepath.Join(c.home, operatorKeyFile))
	if err != nil {
		return "", fmt.Errorf("%w: operator key file is missing", errCoreUnreachable)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxKeyBytes+1))
	if err != nil || len(content) > maxKeyBytes {
		return "", errors.New("operator key file is unreadable")
	}
	key := string(bytes.TrimSpace(content))
	if key == "" {
		return "", errors.New("operator key file is empty")
	}
	c.key = key
	return key, nil
}

// encodeRequest merges the envelope into the request object as one JSON line.
func encodeRequest(action string, key string, request any) ([]byte, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	head, err := json.Marshal(envelope{Version: protocolVersion, Key: key, Action: action})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(head, &fields); err != nil {
		return nil, err
	}
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

// exchange writes one request line and reads one response line within the call deadline.
func (c *Client) exchange(action string, key string, request any, result any) error {
	line, err := encodeRequest(action, key, request)
	if err != nil {
		return err
	}
	connection, err := net.DialTimeout("unix", filepath.Join(c.home, socketFile), dialTimeout)
	if err != nil {
		return errCoreUnreachable
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}
	if _, err := connection.Write(line); err != nil {
		return fmt.Errorf("sending the request failed: %w", err)
	}
	reply, err := bufio.NewReader(io.LimitReader(connection, maxResponseBytes+1)).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("incomplete response from the core: %w", err)
	}
	return decodeResponse(reply, result)
}

// decodeResponse maps an error line to CoreError and decodes a result line into result.
func decodeResponse(reply []byte, result any) error {
	var decoded response
	if err := json.Unmarshal(reply, &decoded); err != nil {
		return errors.New("malformed response from the core")
	}
	if decoded.Error != nil {
		return &CoreError{Message: *decoded.Error}
	}
	if decoded.Result == nil {
		return errors.New("malformed response from the core")
	}
	if err := json.Unmarshal(decoded.Result, result); err != nil {
		return errors.New("unexpected result from the core")
	}
	return nil
}
