package conversation

import (
	"errors"
	"fmt"

	"github.com/witseko/agentcy/internal/core"
)

// The refusals that several rules share, copied from the Python core.
const (
	retiredError           = "You have been retired from this conversation"
	conversationClosed     = "Conversation is closed"
	invalidToken           = "Invalid participant token"
	unknownParticipant     = "Unknown participant in this conversation"
	submissionIDReused     = "Submission ID already used for a different message"
	noSealedRound          = "This conversation has no sealed proposal round"
	joinWorkspaceHelp      = "Set both clients to the same --workspace and reconnect."
	invalidInvitation      = "Invalid or expired invitation"
	unknownConversation    = "Unknown conversation in this workspace"
	unknownClosed          = "Unknown closed conversation in this workspace"
	unknownMcpTask         = "Failed to retrieve task: Task not found"
	participantCapTemplate = "Conversation already has %d participants"
)

// reject returns a refusal whose message reaches the client, like a Python ValueError.
func reject(message string) error {
	return core.Reject(message)
}

// rejectf formats a refusal. The arguments must never hold a secret.
func rejectf(format string, arguments ...any) error {
	return core.Reject(fmt.Sprintf(format, arguments...))
}

// errUnknownOperation is an internal error for a request type that no rule handles.
var errUnknownOperation = errors.New("unknown conversation operation")
